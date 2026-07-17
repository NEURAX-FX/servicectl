package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
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

	"servicectl/internal/statusview"
	"servicectl/internal/util"
	"servicectl/internal/visionapi"
)

type eventSubscriber struct {
	ch chan visionapi.EventEnvelope
}

type servicectlPlaneServer struct {
	mode                   string
	cfg                    Config
	hub                    *servicectlEventHub
	mu                     sync.Mutex
	queryUnitLists         func(string) (visionapi.UnitListsResponse, error)
	queryUnitGroups        func(string, string) (visionapi.UnitGroupsResponse, error)
	replaceUnitList        func(string, string, []string) error
	scanRunnerUnits        func(Config) ([]string, error)
	enabledUnits           func() []string
	discoverUnits          func(Config) []string
	collectSnapshots       func(Config, []string) visionapi.UnitsResponse
	canonicalizeStatusUnit func(Config, string) (string, error)
	buildStatusManifest    func(Config, string, visionapi.UnitListsResponse, string) (visionapi.StatusParticipationManifest, error)
}

var errStatusManifestUnitNotFound = errors.New("status manifest unit not found")

type statusManifestAPIError struct {
	Error statusManifestAPIErrorDetail `json:"error"`
}

type statusManifestAPIErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Unit    string `json:"unit,omitempty"`
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

func (h *servicectlEventHub) serveIngress(mode string, socketPath string, notifyReady func()) error {
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
	if err := os.Chmod(socketPath, 0660); err != nil {
		return err
	}
	if notifyReady != nil {
		notifyReady()
	}
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
		mode:                   visionapi.PlaneForMode(mode).Mode,
		cfg:                    buildConfig(strings.EqualFold(strings.TrimSpace(mode), visionapi.ModeUser)),
		hub:                    hub,
		queryUnitLists:         propertyUnitListsForMode,
		queryUnitGroups:        propertyUnitGroupsForMode,
		replaceUnitList:        propertyReplaceUnitListForMode,
		scanRunnerUnits:        scanRunnerUnits,
		enabledUnits:           enabledStandaloneServicesFromS6Bundle,
		discoverUnits:          discoverSystemdUnits,
		collectSnapshots:       collectUnitSnapshots,
		canonicalizeStatusUnit: canonicalStatusManifestUnitForConfig,
		buildStatusManifest:    buildStatusParticipationManifestForConfig,
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
		units := lists.EffectiveUnits
		if util.ExternalManagedValueEnabled(r.URL.Query().Get("all")) {
			units = append([]string{}, s.discoverUnits(s.cfg)...)
			units = append(units, lists.EnabledUnits...)
			units = append(units, lists.RunnerUnits...)
			units = append(units, lists.EffectiveUnits...)
			units = normalizeUnitListNames(units)
		}
		util.WriteJSON(w, s.collectSnapshots(s.cfg, units))
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
	mux.HandleFunc("/v1/status-manifest/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/status-manifest/"))
		if r.Method != http.MethodGet {
			writeStatusManifestAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Status manifest only supports GET.", name)
			return
		}
		if name == "" || strings.Contains(name, "/") {
			writeStatusManifestAPIError(w, http.StatusBadRequest, "invalid_unit", "A single unit name is required.", name)
			return
		}
		name = strings.TrimSuffix(name, ".service") + ".service"
		canonicalName, err := s.canonicalizeStatusUnit(s.cfg, name)
		if err != nil {
			if errors.Is(err, errStatusManifestUnitNotFound) {
				writeStatusManifestAPIError(w, http.StatusNotFound, "unit_not_found", err.Error(), name)
				return
			}
			writeStatusManifestAPIError(w, http.StatusInternalServerError, "manifest_build_failed", err.Error(), name)
			return
		}
		name = canonicalName
		lists, err := s.queryUnitLists(s.mode)
		if err != nil {
			writeStatusManifestAPIError(w, http.StatusBadGateway, "manager_unavailable", err.Error(), name)
			return
		}
		orchestrator, err := s.statusOrchestrator(name, lists)
		if err != nil {
			writeStatusManifestAPIError(w, http.StatusBadGateway, "manager_unavailable", err.Error(), name)
			return
		}
		manifest, err := s.buildStatusManifest(s.cfg, name, lists, orchestrator)
		if err != nil {
			if errors.Is(err, errStatusManifestUnitNotFound) {
				writeStatusManifestAPIError(w, http.StatusNotFound, "unit_not_found", err.Error(), name)
				return
			}
			writeStatusManifestAPIError(w, http.StatusInternalServerError, "manifest_build_failed", err.Error(), name)
			return
		}
		util.WriteJSON(w, manifest)
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

func writeStatusManifestAPIError(w http.ResponseWriter, status int, code, message, unit string) {
	w.WriteHeader(status)
	util.WriteJSON(w, statusManifestAPIError{Error: statusManifestAPIErrorDetail{
		Code:    code,
		Message: message,
		Unit:    unit,
	}})
}

func (s *servicectlPlaneServer) statusOrchestrator(unitName string, lists visionapi.UnitListsResponse) (string, error) {
	return statusOrchestratorFromLists(s.mode, unitName, lists, s.queryUnitGroups)
}

func statusOrchestratorFromLists(mode, unitName string, lists visionapi.UnitListsResponse, queryUnitGroups func(string, string) (visionapi.UnitGroupsResponse, error)) (string, error) {
	canonical := normalizeServiceUnitName(unitName)
	if statusListContainsUnit(lists.EnabledUnits, canonical) {
		return s6OrchestrdServiceName(canonical), nil
	}
	if !statusListContainsUnit(lists.EffectiveUnits, canonical) || len(lists.EnabledGroups) == 0 {
		return "", nil
	}
	if queryUnitGroups == nil {
		return "", fmt.Errorf("unit group query is unavailable")
	}
	groups, err := queryUnitGroups(mode, canonical)
	if err != nil {
		return "", fmt.Errorf("query group ownership for %s: %w", canonical, err)
	}
	enabledGroups := make(map[string]bool, len(lists.EnabledGroups))
	for _, group := range lists.EnabledGroups {
		enabledGroups[strings.TrimSpace(group)] = true
	}
	owners := make([]string, 0, len(groups.Groups))
	for _, group := range groups.Groups {
		name := strings.TrimSpace(group.Name)
		if group.Enabled && enabledGroups[name] {
			owners = append(owners, name)
		}
	}
	if len(owners) == 0 {
		if statusListContainsUnit(lists.RunnerUnits, canonical) {
			return "", nil
		}
		return "", fmt.Errorf("enabled group ownership for %s is incomplete", canonical)
	}
	sort.Strings(owners)
	return s6GroupOrchestrdServiceName(owners[0]), nil
}

func statusListContainsUnit(units []string, canonical string) bool {
	for _, unit := range units {
		if normalizeServiceUnitName(unit) == canonical {
			return true
		}
	}
	return false
}

func canonicalStatusManifestUnitForConfig(cfg Config, unitName string) (string, error) {
	unitSnapshotConfigMu.Lock()
	defer unitSnapshotConfigMu.Unlock()
	previous := config
	config = cfg
	defer func() { config = previous }()

	return statusManifestUnitName(resolveUnitAlias(unitName))
}

func buildStatusParticipationManifestForConfig(cfg Config, unitName string, lists visionapi.UnitListsResponse, orchestrator string) (visionapi.StatusParticipationManifest, error) {
	unitSnapshotConfigMu.Lock()
	defer unitSnapshotConfigMu.Unlock()
	previous := config
	config = cfg
	defer func() { config = previous }()

	unit, err := parseSystemdUnit(unitName)
	if err != nil {
		if isStatusUnitNotFoundError(err) {
			return visionapi.StatusParticipationManifest{}, fmt.Errorf("%w: %v", errStatusManifestUnitNotFound, err)
		}
		return visionapi.StatusParticipationManifest{}, err
	}
	socketUnit, socketErr := parseOptionalSocketUnit(unit.Name)
	if socketErr != nil {
		socketUnit = nil
	}
	generatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lists.GeneratedAt))
	if err != nil {
		generatedAt = time.Now().UTC()
	}
	uid := uint32(0)
	if cfg.Mode == visionapi.ModeUser {
		uid = uint32(os.Getuid())
	}
	manifest, err := buildStatusParticipationManifest(statusManifestInput{
		Unit:                unit,
		SocketUnit:          socketUnit,
		Mode:                cfg.Mode,
		UID:                 uid,
		Enabled:             strings.TrimSpace(orchestrator) != "",
		OrchestratorService: orchestrator,
		GeneratedAt:         generatedAt,
	})
	if err != nil {
		return visionapi.StatusParticipationManifest{}, err
	}
	manifest.Source = string(statusview.EvidenceManager)
	return manifest, nil
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

func (s *servicectlPlaneServer) serveIngress(notifyReady func()) error {
	return s.hub.serveIngress(s.mode, s.ingressSocketPath(), notifyReady)
}

func (s *servicectlPlaneServer) serveAPI(notifyReady func()) error {
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
	if notifyReady != nil {
		notifyReady()
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

func parseServicectlAPIReadyFD(args []string) (int, error) {
	fs := flag.NewFlagSet("serve-api", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	readyFD := fs.Int("ready-fd", -1, "write a newline to this file descriptor after both API sockets are ready")
	if err := fs.Parse(args); err != nil {
		return -1, err
	}
	if fs.NArg() != 0 {
		return -1, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *readyFD >= 0 && *readyFD < 3 {
		return -1, fmt.Errorf("ready fd must be at least 3")
	}
	return *readyFD, nil
}

func writeServicectlAPIReady(writer io.Writer) error {
	if writer == nil {
		return nil
	}
	_, err := io.WriteString(writer, "\n")
	return err
}

func servicectlAPIServer(readyFD int) int {
	hub := newServicectlEventHub()
	server := selectedServicectlPlane(hub)
	if err := server.refreshPropertyLists(); err != nil {
		fmt.Println(oneLineError("initialize servicectl unit lists", err))
		return 1
	}
	errCh := make(chan error, 2)
	ingressReady := make(chan struct{})
	apiReady := make(chan struct{})
	var ingressReadyOnce sync.Once
	var apiReadyOnce sync.Once
	go func() { errCh <- server.serveIngress(func() { ingressReadyOnce.Do(func() { close(ingressReady) }) }) }()
	go func() { errCh <- server.serveAPI(func() { apiReadyOnce.Do(func() { close(apiReady) }) }) }()
	for ingressReady != nil || apiReady != nil {
		select {
		case <-ingressReady:
			ingressReady = nil
		case <-apiReady:
			apiReady = nil
		case err := <-errCh:
			fmt.Println(oneLineError("servicectl api server failed", err))
			return 1
		}
	}
	var readyWriter io.Writer
	if readyFD >= 3 {
		readyWriter = os.NewFile(uintptr(readyFD), "servicectl-api-ready")
	}
	if err := writeServicectlAPIReady(readyWriter); err != nil {
		fmt.Println(oneLineError("notify servicectl api readiness", err))
		return 1
	}
	err := <-errCh
	if err != nil {
		fmt.Println(oneLineError("servicectl api server failed", err))
		return 1
	}
	return 0
}
