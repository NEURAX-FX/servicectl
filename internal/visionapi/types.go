package visionapi

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	ModeSystem                 = "system"
	ModeUser                   = "user"
	SystemRuntimeDir           = "/run/servicectl"
	SystemServicectlSockName   = "servicectl.sock"
	SystemServicectlEventsName = "servicectl-events.sock"
	SystemPropertySockName     = "sys-propertyd.sock"
	SystemSysvisionSockName    = "sysvisiond.sock"
	SysvisionDirName           = "sysvision"
	SysvisionIngressSockName   = "events.sock"
	SourceServicectl           = "servicectl"
	SourceSysNotifyd           = "sys-notifyd"
	SourceSysOrchestrd         = "sys-orchestrd"
	SourceSysPropertyd         = "sys-propertyd"
	KindUnitCommand            = "unit.command"
	KindUnitRuntime            = "unit.runtime"
	KindUnitOrchestration      = "unit.orchestration"
	KindUnitQuery              = "unit.query"
	KindPropertyChanged        = "property.changed"
	KindGroupChanged           = "group.changed"
)

type Plane struct {
	Mode       string
	RuntimeDir string
}

func Planes() []Plane {
	return []Plane{SystemPlane(), UserPlane()}
}

func SystemPlane() Plane {
	return Plane{Mode: ModeSystem, RuntimeDir: RuntimeDir(false, "")}
}

func UserPlane() Plane {
	return Plane{Mode: ModeUser, RuntimeDir: RuntimeDir(true, "")}
}

func PlaneForMode(mode string) Plane {
	if strings.EqualFold(strings.TrimSpace(mode), ModeUser) {
		return UserPlane()
	}
	return SystemPlane()
}

func ModeForUser(userMode bool) string {
	if userMode {
		return ModeUser
	}
	return ModeSystem
}

func SystemServicectlSocketPath() string {
	return filepath.Join(SystemRuntimeDir, SystemServicectlSockName)
}

func RuntimeDir(userMode bool, xdgRuntimeDir string) string {
	if userMode {
		if override := strings.TrimSpace(os.Getenv("SERVICECTL_USER_RUNTIME_ROOT")); override != "" {
			return override
		}
	}
	if !userMode {
		if override := strings.TrimSpace(os.Getenv("SERVICECTL_SYSTEM_RUNTIME_ROOT")); override != "" {
			return override
		}
	}
	if userMode {
		return filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "servicectl")
	}
	return SystemRuntimeDir
}

func ServicectlSocketPath(userMode bool, xdgRuntimeDir string) string {
	return filepath.Join(RuntimeDir(userMode, xdgRuntimeDir), SystemServicectlSockName)
}

func ServicectlSocketPathForMode(mode string) string {
	return filepath.Join(PlaneForMode(mode).RuntimeDir, SystemServicectlSockName)
}

func SystemServicectlEventsSocketPath() string {
	return filepath.Join(SystemRuntimeDir, SystemServicectlEventsName)
}

func ServicectlEventsSocketPath(userMode bool, xdgRuntimeDir string) string {
	return filepath.Join(RuntimeDir(userMode, xdgRuntimeDir), SystemServicectlEventsName)
}

func ServicectlEventsSocketPathForMode(mode string) string {
	return filepath.Join(PlaneForMode(mode).RuntimeDir, SystemServicectlEventsName)
}

func PropertySocketPath(userMode bool, xdgRuntimeDir string) string {
	return filepath.Join(RuntimeDir(false, xdgRuntimeDir), SystemPropertySockName)
}

func PropertySocketPathForMode(mode string) string {
	return filepath.Join(RuntimeDir(false, ""), SystemPropertySockName)
}

func SystemPropertySocketPath() string {
	return filepath.Join(RuntimeDir(false, ""), SystemPropertySockName)
}

func SystemSysvisionDir() string {
	return filepath.Join(SystemRuntimeDir, SysvisionDirName)
}

func SysvisionDir(userMode bool, xdgRuntimeDir string) string {
	return filepath.Join(RuntimeDir(userMode, xdgRuntimeDir), SysvisionDirName)
}

func SysvisionDirForMode(mode string) string {
	return filepath.Join(PlaneForMode(mode).RuntimeDir, SysvisionDirName)
}

func SystemSysvisionSocketPath() string {
	return filepath.Join(SystemSysvisionDir(), SystemSysvisionSockName)
}

func SysvisionSocketPath(userMode bool, xdgRuntimeDir string) string {
	return filepath.Join(SysvisionDir(userMode, xdgRuntimeDir), SystemSysvisionSockName)
}

func SysvisionSocketPathForMode(mode string) string {
	return filepath.Join(SysvisionDirForMode(mode), SystemSysvisionSockName)
}

func SystemSysvisionIngressSocketPath() string {
	return filepath.Join(SystemSysvisionDir(), SysvisionIngressSockName)
}

func SysvisionIngressSocketPath(userMode bool, xdgRuntimeDir string) string {
	return filepath.Join(SysvisionDir(userMode, xdgRuntimeDir), SysvisionIngressSockName)
}

func SysvisionIngressSocketPathForMode(mode string) string {
	return filepath.Join(SysvisionDirForMode(mode), SysvisionIngressSockName)
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

type PropertyState struct {
	Key        string `json:"key"`
	Value      string `json:"value"`
	Persistent bool   `json:"persistent"`
}

type GroupState struct {
	Name       string   `json:"name"`
	Units      []string `json:"units"`
	Enabled    bool     `json:"enabled"`
	Persistent bool     `json:"persistent"`
	Targets    []string `json:"targets,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
}

type PropertiesResponse struct {
	GeneratedAt string          `json:"generated_at"`
	Properties  []PropertyState `json:"properties"`
}

type GroupsResponse struct {
	GeneratedAt string       `json:"generated_at"`
	Groups      []GroupState `json:"groups"`
}

type UnitGroupsResponse struct {
	GeneratedAt string       `json:"generated_at"`
	Unit        string       `json:"unit"`
	Groups      []GroupState `json:"groups"`
}

type UnitsResponse struct {
	GeneratedAt string         `json:"generated_at"`
	Units       []UnitSnapshot `json:"units"`
}

type EventEnvelope struct {
	Source    string            `json:"source"`
	Kind      string            `json:"kind"`
	Mode      string            `json:"mode"`
	Unit      string            `json:"unit"`
	Target    string            `json:"target,omitempty"`
	Timestamp string            `json:"timestamp"`
	Payload   map[string]string `json:"payload,omitempty"`
}

type WatchFilter struct {
	Source string
	Kind   string
	Mode   string
	Unit   string
	Group  string
	Key    string
}

func (f WatchFilter) Matches(event EventEnvelope) bool {
	if f.Source != "" && f.Source != event.Source {
		return false
	}
	if f.Kind != "" && f.Kind != event.Kind {
		return false
	}
	if f.Mode != "" && !strings.EqualFold(strings.TrimSpace(f.Mode), strings.TrimSpace(event.Mode)) {
		return false
	}
	if f.Group != "" && strings.TrimSpace(f.Group) != strings.TrimSpace(event.Payload["group"]) {
		return false
	}
	if f.Key != "" && strings.TrimSpace(f.Key) != strings.TrimSpace(event.Payload["key"]) {
		return false
	}
	if f.Unit == "" {
		return true
	}
	return strings.TrimSuffix(f.Unit, ".service") == strings.TrimSuffix(event.Unit, ".service")
}

func NewEvent(mode string, source string, kind string, unit string, payload map[string]string) EventEnvelope {
	cleanUnit := strings.TrimSuffix(strings.TrimSpace(unit), ".service")
	if cleanUnit != "" {
		cleanUnit += ".service"
	}
	return EventEnvelope{
		Source:    source,
		Kind:      kind,
		Mode:      PlaneForMode(mode).Mode,
		Unit:      cleanUnit,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Payload:   payload,
	}
}
