package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"servicectl/internal/util"
	"servicectl/internal/visionapi"
)

type eventSubscriber struct {
	ch chan visionapi.EventEnvelope
}

type servicectlPlaneServer struct {
	mode             string
	cfg              Config
	hub              *servicectlEventHub
	mu               sync.Mutex
	queryUnitLists   func(string) (visionapi.UnitListsResponse, error)
	replaceUnitList  func(string, string, []string) error
	scanRunnerUnits  func(Config) ([]string, error)
	enabledUnits     func() []string
	collectSnapshots func(Config, []string) visionapi.UnitsResponse
}

type servicectlEventHub struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]eventSubscriber
}

var unitSnapshotConfigMu sync.Mutex

func newServicectlEventHub() *servicectlEventHub {
	return &servicectlEventHub{subs: make(map[int]eventSubscriber)}
}

func (h *servicectlEventHub) subscribe() (int, chan visionapi.EventEnvelope) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	ch := make(chan visionapi.EventEnvelope, 32)
	h.subs[h.nextID] = eventSubscriber{ch: ch}
	return h.nextID, ch
}

func (h *servicectlEventHub) unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sub, ok := h.subs[id]; ok {
		delete(h.subs, id)
		close(sub.ch)
	}
}

func (h *servicectlEventHub) publish(event visionapi.EventEnvelope) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, sub := range h.subs {
		select {
		case sub.ch <- event:
		default:
			delete(h.subs, id)
			close(sub.ch)
		}
	}
}

func (h *servicectlEventHub) serveIngress(mode string, socketPath string) error {
	if err := visionapi.PrepareUnixDatagramListener(socketPath); err != nil {
		return err
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer func() {
		_ = conn.Close()
		_ = os.Remove(socketPath)
	}()
	_ = os.Chmod(socketPath, 0660)
	buf := make([]byte, 65535)
	for {
		n, _, err := conn.ReadFromUnix(buf)
		if err != nil {
			return err
		}
		var event visionapi.EventEnvelope
		if err := json.Unmarshal(buf[:n], &event); err != nil {
			continue
		}
		if strings.TrimSpace(event.Mode) == "" {
			event.Mode = visionapi.PlaneForMode(mode).Mode
		}
		if present, ok := runnerEventUpdate(event); ok && strings.TrimSpace(event.Unit) != "" {
			_ = propertySetRunnerUnitForMode(event.Mode, event.Unit, present)
		}
		h.publish(event)
	}
}

func buildUnitSnapshot(cfg Config, unitName string) (visionapi.UnitSnapshot, error) {
	unitSnapshotConfigMu.Lock()
	defer unitSnapshotConfigMu.Unlock()
	previous := config
	config = cfg
	defer func() {
		config = previous
	}()
	unit, err := parseSystemdUnit(unitName)
	if err != nil {
		return visionapi.UnitSnapshot{}, err
	}
	socketUnit, _ := parseOptionalSocketUnit(unitName)
	managementMode := managedServiceModeForUnit(unit, socketUnit)
	dinitName := backendServiceNameForUnit(unitName)
	loggerName := loggerServiceName(dinitName)
	status := dinitStatus(dinitName)
	runtimeState := map[string]string(nil)
	managedBy := "dinit"
	if managementMode != managedDirect {
		managedBy = "sys-notifyd"
		runtimeState = parseKeyValueFile(notifydStatePath(unitName, managementMode))
	}
	processID := statusValue(status, "Process ID")
	managerPID := ""
	mainPID := processID
	if runtimeState != nil {
		managerPID = processID
		if value := mapValue(runtimeState, "main_pid"); value != "-" {
			mainPID = value
		}
	}
	if mainPID == "0" {
		mainPID = ""
	}
	if managerPID == "0" {
		managerPID = ""
	}
	return visionapi.UnitSnapshot{
		Name:         unitName,
		Description:  unit.Description,
		Mode:         cfg.Mode,
		SourcePath:   unit.SourcePath,
		ManagedBy:    managedBy,
		DinitName:    dinitName,
		LoggerName:   loggerName,
		State:        statusValue(status, "State"),
		Activation:   statusValue(status, "Activation"),
		ProcessID:    processID,
		ManagerPID:   managerPID,
		MainPID:      mainPID,
		Phase:        mapValue(runtimeState, "phase"),
		ChildState:   mapValue(runtimeState, "child_state"),
		Status:       mapValue(runtimeState, "status"),
		Failure:      mapValue(runtimeState, "failure"),
		NotifySocket: notifySocketPath(unitName, unit, socketUnit),
		BusName:      util.FirstNonEmpty(mapValue(runtimeState, "bus_name"), unit.BusName),
		BusOwner:     mapValue(runtimeState, "bus_owner"),
		StateFile:    managedStateFilePath(unitName, unit, socketUnit),
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func discoverSystemdUnits(cfg Config) []string {
	seen := make(map[string]bool)
	units := make([]string, 0, 32)
	for _, base := range cfg.SystemdPaths {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".service") || strings.Contains(name, "@") {
				continue
			}
			clean := strings.TrimSuffix(name, ".service")
			if clean == "" || seen[clean] {
				continue
			}
			seen[clean] = true
			units = append(units, clean)
		}
	}
	sort.Strings(units)
	return units
}

func collectUnitSnapshots(cfg Config, units []string) visionapi.UnitsResponse {
	units = normalizeUnitListNames(units)
	result := visionapi.UnitsResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	result.Units = make([]visionapi.UnitSnapshot, 0, len(units))
	for _, unitName := range units {
		snapshot, err := buildUnitSnapshot(cfg, unitName)
		if err != nil {
			continue
		}
		result.Units = append(result.Units, snapshot)
	}
	return result
}

func normalizeUnitListNames(units []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(units))
	for _, unit := range units {
		unit = strings.TrimSuffix(strings.TrimSpace(unit), ".service")
		if unit == "" || seen[unit] {
			continue
		}
		seen[unit] = true
		result = append(result, unit)
	}
	sort.Strings(result)
	return result
}

func runnerEventUpdate(event visionapi.EventEnvelope) (bool, bool) {
	if !strings.EqualFold(strings.TrimSpace(event.Payload["result"]), "ok") {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(event.Payload["action"])) {
	case "start", "restart":
		return true, true
	case "stop":
		return false, true
	default:
		return false, false
	}
}

func scanRunnerUnits(cfg Config) ([]string, error) {
	result := make([]string, 0)
	for _, unit := range discoverSystemdUnits(cfg) {
		snapshot, err := buildUnitSnapshot(cfg, unit)
		if err == nil && snapshotIsRunning(snapshot) {
			result = append(result, unit)
		}
	}
	return normalizeUnitListNames(result), nil
}

func snapshotIsRunning(snapshot visionapi.UnitSnapshot) bool {
	if strings.EqualFold(strings.TrimSpace(snapshot.State), "STARTED") {
		return true
	}
	pid, err := strconv.Atoi(strings.TrimSpace(snapshot.MainPID))
	if err != nil || pid <= 0 {
		return false
	}
	_, err = os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

func writeEvent(w io.Writer, event visionapi.EventEnvelope) error {
	return json.NewEncoder(w).Encode(event)
}

func publishServicectlEvent(event visionapi.EventEnvelope) {
	addr := &net.UnixAddr{Name: visionapi.ServicectlEventsSocketPathForMode(event.Mode), Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = conn.Write(data)
}

func publishServicectlCommandEvent(action string, unitName string, result string) {
	payload := map[string]string{"action": action, "result": result}
	publishServicectlEvent(visionapi.NewEvent(config.Mode, visionapi.SourceServicectl, visionapi.KindUnitCommand, unitName, payload))
}

func newServicectlPlaneServer(mode string, hub *servicectlEventHub) servicectlPlaneServer {
	return servicectlPlaneServer{
		mode:             visionapi.PlaneForMode(mode).Mode,
		cfg:              buildConfig(strings.EqualFold(strings.TrimSpace(mode), visionapi.ModeUser)),
		hub:              hub,
		queryUnitLists:   propertyUnitListsForMode,
		replaceUnitList:  propertyReplaceUnitListForMode,
		scanRunnerUnits:  scanRunnerUnits,
		enabledUnits:     enabledStandaloneServicesFromS6Bundle,
		collectSnapshots: collectUnitSnapshots,
	}
}

func (s *servicectlPlaneServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/units", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if util.ExternalManagedValueEnabled(r.URL.Query().Get("refresh")) {
			if err := s.refreshPropertyLists(); err != nil {
				w.WriteHeader(http.StatusBadGateway)
				util.WriteJSON(w, map[string]string{"error": err.Error()})
				return
			}
		}
		lists, err := s.queryUnitLists(s.mode)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			util.WriteJSON(w, map[string]string{"error": err.Error()})
			return
		}
		util.WriteJSON(w, s.collectSnapshots(s.cfg, lists.EffectiveUnits))
	})
	mux.HandleFunc("/v1/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := s.refreshPropertyLists(); err != nil {
			w.WriteHeader(http.StatusBadGateway)
			util.WriteJSON(w, map[string]string{"error": err.Error()})
			return
		}
		util.WriteJSON(w, map[string]string{"result": "ok"})
	})
	mux.HandleFunc("/v1/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		filter := visionapi.WatchFilter{Mode: strings.TrimSpace(r.URL.Query().Get("mode"))}
		id, ch := s.hub.subscribe()
		defer s.hub.unsubscribe(id)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		writer := bufio.NewWriter(w)
		defer writer.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}
				if !filter.Matches(event) {
					continue
				}
				if err := writeEvent(writer, event); err != nil {
					return
				}
				if err := writer.Flush(); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("/v1/unit/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/v1/unit/")
		name = strings.TrimSuffix(name, ".service")
		if name == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		snapshot, err := buildUnitSnapshot(s.cfg, name)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			util.WriteJSON(w, map[string]string{"error": err.Error()})
			return
		}
		util.WriteJSON(w, snapshot)
	})
	return mux
}

func (s *servicectlPlaneServer) refreshPropertyLists() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lists, err := s.queryUnitLists(s.mode)
	if err != nil {
		return fmt.Errorf("query unit lists: %w", err)
	}
	if !lists.EnabledInitialized {
		if err := s.replaceUnitList(s.mode, "/v1/enabled-list", s.enabledUnits()); err != nil {
			return fmt.Errorf("initialize enabled list: %w", err)
		}
	}
	runners, err := s.scanRunnerUnits(s.cfg)
	if err != nil {
		return fmt.Errorf("scan runner list: %w", err)
	}
	if err := s.replaceUnitList(s.mode, "/v1/runner-list", runners); err != nil {
		return fmt.Errorf("refresh runner list: %w", err)
	}
	return nil
}

func (s *servicectlPlaneServer) apiSocketPath() string {
	return visionapi.ServicectlSocketPathForMode(s.mode)
}

func (s *servicectlPlaneServer) ingressSocketPath() string {
	return visionapi.ServicectlEventsSocketPathForMode(s.mode)
}

func (s *servicectlPlaneServer) serveIngress() error {
	for {
		if err := s.hub.serveIngress(s.mode, s.ingressSocketPath()); err != nil {
			fmt.Println(oneLineError(s.mode+" servicectl event ingress failed", err))
			time.Sleep(time.Second)
		}
	}
}

func (s *servicectlPlaneServer) serveAPI() error {
	socketPath := s.apiSocketPath()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		return fmt.Errorf("create servicectl runtime directory: %w", err)
	}
	if err := visionapi.PrepareUnixStreamListener(socketPath); err != nil {
		return fmt.Errorf("prepare servicectl api socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on servicectl api socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0660); err != nil {
		return fmt.Errorf("chmod servicectl api socket: %w", err)
	}
	server := &http.Server{Handler: s.handler()}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func selectedServicectlPlane(hub *servicectlEventHub) servicectlPlaneServer {
	mode := visionapi.ModeSystem
	if userMode() {
		mode = visionapi.ModeUser
	}
	return newServicectlPlaneServer(mode, hub)
}

func servicectlAPIServer() int {
	hub := newServicectlEventHub()
	server := selectedServicectlPlane(hub)
	if err := server.refreshPropertyLists(); err != nil {
		fmt.Println(oneLineError("initialize servicectl unit lists", err))
		return 1
	}
	errCh := make(chan error, 2)
	go func() { errCh <- server.serveIngress() }()
	go func() { errCh <- server.serveAPI() }()
	err := <-errCh
	if err != nil {
		fmt.Println(oneLineError("servicectl api server failed", err))
		return 1
	}
	return 0
}
