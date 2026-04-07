package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type startupDebugger struct {
	enabled bool
	path    string
	mu      sync.Mutex
}

func newStartupDebugger() *startupDebugger {
	if !startupDebugEnabled() {
		return &startupDebugger{}
	}
	path := strings.TrimSpace(os.Getenv("SERVICECTL_DEBUG_STARTUP_FILE"))
	if path == "" {
		path = "/run/servicectl/debug-startup.jsonl"
	}
	return &startupDebugger{enabled: true, path: path}
}

func startupDebugEnabled() bool {
	value := strings.TrimSpace(os.Getenv("SERVICECTL_DEBUG_STARTUP"))
	return value == "1" || strings.EqualFold(value, "true")
}

func (d *startupDebugger) event(event string, fields map[string]any) {
	if d == nil || !d.enabled {
		return
	}
	record := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "event": event}
	for key, value := range fields {
		record[key] = value
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(d.path), 0755); err != nil {
		return
	}
	f, err := os.OpenFile(d.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

func (d *startupDebugger) logMungeProbe(context string) {
	if d == nil || !d.enabled {
		return
	}
	fields := map[string]any{"context": context}
	for _, path := range []string{"/run/munge/munged.pid", "/run/munge/munge.socket.2"} {
		_, err := os.Stat(path)
		fields[path] = err == nil
	}
	cmd := exec.Command("/bin/sh", "-c", "munge -n | unmunge >/dev/null 2>&1")
	fields["roundtrip_ok"] = cmd.Run() == nil
	d.event("munge.probe", fields)
}
