package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"servicectl/internal/cgrouptrack"
	"servicectl/internal/dbusmanager"
	"servicectl/internal/statusview"
	"servicectl/internal/visionapi"
)

const statusSnapshotFreshness = 30 * time.Second

var errStatusDBusNoOwner = errors.New("D-Bus name has no owner")

type statusRuntimeObservation struct {
	State   string
	PID     int
	Healthy bool
}

type statusProbeInput struct {
	Mode     string
	UID      uint32
	Unit     string
	Snapshot visionapi.UnitSnapshot
}

type statusProbeResult struct {
	State      string
	PID        int
	ManagerPID int
	BusOwner   string
	CgroupPath string
	Evidence   []statusview.Evidence
}

type statusProbeDependencies struct {
	now         func() time.Time
	s6Status    func(context.Context, string) (statusRuntimeObservation, error)
	dinitStatus func(context.Context, string, string) (statusRuntimeObservation, error)
	busMeta     func(context.Context, string) (sysvisionMetaResponse, error)
	dbusOwner   func(context.Context, string, string) (string, error)
	cgroupUnit  func(context.Context, string, uint32, string) (cgrouptrack.UnitStatus, error)
}

func defaultStatusProbeDependencies() statusProbeDependencies {
	return statusProbeDependencies{
		now:         time.Now,
		s6Status:    queryStatusS6Service,
		dinitStatus: queryStatusDinitService,
		busMeta:     queryBusMetaViaSysvisionContext,
		dbusOwner:   queryStatusDBusOwner,
		cgroupUnit: func(ctx context.Context, mode string, uid uint32, unit string) (cgrouptrack.UnitStatus, error) {
			return queryStatusCgroupUnit(ctx, cgroupdSocketPath, mode, uid, unit, doCgroupRequest)
		},
	}
}

func probeStatusParticipants(ctx context.Context, manifest visionapi.StatusParticipationManifest, input statusProbeInput, deps statusProbeDependencies) map[string]statusProbeResult {
	results := make(map[string]statusProbeResult, len(manifest.Components))
	type componentResult struct {
		key    string
		result statusProbeResult
	}
	completed := make(chan componentResult, len(manifest.Components))
	for _, component := range manifest.Components {
		component := component
		go func() {
			completed <- componentResult{key: component.Key, result: probeStatusComponent(ctx, component, input, deps)}
		}()
	}
	for len(results) < len(manifest.Components) {
		select {
		case result := <-completed:
			results[result.key] = result.result
		case <-ctx.Done():
			checkedAt := time.Now().UTC()
			if deps.now != nil {
				checkedAt = deps.now().UTC()
			}
			for _, component := range manifest.Components {
				if _, ok := results[component.Key]; !ok {
					results[component.Key] = statusProbeResultFromError(checkedAt, statusview.EvidenceComponentStatus, ctx.Err())
				}
			}
			return results
		}
	}
	return results
}

func probeStatusComponent(ctx context.Context, component visionapi.StatusManifestComponent, input statusProbeInput, deps statusProbeDependencies) statusProbeResult {
	if deps.now == nil {
		deps.now = time.Now
	}
	checkedAt := deps.now().UTC()
	switch component.Type {
	case "service":
		return probeStatusService(input.Snapshot, checkedAt)
	case "sys-notifyd":
		return probeStatusHelper(input.Snapshot, checkedAt)
	case "dinit":
		if deps.dinitStatus == nil {
			return statusProbeResultFromError(checkedAt, statusview.EvidenceComponentStatus, errors.New("runtime probe is unavailable"))
		}
		observation, err := deps.dinitStatus(ctx, input.Mode, component.ServiceName)
		return probeStatusRuntimeObservation(observation, err, statusview.EvidenceComponentStatus, checkedAt)
	case "s6":
		result := probeStatusRuntime(ctx, component.ServiceName, statusview.EvidenceComponentStatus, checkedAt, deps.s6Status)
		if len(result.Evidence) == 1 && result.Evidence[0].Result == statusview.EvidenceHealthy {
			result.State = "supervising"
			result.PID = 0
		}
		return result
	case "sys-orchestrd", "sysvisiond", "servicectl-api":
		return probeStatusRuntime(ctx, component.ServiceName, statusview.EvidenceComponentStatus, checkedAt, deps.s6Status)
	case "sysvbus":
		return probeStatusSysvbus(ctx, input.Mode, checkedAt, deps.busMeta)
	case "dbus":
		return probeStatusDBus(ctx, input.Mode, component.BusName, checkedAt, deps.dbusOwner)
	case "sys-cgroupd":
		return probeStatusCgroup(ctx, input, checkedAt, deps.cgroupUnit)
	default:
		return statusProbeResultFromError(checkedAt, statusview.EvidenceComponentStatus, fmt.Errorf("unsupported component type %q", component.Type))
	}
}

func probeStatusService(snapshot visionapi.UnitSnapshot, checkedAt time.Time) statusProbeResult {
	state := snapshotRuntimeState(snapshot.State)
	result := statusview.EvidenceHealthy
	if state == "failed" {
		result = statusview.EvidenceUnhealthy
	}
	observedAt := parseStatusObservationTime(snapshot.UpdatedAt)
	if observedAt != nil && checkedAt.Sub(*observedAt) > statusSnapshotFreshness {
		result = statusview.EvidenceStale
	}
	return statusProbeResult{
		State:      state,
		PID:        parseStatusPID(snapshot.MainPID),
		ManagerPID: parseStatusPID(snapshot.ManagerPID),
		Evidence: []statusview.Evidence{{
			Source:           statusview.EvidenceSysvisionSnapshot,
			Result:           result,
			Authoritative:    true,
			CheckedAt:        checkedAt,
			SourceObservedAt: observedAt,
		}},
	}
}

func probeStatusHelper(snapshot visionapi.UnitSnapshot, checkedAt time.Time) statusProbeResult {
	state := strings.TrimSpace(snapshot.ChildState)
	if state == "" {
		state = strings.TrimSpace(snapshot.Phase)
	}
	result := statusview.EvidenceHealthy
	if strings.TrimSpace(snapshot.Failure) != "" || state == "failed" || state == "stopped" {
		result = statusview.EvidenceUnhealthy
	} else if state == "" {
		state = "unknown"
		result = statusview.EvidenceError
	}
	return statusProbeResult{
		State:      state,
		PID:        parseStatusPID(snapshot.ManagerPID),
		ManagerPID: parseStatusPID(snapshot.ManagerPID),
		BusOwner:   strings.TrimSpace(snapshot.BusOwner),
		Evidence: []statusview.Evidence{{
			Source:           statusview.EvidenceSysvisionSnapshot,
			Result:           result,
			Authoritative:    true,
			CheckedAt:        checkedAt,
			SourceObservedAt: parseStatusObservationTime(snapshot.UpdatedAt),
			Detail:           strings.TrimSpace(snapshot.Failure),
		}},
	}
}

func probeStatusRuntime(ctx context.Context, serviceName string, source statusview.EvidenceSource, checkedAt time.Time, query func(context.Context, string) (statusRuntimeObservation, error)) statusProbeResult {
	if query == nil {
		return statusProbeResultFromError(checkedAt, source, errors.New("runtime probe is unavailable"))
	}
	observation, err := query(ctx, serviceName)
	return probeStatusRuntimeObservation(observation, err, source, checkedAt)
}

func probeStatusRuntimeObservation(observation statusRuntimeObservation, err error, source statusview.EvidenceSource, checkedAt time.Time) statusProbeResult {
	if err != nil {
		return statusProbeResultFromError(checkedAt, source, err)
	}
	result := statusview.EvidenceHealthy
	if !observation.Healthy {
		result = statusview.EvidenceUnhealthy
	}
	return statusProbeResult{State: observation.State, PID: observation.PID, Evidence: []statusview.Evidence{{
		Source: source, Result: result, CheckedAt: checkedAt,
	}}}
}

func probeStatusSysvbus(ctx context.Context, mode string, checkedAt time.Time, query func(context.Context, string) (sysvisionMetaResponse, error)) statusProbeResult {
	if query == nil {
		return statusProbeResultFromError(checkedAt, statusview.EvidenceComponentStatus, errors.New("sysvbus probe is unavailable"))
	}
	meta, err := query(ctx, mode)
	if err != nil {
		return statusProbeResultFromError(checkedAt, statusview.EvidenceComponentStatus, err)
	}
	state := "disconnected"
	result := statusview.EvidenceUnhealthy
	if meta.ServicectlEventsConnected {
		state = "connected"
		result = statusview.EvidenceHealthy
	}
	return statusProbeResult{State: state, Evidence: []statusview.Evidence{{
		Source: statusview.EvidenceComponentStatus, Result: result, CheckedAt: checkedAt, Detail: strings.TrimSpace(meta.ServicectlEventsError),
	}}}
}

func probeStatusDBus(ctx context.Context, mode, busName string, checkedAt time.Time, query func(context.Context, string, string) (string, error)) statusProbeResult {
	if query == nil {
		return statusProbeResultFromError(checkedAt, statusview.EvidenceComponentStatus, errors.New("D-Bus probe is unavailable"))
	}
	owner, err := query(ctx, mode, busName)
	if err != nil {
		return statusProbeResultFromError(checkedAt, statusview.EvidenceComponentStatus, err)
	}
	return statusProbeResult{State: "owned", BusOwner: owner, Evidence: []statusview.Evidence{{
		Source: statusview.EvidenceComponentStatus, Result: statusview.EvidenceHealthy, CheckedAt: checkedAt,
	}}}
}

func probeStatusCgroup(ctx context.Context, input statusProbeInput, checkedAt time.Time, query func(context.Context, string, uint32, string) (cgrouptrack.UnitStatus, error)) statusProbeResult {
	if query == nil {
		return statusProbeResultFromError(checkedAt, statusview.EvidenceCgroupProbe, errors.New("cgroup probe is unavailable"))
	}
	unit, err := query(ctx, input.Mode, input.UID, input.Unit)
	if err != nil {
		return statusProbeResultFromError(checkedAt, statusview.EvidenceCgroupProbe, err)
	}
	result := statusview.EvidenceHealthy
	if unit.State != cgrouptrack.StateTracked {
		result = statusview.EvidenceUnhealthy
	}
	return statusProbeResult{State: string(unit.State), CgroupPath: unit.Path, Evidence: []statusview.Evidence{{
		Source: statusview.EvidenceCgroupProbe, Result: result, CheckedAt: checkedAt, Detail: strings.TrimSpace(unit.LastError),
	}}}
}

func statusProbeResultFromError(checkedAt time.Time, source statusview.EvidenceSource, err error) statusProbeResult {
	result := statusview.EvidenceError
	state := "unknown"
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		result = statusview.EvidenceTimeout
	case errors.Is(err, os.ErrNotExist), errors.Is(err, errStatusDBusNoOwner), isStatusNotFoundError(err):
		result = statusview.EvidenceNotFound
		state = "missing"
	}
	return statusProbeResult{State: state, Evidence: []statusview.Evidence{{
		Source: source, Result: result, CheckedAt: checkedAt, Detail: err.Error(),
	}}}
}

func queryStatusS6Service(ctx context.Context, serviceName string) (statusRuntimeObservation, error) {
	if err := ctx.Err(); err != nil {
		return statusRuntimeObservation{}, err
	}
	if strings.TrimSpace(serviceName) == "" {
		return statusRuntimeObservation{}, os.ErrNotExist
	}
	serviceDir := filepath.Join(filepath.Dir(s6LiveDir()), "service", serviceName)
	output, err := exec.CommandContext(ctx, "/bin/s6-svstat", "-o", "up,pid", serviceDir).CombinedOutput()
	text := strings.TrimSpace(string(output))
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = 1
	}
	if err != nil || code != 0 {
		if _, statErr := os.Stat(serviceDir); errors.Is(statErr, os.ErrNotExist) {
			return statusRuntimeObservation{}, os.ErrNotExist
		}
		if err != nil {
			return statusRuntimeObservation{}, err
		}
		return statusRuntimeObservation{}, fmt.Errorf("s6-svstat exited %d: %s", code, strings.TrimSpace(text))
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return statusRuntimeObservation{}, fmt.Errorf("s6-svstat returned no state")
	}
	up := strings.EqualFold(fields[0], "true")
	pid := 0
	if len(fields) > 1 {
		pid, _ = strconv.Atoi(fields[1])
	}
	state := "down"
	if up {
		state = "active"
	}
	return statusRuntimeObservation{State: state, PID: pid, Healthy: up}, nil
}

func queryStatusDinitService(ctx context.Context, mode, serviceName string) (statusRuntimeObservation, error) {
	return queryStatusDinitServiceWithCommand(ctx, mode, serviceName, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	})
}

func queryStatusDinitServiceWithCommand(ctx context.Context, mode, serviceName string, command func(context.Context, string, ...string) ([]byte, error)) (statusRuntimeObservation, error) {
	if strings.TrimSpace(serviceName) == "" {
		return statusRuntimeObservation{}, os.ErrNotExist
	}
	args := []string{"status", serviceName}
	if strings.EqualFold(strings.TrimSpace(mode), visionapi.ModeUser) {
		args = append([]string{"--user"}, args...)
	}
	output, err := command(ctx, "dinitctl", args...)
	if err != nil && len(output) == 0 {
		return statusRuntimeObservation{}, err
	}
	status := parseDinitStatus(string(output))
	state := statusValue(status, "State")
	if strings.EqualFold(state, "NOT LOADED") || state == "" {
		return statusRuntimeObservation{}, os.ErrNotExist
	}
	healthy := state == "STARTED"
	return statusRuntimeObservation{State: snapshotRuntimeState(state), PID: parseStatusPID(statusValue(status, "Process ID")), Healthy: healthy}, nil
}

func queryStatusDBusOwner(ctx context.Context, mode, busName string) (string, error) {
	var bus *dbusmanager.Godbus
	var err error
	if strings.EqualFold(strings.TrimSpace(mode), visionapi.ModeUser) {
		bus, err = dbusmanager.NewSessionBus()
	} else {
		bus, err = dbusmanager.NewSystemBus()
	}
	if err != nil {
		return "", err
	}
	defer bus.Close()
	owner, err := bus.GetNameOwner(ctx, busName)
	if errors.Is(err, dbusmanager.ErrNoOwner) {
		return "", errStatusDBusNoOwner
	}
	return owner, err
}

func snapshotRuntimeState(state string) string {
	state = strings.TrimSpace(state)
	switch {
	case state == "STARTED":
		return "active"
	case strings.HasPrefix(state, "STOPPED (terminated; exited - status "):
		return "failed"
	case strings.HasPrefix(state, "STOPPED"), strings.EqualFold(state, "NOT LOADED"):
		return "inactive"
	case state == "":
		return "unknown"
	default:
		return strings.ToLower(state)
	}
}

func parseStatusPID(raw string) int {
	pid, _ := strconv.Atoi(strings.TrimSpace(raw))
	if pid < 1 {
		return 0
	}
	return pid
}

func parseStatusObservationTime(raw string) *time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func isStatusNotFoundError(err error) bool {
	var apiErr *cgrouptrack.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(apiErr.Code))
	return strings.Contains(code, "not_found") || strings.Contains(code, "unknown_unit")
}
