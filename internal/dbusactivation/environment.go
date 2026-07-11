package dbusactivation

import (
	"errors"
	"sort"
	"strings"
	"sync"
)

var blockedEnvironment = map[string]struct{}{
	"LD_PRELOAD":                {},
	"LD_LIBRARY_PATH":           {},
	"LD_AUDIT":                  {},
	"GCONV_PATH":                {},
	"GLIBC_TUNABLES":            {},
	"MALLOC_TRACE":              {},
	"PYTHONHOME":                {},
	"PYTHONPATH":                {},
	"PERL5LIB":                  {},
	"PERLLIB":                   {},
	"RUBYLIB":                   {},
	"RUBYOPT":                   {},
	"NODE_OPTIONS":              {},
	"NODE_PATH":                 {},
	"BASH_ENV":                  {},
	"ENV":                       {},
	"DBUS_SESSION_BUS_ADDRESS":  {},
	"DBUS_SESSION_BUS_PID":      {},
	"DBUS_SESSION_BUS_WINDOWID": {},
	"DBUS_STARTER_ADDRESS":      {},
	"DBUS_STARTER_BUS_TYPE":     {},
	"DISPLAY":                   {},
	"WAYLAND_DISPLAY":           {},
	"XAUTHORITY":                {},
	"SSH_AUTH_SOCK":             {},
	"NOTIFY_SOCKET":             {},
	"LISTEN_FDS":                {},
	"LISTEN_PID":                {},
	"LISTEN_FDNAMES":            {},
	"WATCHDOG_PID":              {},
	"WATCHDOG_USEC":             {},
	"INVOCATION_ID":             {},
	"JOURNAL_STREAM":            {},
	"MANAGERPID":                {},
	"SYSTEMD_EXEC_PID":          {},
}

const (
	MaxEnvironmentEntries = 256
	MaxEnvironmentTotal   = 32 << 10
	MaxEnvironmentValue   = 8 << 10
)

func FilterEnvironment(values []string) (map[string]string, error) {
	result := make(map[string]string)
	total := 0
	for _, entry := range values {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			continue
		}
		if _, blocked := blockedEnvironment[key]; blocked {
			continue
		}
		if len(value) > MaxEnvironmentValue {
			return nil, errors.New("environment value exceeds maximum size")
		}
		if old, exists := result[key]; exists {
			total -= len(key) + len(old) + 2
		}
		result[key] = value
		total += len(key) + len(value) + 2
		if len(result) > MaxEnvironmentEntries {
			return nil, errors.New("environment has too many entries")
		}
		if total > MaxEnvironmentTotal {
			return nil, errors.New("environment exceeds maximum total size")
		}
	}
	return result, nil
}

type environmentSnapshot struct {
	generation uint64
	values     map[string]string
}

type EnvironmentStore struct {
	mu        sync.RWMutex
	snapshots map[Frontend]environmentSnapshot
}

func (s *EnvironmentStore) Replace(frontend Frontend, values map[string]string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshots == nil {
		s.snapshots = make(map[Frontend]environmentSnapshot)
	}
	current := s.snapshots[frontend]
	copyValues := make(map[string]string, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	current.generation++
	current.values = copyValues
	s.snapshots[frontend] = current
	return current.generation
}

func (s *EnvironmentStore) Snapshot(frontend Frontend) (uint64, []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	current, exists := s.snapshots[frontend]
	if !exists {
		return 0, nil
	}
	keys := make([]string, 0, len(current.values))
	for key := range current.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+current.values[key])
	}
	return current.generation, values
}
