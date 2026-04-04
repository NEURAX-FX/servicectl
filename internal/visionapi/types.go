package visionapi

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	SystemRuntimeDir           = "/run/servicectl"
	SystemServicectlSockName   = "servicectl.sock"
	SystemServicectlEventsName = "servicectl-events.sock"
	SystemSysvisionSockName    = "sysvisiond.sock"
	SysvisionDirName           = "sysvision"
	SysvisionIngressSockName   = "events.sock"
	SourceServicectl           = "servicectl"
	SourceSysNotifyd           = "sys-notifyd"
	SourceSysOrchestrd         = "sys-orchestrd"
	KindUnitCommand            = "unit.command"
	KindUnitRuntime            = "unit.runtime"
	KindUnitOrchestration      = "unit.orchestration"
	KindUnitQuery              = "unit.query"
)

func SystemServicectlSocketPath() string {
	return filepath.Join(SystemRuntimeDir, SystemServicectlSockName)
}

func RuntimeDir(userMode bool, xdgRuntimeDir string) string {
	if userMode {
		return filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "servicectl")
	}
	return SystemRuntimeDir
}

func ServicectlSocketPath(userMode bool, xdgRuntimeDir string) string {
	return filepath.Join(RuntimeDir(userMode, xdgRuntimeDir), SystemServicectlSockName)
}

func SystemServicectlEventsSocketPath() string {
	return filepath.Join(SystemRuntimeDir, SystemServicectlEventsName)
}

func ServicectlEventsSocketPath(userMode bool, xdgRuntimeDir string) string {
	return filepath.Join(RuntimeDir(userMode, xdgRuntimeDir), SystemServicectlEventsName)
}

func SystemSysvisionDir() string {
	return filepath.Join(SystemRuntimeDir, SysvisionDirName)
}

func SysvisionDir(userMode bool, xdgRuntimeDir string) string {
	return filepath.Join(RuntimeDir(userMode, xdgRuntimeDir), SysvisionDirName)
}

func SystemSysvisionSocketPath() string {
	return filepath.Join(SystemSysvisionDir(), SystemSysvisionSockName)
}

func SysvisionSocketPath(userMode bool, xdgRuntimeDir string) string {
	return filepath.Join(SysvisionDir(userMode, xdgRuntimeDir), SystemSysvisionSockName)
}

func SystemSysvisionIngressSocketPath() string {
	return filepath.Join(SystemSysvisionDir(), SysvisionIngressSockName)
}

func SysvisionIngressSocketPath(userMode bool, xdgRuntimeDir string) string {
	return filepath.Join(SysvisionDir(userMode, xdgRuntimeDir), SysvisionIngressSockName)
}

func SystemNotifydIngressSocketPath() string {
	return SystemSysvisionIngressSocketPath()
}

type UnitSnapshot struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Mode         string `json:"mode"`
	SourcePath   string `json:"source_path"`
	ManagedBy    string `json:"managed_by"`
	DinitName    string `json:"dinit_name"`
	LoggerName   string `json:"logger_name"`
	State        string `json:"state"`
	Activation   string `json:"activation"`
	ProcessID    string `json:"process_id"`
	ManagerPID   string `json:"manager_pid"`
	MainPID      string `json:"main_pid"`
	Phase        string `json:"phase"`
	ChildState   string `json:"child_state"`
	Status       string `json:"status"`
	Failure      string `json:"failure"`
	NotifySocket string `json:"notify_socket"`
	StateFile    string `json:"state_file"`
	UpdatedAt    string `json:"updated_at"`
}

type UnitsResponse struct {
	GeneratedAt string         `json:"generated_at"`
	Units       []UnitSnapshot `json:"units"`
}

type EventEnvelope struct {
	Source    string            `json:"source"`
	Kind      string            `json:"kind"`
	Unit      string            `json:"unit"`
	Timestamp string            `json:"timestamp"`
	Payload   map[string]string `json:"payload,omitempty"`
}

type WatchFilter struct {
	Source string
	Kind   string
	Unit   string
}

func (f WatchFilter) Matches(event EventEnvelope) bool {
	if f.Source != "" && f.Source != event.Source {
		return false
	}
	if f.Kind != "" && f.Kind != event.Kind {
		return false
	}
	if f.Unit == "" {
		return true
	}
	return strings.TrimSuffix(f.Unit, ".service") == strings.TrimSuffix(event.Unit, ".service")
}

func NewEvent(source string, kind string, unit string, payload map[string]string) EventEnvelope {
	cleanUnit := strings.TrimSuffix(strings.TrimSpace(unit), ".service")
	if cleanUnit != "" {
		cleanUnit += ".service"
	}
	return EventEnvelope{
		Source:    source,
		Kind:      kind,
		Unit:      cleanUnit,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Payload:   payload,
	}
}
