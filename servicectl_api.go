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
	"strings"
	"sync"
	"time"

	"servicectl/internal/visionapi"
)

type eventSubscriber struct {
	ch chan visionapi.EventEnvelope
}

type servicectlEventHub struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]eventSubscriber
}

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

func (h *servicectlEventHub) serveIngress() error {
	socketPath := visionapi.ServicectlEventsSocketPath(userMode(), runtimeDir())
	_ = os.Remove(socketPath)
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
		h.publish(event)
	}
}

func buildUnitSnapshot(unitName string) (visionapi.UnitSnapshot, error) {
	unit, err := parseSystemdUnit(unitName)
	if err != nil {
		return visionapi.UnitSnapshot{}, err
	}
	socketUnit, _ := parseOptionalSocketUnit(unitName)
	dinitName := dinitNameForUnit(unitName)
	loggerName := loggerServiceName(dinitName)
	status := dinitStatus(dinitName)
	runtimeState := map[string]string(nil)
	managedBy := "dinit"
	if shouldManageWithNotifyd(unit, socketUnit) {
		managedBy = "sys-notifyd"
		runtimeState = parseKeyValueFile(notifydStatePath(unitName))
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
		Mode:         config.Mode,
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
		StateFile:    managedStateFilePath(unitName, unit, socketUnit),
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func discoverSystemdUnits() []string {
	seen := make(map[string]bool)
	units := make([]string, 0, 32)
	for _, base := range config.SystemdPaths {
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

func collectUnitSnapshots() visionapi.UnitsResponse {
	units := discoverSystemdUnits()
	result := visionapi.UnitsResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	result.Units = make([]visionapi.UnitSnapshot, 0, len(units))
	for _, unitName := range units {
		snapshot, err := buildUnitSnapshot(unitName)
		if err != nil {
			continue
		}
		result.Units = append(result.Units, snapshot)
	}
	return result
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(value)
}

func writeEvent(w io.Writer, event visionapi.EventEnvelope) error {
	return json.NewEncoder(w).Encode(event)
}

func publishServicectlEvent(event visionapi.EventEnvelope) {
	addr := &net.UnixAddr{Name: visionapi.ServicectlEventsSocketPath(userMode(), runtimeDir()), Net: "unixgram"}
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
	publishServicectlEvent(visionapi.NewEvent(visionapi.SourceServicectl, visionapi.KindUnitCommand, unitName, payload))
}

func servicectlAPIServer() int {
	socketPath := visionapi.ServicectlSocketPath(userMode(), runtimeDir())
	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		fmt.Println(oneLineError("create servicectl runtime directory", err))
		return 1
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		fmt.Println(oneLineError("listen on servicectl api socket", err))
		return 1
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0660); err != nil {
		fmt.Println(oneLineError("chmod servicectl api socket", err))
		return 1
	}
	hub := newServicectlEventHub()
	go func() {
		if err := hub.serveIngress(); err != nil {
			fmt.Println(oneLineError("servicectl event ingress failed", err))
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/units", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, collectUnitSnapshots())
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
		id, ch := hub.subscribe()
		defer hub.unsubscribe(id)
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
		snapshot, err := buildUnitSnapshot(name)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, snapshot)
	})
	server := &http.Server{Handler: mux}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Println(oneLineError("servicectl api server failed", err))
		return 1
	}
	return 0
}
