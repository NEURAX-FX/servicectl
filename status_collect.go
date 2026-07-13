package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"servicectl/internal/statusview"
	"servicectl/internal/visionapi"
)

var errStatusUnitNotFound = errors.New("status unit not found")

type statusCollectionErrorKind string

const (
	statusCollectionNotFound  statusCollectionErrorKind = "not_found"
	statusCollectionMandatory statusCollectionErrorKind = "mandatory"
)

type statusCollectionError struct {
	Kind statusCollectionErrorKind
	Unit string
	Err  error
}

func (e *statusCollectionError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("collect status for %s: %v", e.Unit, e.Err)
}

func (e *statusCollectionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type statusCollectionDependencies struct {
	resolveUnit           func(string) (*Unit, *SocketUnit, error)
	querySnapshot         func(context.Context, string, string) (visionapi.UnitSnapshot, error)
	queryManifest         func(context.Context, string, string) (visionapi.StatusParticipationManifest, error)
	buildFallbackManifest func(*Unit, *SocketUnit, string, uint32, string, time.Time) (visionapi.StatusParticipationManifest, error)
	enabled               func(string) bool
	resolveOrchestrator   func(string, string) (string, error)
	probeParticipants     func(context.Context, visionapi.StatusParticipationManifest, statusProbeInput) map[string]statusProbeResult
	collectLogs           func(context.Context, string, string, time.Time) ([]statusview.LogEntry, []statusview.Diagnostic)
	processStartedAt      func(context.Context, int) (time.Time, error)
	now                   func() time.Time
}

func defaultStatusCollectionDependencies() statusCollectionDependencies {
	probeDeps := defaultStatusProbeDependencies()
	logDeps := defaultStatusLogDependencies()
	return statusCollectionDependencies{
		resolveUnit: func(name string) (*Unit, *SocketUnit, error) {
			unit, err := parseSystemdUnit(name)
			if err != nil {
				if isStatusUnitNotFoundError(err) {
					return nil, nil, fmt.Errorf("%w: %v", errStatusUnitNotFound, err)
				}
				return nil, nil, err
			}
			socketUnit, socketErr := parseOptionalSocketUnit(unit.Name)
			if socketErr != nil {
				socketUnit = nil
			}
			return unit, socketUnit, nil
		},
		querySnapshot: func(ctx context.Context, mode, unit string) (visionapi.UnitSnapshot, error) {
			return queryStatusSnapshot(ctx, mode, unit, queryUnitSnapshotViaSysvisionMode, func(mode, unit string) (visionapi.UnitSnapshot, error) {
				return buildUnitSnapshot(buildConfig(mode == visionapi.ModeUser), strings.TrimSuffix(unit, ".service"))
			})
		},
		queryManifest: func(ctx context.Context, mode, unit string) (visionapi.StatusParticipationManifest, error) {
			queryCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			defer cancel()
			return queryStatusManifest(queryCtx, mode, unit)
		},
		buildFallbackManifest: func(unit *Unit, socketUnit *SocketUnit, mode string, uid uint32, orchestrator string, generatedAt time.Time) (visionapi.StatusParticipationManifest, error) {
			return buildStatusParticipationManifest(statusManifestInput{
				Unit:                unit,
				SocketUnit:          socketUnit,
				Mode:                mode,
				UID:                 uid,
				Enabled:             strings.TrimSpace(orchestrator) != "",
				OrchestratorService: orchestrator,
				GeneratedAt:         generatedAt,
			})
		},
		enabled:             isEffectivelyEnabled,
		resolveOrchestrator: resolveStatusOrchestrator,
		probeParticipants: func(ctx context.Context, manifest visionapi.StatusParticipationManifest, input statusProbeInput) map[string]statusProbeResult {
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			return probeStatusParticipants(probeCtx, manifest, input, probeDeps)
		},
		collectLogs: func(ctx context.Context, unit, mode string, observedAt time.Time) ([]statusview.LogEntry, []statusview.Diagnostic) {
			return collectStatusLogs(ctx, unit, mode, observedAt, logDeps)
		},
		processStartedAt: queryStatusProcessStartedAt,
		now:              time.Now,
	}
}

func queryStatusSnapshot(ctx context.Context, mode, unit string, preferred func(context.Context, string, string) (visionapi.UnitSnapshot, error), fallback func(string, string) (visionapi.UnitSnapshot, error)) (visionapi.UnitSnapshot, error) {
	if preferred != nil {
		queryCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		snapshot, err := preferred(queryCtx, mode, unit)
		cancel()
		if err == nil {
			return snapshot, nil
		}
		if fallback == nil {
			return visionapi.UnitSnapshot{}, err
		}
		fallbackSnapshot, fallbackErr := fallback(mode, unit)
		if fallbackErr == nil {
			return fallbackSnapshot, nil
		}
		return visionapi.UnitSnapshot{}, fmt.Errorf("preferred snapshot: %v; direct fallback: %w", err, fallbackErr)
	}
	if fallback == nil {
		return visionapi.UnitSnapshot{}, errors.New("status snapshot collectors are unavailable")
	}
	return fallback(mode, unit)
}

func isStatusUnitNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.HasPrefix(message, "unit ") && strings.HasSuffix(message, " not found")
}

func collectStatusModel(ctx context.Context, rawUnit, mode string, uid uint32, deps statusCollectionDependencies) (statusview.Model, error) {
	unitName := strings.TrimSuffix(strings.TrimSpace(rawUnit), ".service")
	canonicalUnit := unitName + ".service"
	if unitName == "" {
		return statusview.Model{}, &statusCollectionError{Kind: statusCollectionNotFound, Unit: canonicalUnit, Err: errStatusUnitNotFound}
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	startedAt := deps.now().UTC()
	if deps.resolveUnit == nil || deps.querySnapshot == nil {
		return statusview.Model{}, &statusCollectionError{Kind: statusCollectionMandatory, Unit: canonicalUnit, Err: errors.New("mandatory status collectors are unavailable")}
	}

	unit, socketUnit, err := deps.resolveUnit(unitName)
	if err != nil {
		kind := statusCollectionMandatory
		if errors.Is(err, errStatusUnitNotFound) {
			kind = statusCollectionNotFound
		}
		return statusview.Model{}, &statusCollectionError{Kind: kind, Unit: canonicalUnit, Err: err}
	}
	canonicalUnit, err = statusManifestUnitName(unit.Name)
	if err != nil {
		return statusview.Model{}, &statusCollectionError{Kind: statusCollectionMandatory, Unit: canonicalUnit, Err: err}
	}
	snapshot, err := deps.querySnapshot(ctx, mode, canonicalUnit)
	if err != nil {
		return statusview.Model{}, &statusCollectionError{Kind: statusCollectionMandatory, Unit: canonicalUnit, Err: err}
	}
	if err := validateStatusSnapshotIdentity(snapshot, canonicalUnit, mode, uid); err != nil {
		return statusview.Model{}, &statusCollectionError{Kind: statusCollectionMandatory, Unit: canonicalUnit, Err: err}
	}

	enabled := false
	if deps.enabled != nil {
		enabled = deps.enabled(unitName)
	}
	manifest, manifestErr := queryCompleteStatusManifest(ctx, canonicalUnit, mode, uid, startedAt, unit, socketUnit, deps)
	if manifestErr != nil {
		return statusview.Model{}, &statusCollectionError{Kind: statusCollectionMandatory, Unit: canonicalUnit, Err: manifestErr}
	}

	probeInput := statusProbeInput{Mode: mode, UID: uid, Unit: canonicalUnit, Snapshot: snapshot}
	results := map[string]statusProbeResult{}
	if deps.probeParticipants != nil {
		results = deps.probeParticipants(ctx, manifest, probeInput)
	}
	observedAt := deps.now().UTC()
	model := materializeStatusModel(unit, snapshot, manifest, results, enabled, observedAt)
	model.ObservedAt = observedAt
	if model.Summary.MainPID > 0 && deps.processStartedAt != nil {
		if processStartedAt, processErr := deps.processStartedAt(ctx, model.Summary.MainPID); processErr == nil && !processStartedAt.IsZero() {
			processStartedAt = processStartedAt.UTC()
			model.Summary.StartedAt = &processStartedAt
			if model.ObservedAt.After(processStartedAt) {
				model.Summary.ActiveDurationSeconds = int64(model.ObservedAt.Sub(processStartedAt) / time.Second)
			}
			for i := range model.Orchestration.Nodes {
				if model.Orchestration.Nodes[i].Type == "service" {
					model.Orchestration.Nodes[i].ProcessStartedAt = &processStartedAt
					model.Orchestration.Nodes[i].ActiveDurationSeconds = model.Summary.ActiveDurationSeconds
				}
			}
		}
	}
	if deps.collectLogs != nil {
		logCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		logs, diagnostics := deps.collectLogs(logCtx, canonicalUnit, mode, model.ObservedAt)
		cancel()
		model.Logs = normalizeStatusLogs(logs)
		model.Diagnostics = append(model.Diagnostics, diagnostics...)
	}

	finalized, err := statusview.Finalize(model)
	if err != nil {
		return statusview.Model{}, &statusCollectionError{Kind: statusCollectionMandatory, Unit: canonicalUnit, Err: err}
	}
	return finalized, nil
}

func queryCompleteStatusManifest(ctx context.Context, unit, mode string, uid uint32, generatedAt time.Time, parsedUnit *Unit, socketUnit *SocketUnit, deps statusCollectionDependencies) (visionapi.StatusParticipationManifest, error) {
	if deps.queryManifest != nil {
		manifest, err := deps.queryManifest(ctx, mode, unit)
		if err == nil {
			if validateErr := validateStatusManifestIdentity(manifest, unit, mode, uid); validateErr == nil {
				return manifest, nil
			}
		}
	}
	if deps.buildFallbackManifest == nil {
		return visionapi.StatusParticipationManifest{}, errors.New("no complete status participation manifest is available")
	}
	orchestrator := ""
	if deps.resolveOrchestrator != nil {
		var err error
		orchestrator, err = deps.resolveOrchestrator(mode, unit)
		if err != nil {
			return visionapi.StatusParticipationManifest{}, fmt.Errorf("resolve fallback orchestration owner: %w", err)
		}
	}
	manifest, err := deps.buildFallbackManifest(parsedUnit, socketUnit, mode, uid, orchestrator, generatedAt)
	if err != nil {
		return visionapi.StatusParticipationManifest{}, fmt.Errorf("build fallback status manifest: %w", err)
	}
	if err := validateStatusManifestIdentity(manifest, unit, mode, uid); err != nil {
		return visionapi.StatusParticipationManifest{}, fmt.Errorf("validate fallback status manifest: %w", err)
	}
	return manifest, nil
}

func validateStatusManifestIdentity(manifest visionapi.StatusParticipationManifest, unit, mode string, uid uint32) error {
	if err := visionapi.ValidateStatusParticipationManifest(manifest); err != nil {
		return err
	}
	wantMode := visionapi.PlaneForMode(mode).Mode
	wantUID := uint32(0)
	if wantMode == visionapi.ModeUser {
		wantUID = uid
	}
	if manifest.Unit != unit || manifest.Mode != wantMode || manifest.UID != wantUID {
		return fmt.Errorf("status manifest identity %s %s/%d does not match request %s %s/%d", manifest.Unit, manifest.Mode, manifest.UID, unit, wantMode, wantUID)
	}
	return nil
}

func validateStatusSnapshotIdentity(snapshot visionapi.UnitSnapshot, unit, mode string, uid uint32) error {
	name, err := statusManifestUnitName(snapshot.Name)
	if err != nil || name != unit {
		return fmt.Errorf("status snapshot unit %q does not match requested unit %q", snapshot.Name, unit)
	}
	wantMode := visionapi.PlaneForMode(mode).Mode
	if snapshot.Mode != "" && visionapi.PlaneForMode(snapshot.Mode).Mode != wantMode {
		return fmt.Errorf("status snapshot mode %q does not match requested mode %q", snapshot.Mode, wantMode)
	}
	if wantMode == visionapi.ModeUser && snapshot.UID != uid {
		return fmt.Errorf("status snapshot UID %d does not match requested UID %d", snapshot.UID, uid)
	}
	if wantMode == visionapi.ModeSystem && snapshot.UID != 0 {
		return fmt.Errorf("system status snapshot has unexpected UID %d", snapshot.UID)
	}
	return nil
}

func resolveStatusOrchestrator(mode, unit string) (string, error) {
	return resolveStatusOrchestratorWithQueries(mode, unit, propertyUnitListsForMode, propertyUnitGroupsForMode)
}

func resolveStatusOrchestratorWithQueries(mode, unit string, queryUnitLists func(string) (visionapi.UnitListsResponse, error), queryUnitGroups func(string, string) (visionapi.UnitGroupsResponse, error)) (string, error) {
	if queryUnitLists == nil {
		return "", errors.New("unit list query is unavailable")
	}
	lists, err := queryUnitLists(mode)
	if err != nil {
		return "", fmt.Errorf("query unit lists: %w", err)
	}
	return statusOrchestratorFromLists(mode, unit, lists, queryUnitGroups)
}

func materializeStatusModel(unit *Unit, snapshot visionapi.UnitSnapshot, manifest visionapi.StatusParticipationManifest, results map[string]statusProbeResult, enabled bool, observedAt time.Time) statusview.Model {
	model := statusview.NewModel()
	name := strings.TrimSuffix(manifest.Unit, ".service")
	description := strings.TrimSpace(unit.Description)
	if description == "" {
		description = strings.TrimSpace(snapshot.Description)
	}
	if description == "" {
		description = name
	}
	typeName := strings.ToLower(strings.TrimSpace(unit.Type))
	if typeName == "" {
		typeName = "service"
	}
	sourcePath := strings.TrimSpace(unit.SourcePath)
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(snapshot.SourcePath)
	}
	model.Identity = statusview.Identity{
		Unit:        manifest.Unit,
		Name:        name,
		Description: description,
		Type:        typeName,
		Scope:       manifest.Scope,
		SourcePath:  sourcePath,
	}
	model.Summary.RuntimeState = statusview.RuntimeState(canonicalRuntimeState(snapshot))
	model.Summary.EnabledState = "disabled"
	if enabled || statusManifestNamespaceApplicable(manifest, visionapi.StatusNamespaceControl) {
		model.Summary.EnabledState = "enabled"
	}
	model.Summary.MainPID = parseStatusPID(snapshot.MainPID)
	manifestEvidence := statusManifestEvidence(manifest, observedAt)

	for _, component := range manifest.Components {
		result, ok := results[component.Key]
		if !ok {
			result = statusProbeResult{State: "unknown", Evidence: []statusview.Evidence{{
				Source: statusview.EvidenceComponentStatus, Result: statusview.EvidenceError, CheckedAt: observedAt, Detail: "component probe returned no result",
			}}}
		}
		node := statusview.NewNode(component.Key, component.Type, component.Name, component.Scope)
		node.Expected = true
		node.State = strings.TrimSpace(result.State)
		if node.State == "" {
			node.State = "unknown"
		}
		node.PID = result.PID
		node.ManagerPID = result.ManagerPID
		node.Endpoint = component.Endpoint
		node.BusOwner = result.BusOwner
		node.CgroupPath = result.CgroupPath
		node.Evidence = append(node.Evidence, manifestEvidence)
		node.Evidence = append(node.Evidence, result.Evidence...)
		node.ObservedAt = latestStatusEvidenceCheck(node.Evidence)
		node.Health = statusHealthFromEvidence(component.Type, node.Evidence)
		if node.State == "missing" {
			node.LastSeenAt = latestStatusSourceObservation(node.Evidence)
		}
		model.Orchestration.Nodes = append(model.Orchestration.Nodes, node)
		if component.Type == "service" {
			if diagnostic := statusServiceObservationDiagnostic(node); diagnostic != nil {
				model.Diagnostics = append(model.Diagnostics, *diagnostic)
			}
		} else if node.Health != statusview.HealthHealthy {
			model.Diagnostics = append(model.Diagnostics, statusDiagnosticForNode(node))
		}
	}
	for _, relationship := range manifest.Relationships {
		model.Orchestration.Edges = append(model.Orchestration.Edges, statusview.Edge{
			From: relationship.From, To: relationship.To, Relation: statusview.Relation(relationship.Relation), Primary: relationship.Primary,
		})
	}
	return model
}

func statusManifestNamespaceApplicable(manifest visionapi.StatusParticipationManifest, name string) bool {
	for _, namespace := range manifest.Namespaces {
		if namespace.Name == name {
			return namespace.Applicable
		}
	}
	return false
}

func statusManifestEvidence(manifest visionapi.StatusParticipationManifest, checkedAt time.Time) statusview.Evidence {
	source := statusview.EvidenceSource(strings.TrimSpace(manifest.Source))
	if source == "" {
		source = statusview.EvidenceUnitConfiguration
	}
	return statusview.Evidence{
		Source:           source,
		Result:           statusview.EvidenceExpected,
		Authoritative:    true,
		CheckedAt:        checkedAt,
		SourceObservedAt: parseStatusObservationTime(manifest.GeneratedAt),
		Detail:           fmt.Sprintf("manifest version %d generation %d", manifest.Version, manifest.Generation),
	}
}

func queryStatusProcessStartedAt(ctx context.Context, pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, errors.New("process PID must be positive")
	}
	command := exec.CommandContext(ctx, "ps", "-p", fmt.Sprint(pid), "-o", "lstart=")
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.Output()
	if err != nil {
		return time.Time{}, err
	}
	startedAt, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.TrimSpace(string(output)), time.Local)
	if err != nil {
		return time.Time{}, err
	}
	return startedAt, nil
}

func statusHealthFromEvidence(componentType string, evidence []statusview.Evidence) statusview.Health {
	if componentType == "service" {
		return statusview.HealthHealthy
	}
	health := statusview.HealthHealthy
	for _, item := range evidence {
		switch item.Result {
		case statusview.EvidenceNotFound, statusview.EvidenceUnhealthy:
			health = statusview.HealthDegraded
		case statusview.EvidenceTimeout, statusview.EvidenceStale, statusview.EvidenceError:
			if health != statusview.HealthDegraded {
				health = statusview.HealthUnknown
			}
		}
	}
	return health
}

func statusDiagnosticForNode(node statusview.Node) statusview.Diagnostic {
	severity := statusview.SeverityUnknown
	code := "component_unobservable"
	message := fmt.Sprintf("Component %s cannot be observed.", node.Name)
	if node.Health == statusview.HealthDegraded {
		severity = statusview.SeverityDegraded
		code = "component_unhealthy"
		message = fmt.Sprintf("Component %s is unhealthy.", node.Name)
		if node.State == "missing" {
			code = "expected_node_missing"
			message = fmt.Sprintf("Expected component %s is missing.", node.Name)
		}
	}
	return statusview.Diagnostic{
		Severity: severity, Domain: statusview.DomainOrchestration, Code: code, Message: message,
		AffectsHealth: true, ObservedAt: node.ObservedAt, NodeID: node.ID,
	}
}

func statusServiceObservationDiagnostic(node statusview.Node) *statusview.Diagnostic {
	for _, evidence := range node.Evidence {
		switch evidence.Result {
		case statusview.EvidenceTimeout, statusview.EvidenceStale, statusview.EvidenceError:
			return &statusview.Diagnostic{
				Severity:      statusview.SeverityUnknown,
				Domain:        statusview.DomainRuntime,
				Code:          "runtime_unobservable",
				Message:       "Service runtime state could not be observed reliably.",
				AffectsHealth: true,
				ObservedAt:    node.ObservedAt,
				NodeID:        node.ID,
				Source:        string(evidence.Source),
			}
		}
	}
	return nil
}

func latestStatusEvidenceCheck(evidence []statusview.Evidence) time.Time {
	var latest time.Time
	for _, item := range evidence {
		if item.Result == statusview.EvidenceExpected {
			continue
		}
		if item.CheckedAt.After(latest) {
			latest = item.CheckedAt
		}
	}
	if !latest.IsZero() {
		return latest
	}
	for _, item := range evidence {
		if item.CheckedAt.After(latest) {
			latest = item.CheckedAt
		}
	}
	return latest
}

func latestStatusSourceObservation(evidence []statusview.Evidence) *time.Time {
	var latest *time.Time
	for _, item := range evidence {
		if item.Result == statusview.EvidenceExpected || item.SourceObservedAt == nil || latest != nil && !item.SourceObservedAt.After(*latest) {
			continue
		}
		value := item.SourceObservedAt.UTC()
		latest = &value
	}
	return latest
}
