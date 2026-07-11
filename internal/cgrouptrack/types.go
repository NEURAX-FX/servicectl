package cgrouptrack

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type Mode string

const (
	ModeSystem Mode = "system"
	ModeUser   Mode = "user"
)

type UnitKey struct {
	Mode Mode   `json:"mode"`
	UID  uint32 `json:"uid"`
	Unit string `json:"unit"`
}

func (k UnitKey) Validate() error {
	switch k.Mode {
	case ModeSystem:
		if k.UID != 0 {
			return errors.New("system unit UID must be zero")
		}
	case ModeUser:
		if k.UID == 0 {
			return errors.New("user unit UID must be nonzero")
		}
	default:
		return fmt.Errorf("invalid cgroup mode %q", k.Mode)
	}
	return validateUnitName(k.Unit)
}

func validateUnitName(name string) error {
	if len(name) == 0 || len(name) > 255 || !utf8.ValidString(name) {
		return errors.New("unit name must contain 1 to 255 valid UTF-8 bytes")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return errors.New("unit name contains a forbidden path component")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("unit name contains a control character")
		}
	}
	if !strings.HasSuffix(name, ".service") || strings.Count(name, ".service") != 1 || name == ".service" {
		return errors.New("unit name must have one canonical .service suffix")
	}
	return nil
}

func (k UnitKey) EncodedUnit() (string, error) {
	if err := k.Validate(); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString([]byte(k.Unit)), nil
}

func DecodeUnit(mode Mode, uid uint32, encoded string) (UnitKey, error) {
	if encoded == "" {
		return UnitKey{}, errors.New("encoded unit is empty")
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return UnitKey{}, fmt.Errorf("decode unit: %w", err)
	}
	key := UnitKey{Mode: mode, UID: uid, Unit: string(data)}
	if err := key.Validate(); err != nil {
		return UnitKey{}, err
	}
	canonical, _ := key.EncodedUnit()
	if canonical != encoded {
		return UnitKey{}, errors.New("encoded unit is not canonical")
	}
	return key, nil
}

type InstanceIdentity struct {
	UnitKey
	BootID           string `json:"boot_id"`
	MainPID          int    `json:"main_pid"`
	MainPIDStartTime uint64 `json:"main_pid_starttime"`
	VisionEpoch      string `json:"vision_epoch"`
	Generation       uint64 `json:"generation"`
}

func (i InstanceIdentity) Validate() error {
	if err := i.UnitKey.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(i.BootID) == "" || strings.TrimSpace(i.VisionEpoch) == "" {
		return errors.New("instance boot ID and vision epoch are required")
	}
	if i.MainPID <= 0 || i.MainPIDStartTime == 0 || i.Generation == 0 {
		return errors.New("instance PID, start time, and generation must be positive")
	}
	return nil
}

type TrackingState string

const (
	StatePending            TrackingState = "pending"
	StateTracked            TrackingState = "tracked"
	StatePartial            TrackingState = "partial"
	StateDegraded           TrackingState = "degraded"
	StateStopped            TrackingState = "stopped"
	StateOrphanedPopulated  TrackingState = "orphaned-populated"
	StateUnknownUnit        TrackingState = "unknown-unit"
	StateEventSourceOffline TrackingState = "event-source-offline"
)

type DaemonStatus struct {
	Healthy       bool   `json:"healthy"`
	CgroupRoot    string `json:"cgroup_root"`
	LastReconcile string `json:"last_reconcile,omitempty"`
	Pending       int    `json:"pending"`
	Abnormal      int    `json:"abnormal"`
}

type UnitStatus struct {
	Identity      InstanceIdentity `json:"identity"`
	State         TrackingState    `json:"state"`
	Path          string           `json:"path"`
	MemberCount   int              `json:"member_count"`
	LastMigration string           `json:"last_migration,omitempty"`
	LastError     string           `json:"last_error,omitempty"`
}

type ProcessStatus struct {
	PID       int    `json:"pid"`
	StartTime uint64 `json:"starttime"`
	UID       uint32 `json:"uid"`
	Comm      string `json:"comm"`
	MainPID   bool   `json:"main_pid"`
}

type Scope struct {
	Mode   Mode
	UID    uint32
	Global bool
}
