package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"servicectl/internal/statusview"
	"servicectl/internal/visionapi"
)

func TestCollectStatusModel(t *testing.T) {
	base := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	unit := &Unit{Name: "demo", Description: "Demo worker", Type: "notify", SourcePath: "/etc/systemd/system/demo.service"}
	snapshot := visionapi.UnitSnapshot{
		Name: "demo.service", Description: "Demo worker", Mode: visionapi.ModeSystem, SourcePath: unit.SourcePath,
		State: "STARTED", MainPID: "42", ManagerPID: "41", Phase: "ready", ChildState: "running",
		UpdatedAt: base.Add(-time.Second).Format(time.RFC3339Nano),
	}
	manifest, err := buildStatusParticipationManifest(statusManifestInput{Unit: unit, Mode: visionapi.ModeSystem, Enabled: true, GeneratedAt: base.Add(-2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}

	clock := &sequenceClock{times: []time.Time{base, base.Add(time.Millisecond)}}
	deps := statusCollectionDependencies{
		resolveUnit: func(name string) (*Unit, *SocketUnit, error) {
			if name != "demo" {
				t.Fatalf("resolve name = %q", name)
			}
			return unit, nil, nil
		},
		querySnapshot: func(_ context.Context, mode, unitName string) (visionapi.UnitSnapshot, error) {
			if mode != visionapi.ModeSystem || unitName != "demo.service" {
				t.Fatalf("snapshot input = mode %q unit %q", mode, unitName)
			}
			return snapshot, nil
		},
		queryManifest: func(context.Context, string, string) (visionapi.StatusParticipationManifest, error) {
			return manifest, nil
		},
		buildFallbackManifest: func(*Unit, *SocketUnit, string, uint32, string, time.Time) (visionapi.StatusParticipationManifest, error) {
			t.Fatal("fallback should not run")
			return visionapi.StatusParticipationManifest{}, nil
		},
		enabled: func(string) bool { return true },
		resolveOrchestrator: func(string, string) (string, error) {
			t.Fatal("fallback orchestrator resolution should not run when the manager manifest is complete")
			return "", nil
		},
		probeParticipants: func(_ context.Context, got visionapi.StatusParticipationManifest, input statusProbeInput) map[string]statusProbeResult {
			if !reflect.DeepEqual(got, manifest) || input.Unit != "demo.service" {
				t.Fatalf("probe input = %#v %#v", got, input)
			}
			return healthyProbeResults(manifest, snapshot, base)
		},
		collectLogs: func(_ context.Context, unit, mode string, observedAt time.Time) ([]statusview.LogEntry, []statusview.Diagnostic) {
			if unit != "demo.service" || mode != visionapi.ModeSystem || !observedAt.Equal(base.Add(time.Millisecond)) {
				t.Fatalf("log input = %q %q %s", unit, mode, observedAt)
			}
			return []statusview.LogEntry{{Timestamp: base, Stream: "stdout", Severity: statusview.LogInfo, Message: "ready"}}, nil
		},
		processStartedAt: func(context.Context, int) (time.Time, error) { return base.Add(-90 * time.Second), nil },
		now:              clock.Now,
	}

	got, err := collectStatusModel(context.Background(), "demo", visionapi.ModeSystem, 0, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 2 || got.Identity.Unit != "demo.service" || got.Identity.Description != "Demo worker" || got.Identity.Type != "notify" || got.Identity.Scope != "system" {
		t.Fatalf("identity = %#v", got)
	}
	if got.Summary.RuntimeState != statusview.RuntimeActive || got.Summary.EnabledState != "enabled" || got.Summary.MainPID != 42 || got.Summary.AggregateHealth != statusview.HealthHealthy {
		t.Fatalf("summary = %#v", got.Summary)
	}
	if got.Summary.StartedAt == nil || !got.Summary.StartedAt.Equal(base.Add(-90*time.Second)) || got.Summary.ActiveDurationSeconds != 90 {
		t.Fatalf("process timing = %#v", got.Summary)
	}
	if !got.ObservedAt.Equal(base.Add(time.Millisecond)) || len(got.Orchestration.Nodes) != len(manifest.Components) || len(got.Orchestration.Edges) != len(manifest.Relationships) || len(got.Logs) != 1 {
		t.Fatalf("model = %#v", got)
	}
	for _, node := range got.Orchestration.Nodes {
		if len(node.Evidence) < 2 || node.Evidence[0].Result != statusview.EvidenceExpected || !node.Evidence[0].Authoritative {
			t.Fatalf("node lacks authoritative participation evidence: %#v", node)
		}
	}
	for _, edge := range got.Orchestration.Edges {
		if edge.From == "" || edge.To == "" || edge.Relation == "" {
			t.Fatalf("invalid edge = %#v", edge)
		}
	}
}

func TestStatusCollectionBoundary(t *testing.T) {
	base := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	unit := &Unit{Name: "demo", Type: "simple"}
	fallback, err := buildStatusParticipationManifest(statusManifestInput{Unit: unit, Mode: visionapi.ModeSystem, Enabled: false, GeneratedAt: base})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := visionapi.UnitSnapshot{Name: "demo.service", State: "STARTED", UpdatedAt: base.Format(time.RFC3339Nano)}

	tests := []struct {
		name         string
		resolveErr   error
		snapshotErr  error
		preferred    visionapi.StatusParticipationManifest
		preferredErr error
		fallback     visionapi.StatusParticipationManifest
		fallbackErr  error
		wantKind     statusCollectionErrorKind
		wantSource   string
	}{
		{name: "unit not found", resolveErr: errStatusUnitNotFound, wantKind: statusCollectionNotFound},
		{name: "resolution failure", resolveErr: errors.New("permission denied"), wantKind: statusCollectionMandatory},
		{name: "mandatory snapshot failure", snapshotErr: errors.New("snapshot unavailable"), wantKind: statusCollectionMandatory},
		{name: "preferred manifest", preferred: fallback, wantSource: fallback.Source},
		{name: "preferred manifest for another unit is rejected", preferred: func() visionapi.StatusParticipationManifest {
			value := cloneStatusManifest(fallback)
			value.Unit = "other.service"
			value.Components[0].Identity = value.Unit
			value.Components[0].ServiceName = value.Unit
			value.Components[0].Key, _ = statusview.NewNodeID("service", value.Scope, value.Unit)
			return value
		}(), wantKind: statusCollectionMandatory},
		{name: "preferred manifest for another plane is rejected", preferred: func() visionapi.StatusParticipationManifest {
			value := cloneStatusManifest(fallback)
			value.Mode = visionapi.ModeUser
			value.UID = 1000
			value.Scope = "user@1000"
			value.Components[0].Scope = value.Scope
			value.Components[0].Key, _ = statusview.NewNodeID("service", value.Scope, value.Unit)
			return value
		}(), wantKind: statusCollectionMandatory},
		{name: "fallback after manager unavailable", preferredErr: errors.New("manager unavailable"), fallback: fallback, wantSource: fallback.Source},
		{name: "fallback after incomplete preferred", preferred: func() visionapi.StatusParticipationManifest { value := fallback; value.Complete = false; return value }(), fallback: fallback, wantSource: fallback.Source},
		{name: "no complete manifest", preferredErr: errors.New("manager unavailable"), fallbackErr: errors.New("fallback failed"), wantKind: statusCollectionMandatory},
		{name: "incomplete fallback", preferredErr: errors.New("manager unavailable"), fallback: func() visionapi.StatusParticipationManifest {
			value := cloneStatusManifest(fallback)
			value.Namespaces[0].Complete = false
			return value
		}(), wantKind: statusCollectionMandatory},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := statusCollectionDependencies{
				resolveUnit: func(string) (*Unit, *SocketUnit, error) {
					if tt.resolveErr != nil {
						return nil, nil, tt.resolveErr
					}
					return unit, nil, nil
				},
				querySnapshot: func(context.Context, string, string) (visionapi.UnitSnapshot, error) {
					return snapshot, tt.snapshotErr
				},
				queryManifest: func(context.Context, string, string) (visionapi.StatusParticipationManifest, error) {
					return tt.preferred, tt.preferredErr
				},
				buildFallbackManifest: func(*Unit, *SocketUnit, string, uint32, string, time.Time) (visionapi.StatusParticipationManifest, error) {
					return tt.fallback, tt.fallbackErr
				},
				enabled: func(string) bool { return false },
				resolveOrchestrator: func(string, string) (string, error) {
					return "", nil
				},
				probeParticipants: func(_ context.Context, manifest visionapi.StatusParticipationManifest, _ statusProbeInput) map[string]statusProbeResult {
					return healthyProbeResults(manifest, snapshot, base)
				},
				collectLogs: func(context.Context, string, string, time.Time) ([]statusview.LogEntry, []statusview.Diagnostic) {
					return nil, nil
				},
				now: func() time.Time { return base },
			}
			got, err := collectStatusModel(context.Background(), "demo", visionapi.ModeSystem, 0, deps)
			if tt.wantKind != "" {
				var collectionErr *statusCollectionError
				if !errors.As(err, &collectionErr) || collectionErr.Kind != tt.wantKind {
					t.Fatalf("error = %T %v, want kind %q", err, err, tt.wantKind)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.SchemaVersion != 2 || len(got.Orchestration.Nodes) == 0 {
				t.Fatalf("model = %#v", got)
			}
		})
	}
}

func TestCollectStatusModelRejectsSnapshotForAnotherUnit(t *testing.T) {
	base := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	unit := &Unit{Name: "demo", Type: "simple"}
	manifest, err := buildStatusParticipationManifest(statusManifestInput{Unit: unit, Mode: visionapi.ModeSystem, GeneratedAt: base})
	if err != nil {
		t.Fatal(err)
	}
	deps := basicStatusCollectionDependencies(unit, visionapi.UnitSnapshot{
		Name: "other.service", Mode: visionapi.ModeSystem, State: "STARTED", UpdatedAt: base.Format(time.RFC3339Nano),
	}, manifest, base)
	_, err = collectStatusModel(context.Background(), "demo", visionapi.ModeSystem, 0, deps)
	var collectionErr *statusCollectionError
	if !errors.As(err, &collectionErr) || collectionErr.Kind != statusCollectionMandatory {
		t.Fatalf("error = %T %v, want mandatory collection error", err, err)
	}
}

func TestQueryStatusSnapshotFallsBackToDirectRuntime(t *testing.T) {
	want := visionapi.UnitSnapshot{Name: "demo.service", State: "STARTED"}
	got, err := queryStatusSnapshot(context.Background(), visionapi.ModeUser, "demo.service",
		func(context.Context, string, string) (visionapi.UnitSnapshot, error) {
			return visionapi.UnitSnapshot{}, errors.New("sysvision unavailable")
		},
		func(mode, unit string) (visionapi.UnitSnapshot, error) {
			if mode != visionapi.ModeUser || unit != "demo.service" {
				t.Fatalf("fallback input = mode %q unit %q", mode, unit)
			}
			return want, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %#v, want %#v", got, want)
	}
}

func TestQueryStatusSnapshotReportsBothFailures(t *testing.T) {
	_, err := queryStatusSnapshot(context.Background(), visionapi.ModeSystem, "demo.service",
		func(context.Context, string, string) (visionapi.UnitSnapshot, error) {
			return visionapi.UnitSnapshot{}, errors.New("sysvision unavailable")
		},
		func(string, string) (visionapi.UnitSnapshot, error) {
			return visionapi.UnitSnapshot{}, errors.New("dinit unavailable")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "sysvision unavailable") || !strings.Contains(err.Error(), "dinit unavailable") {
		t.Fatalf("error = %v, want preferred and fallback failures", err)
	}
}

func TestStatusCollectionFallbackUsesExactGroupOrchestrator(t *testing.T) {
	base := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	unit := &Unit{Name: "pipewire-pulse", Type: "notify"}
	snapshot := visionapi.UnitSnapshot{Name: "pipewire-pulse.service", Mode: visionapi.ModeUser, UID: 1000, State: "STARTED", UpdatedAt: base.Format(time.RFC3339Nano)}
	var fallbackOrchestrator string
	deps := statusCollectionDependencies{
		resolveUnit:   func(string) (*Unit, *SocketUnit, error) { return unit, nil, nil },
		querySnapshot: func(context.Context, string, string) (visionapi.UnitSnapshot, error) { return snapshot, nil },
		queryManifest: func(context.Context, string, string) (visionapi.StatusParticipationManifest, error) {
			return visionapi.StatusParticipationManifest{}, errors.New("servicectl-api unavailable")
		},
		resolveOrchestrator: func(mode, name string) (string, error) {
			if mode != visionapi.ModeUser || name != "pipewire-pulse.service" {
				t.Fatalf("orchestrator input = mode %q unit %q", mode, name)
			}
			return "group-pipewire-orchestrd", nil
		},
		buildFallbackManifest: func(gotUnit *Unit, _ *SocketUnit, mode string, uid uint32, orchestrator string, generatedAt time.Time) (visionapi.StatusParticipationManifest, error) {
			fallbackOrchestrator = orchestrator
			return buildStatusParticipationManifest(statusManifestInput{
				Unit: gotUnit, Mode: mode, UID: uid, Enabled: orchestrator != "", OrchestratorService: orchestrator, GeneratedAt: generatedAt,
			})
		},
		enabled: func(string) bool { return false },
		probeParticipants: func(_ context.Context, manifest visionapi.StatusParticipationManifest, _ statusProbeInput) map[string]statusProbeResult {
			return healthyProbeResults(manifest, snapshot, base)
		},
		collectLogs: func(context.Context, string, string, time.Time) ([]statusview.LogEntry, []statusview.Diagnostic) {
			return nil, nil
		},
		now: func() time.Time { return base },
	}
	model, err := collectStatusModel(context.Background(), "pipewire-pulse", visionapi.ModeUser, 1000, deps)
	if err != nil {
		t.Fatal(err)
	}
	if fallbackOrchestrator != "group-pipewire-orchestrd" {
		t.Fatalf("fallback orchestrator = %q", fallbackOrchestrator)
	}
	if model.Summary.EnabledState != "enabled" {
		t.Fatalf("enabled state = %q, want enabled for group-managed unit", model.Summary.EnabledState)
	}
	found := false
	for _, node := range model.Orchestration.Nodes {
		if node.Type == "sys-orchestrd" {
			found = true
			if !strings.Contains(node.Name, "pipewire-pulse.service") {
				t.Fatalf("orchestrator node = %#v", node)
			}
		}
	}
	if !found {
		t.Fatal("group orchestrator node is missing")
	}
}

func TestResolveStatusOrchestratorWithQueriesUsesManagerSemantics(t *testing.T) {
	lists := visionapi.UnitListsResponse{
		EnabledGroups:  []string{"pipewire"},
		RunnerUnits:    []string{"pipewire-pulse.service"},
		EffectiveUnits: []string{"pipewire-pulse.service"},
	}
	got, err := resolveStatusOrchestratorWithQueries(visionapi.ModeUser, "pipewire-pulse.service",
		func(mode string) (visionapi.UnitListsResponse, error) {
			if mode != visionapi.ModeUser {
				t.Fatalf("list mode = %q", mode)
			}
			return lists, nil
		},
		func(mode, unit string) (visionapi.UnitGroupsResponse, error) {
			return visionapi.UnitGroupsResponse{Groups: []visionapi.GroupState{{Name: "pipewire", Enabled: true}}}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "group-pipewire-orchestrd" {
		t.Fatalf("orchestrator = %q, want group-pipewire-orchestrd", got)
	}
}

func TestResolveStatusOrchestratorWithQueriesPropagatesListFailure(t *testing.T) {
	_, err := resolveStatusOrchestratorWithQueries(visionapi.ModeSystem, "demo.service",
		func(string) (visionapi.UnitListsResponse, error) {
			return visionapi.UnitListsResponse{}, errors.New("unit lists unavailable")
		},
		func(string, string) (visionapi.UnitGroupsResponse, error) {
			t.Fatal("group query should not run after list failure")
			return visionapi.UnitGroupsResponse{}, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "unit lists unavailable") {
		t.Fatalf("error = %v, want unit list failure", err)
	}
}

func TestCollectStatusModelMapsProbeEvidence(t *testing.T) {
	base := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	unit := &Unit{Name: "demo", Type: "simple"}
	manifest, err := buildStatusParticipationManifest(statusManifestInput{Unit: unit, Mode: visionapi.ModeSystem, Enabled: true, GeneratedAt: base})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := visionapi.UnitSnapshot{Name: "demo.service", State: "STARTED", UpdatedAt: base.Format(time.RFC3339Nano)}

	tests := []struct {
		name       string
		result     statusview.EvidenceResult
		wantHealth statusview.Health
		wantState  string
		wantCode   string
	}{
		{name: "missing is degraded", result: statusview.EvidenceNotFound, wantHealth: statusview.HealthDegraded, wantState: "missing", wantCode: "expected_node_missing"},
		{name: "unhealthy is degraded", result: statusview.EvidenceUnhealthy, wantHealth: statusview.HealthDegraded, wantState: "down", wantCode: "component_unhealthy"},
		{name: "timeout is unknown", result: statusview.EvidenceTimeout, wantHealth: statusview.HealthUnknown, wantState: "unknown", wantCode: "component_unobservable"},
		{name: "stale is unknown", result: statusview.EvidenceStale, wantHealth: statusview.HealthUnknown, wantState: "active", wantCode: "component_unobservable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := manifest.Components[0]
			for _, component := range manifest.Components {
				if component.Type != "service" {
					target = component
					break
				}
			}
			deps := basicStatusCollectionDependencies(unit, snapshot, manifest, base)
			deps.probeParticipants = func(_ context.Context, got visionapi.StatusParticipationManifest, _ statusProbeInput) map[string]statusProbeResult {
				results := healthyProbeResults(got, snapshot, base)
				state := "active"
				if tt.result == statusview.EvidenceNotFound {
					state = "missing"
				} else if tt.result == statusview.EvidenceUnhealthy {
					state = "down"
				} else if tt.result == statusview.EvidenceTimeout {
					state = "unknown"
				}
				results[target.Key] = statusProbeResult{State: state, Evidence: []statusview.Evidence{{Source: statusview.EvidenceComponentStatus, Result: tt.result, CheckedAt: base}}}
				return results
			}
			got, err := collectStatusModel(context.Background(), "demo", visionapi.ModeSystem, 0, deps)
			if err != nil {
				t.Fatal(err)
			}
			node := findStatusNode(got, target.Key)
			if node == nil || node.Health != tt.wantHealth || node.State != tt.wantState {
				t.Fatalf("node = %#v", node)
			}
			if len(got.Diagnostics) == 0 || got.Diagnostics[0].Code != tt.wantCode {
				t.Fatalf("diagnostics = %#v", got.Diagnostics)
			}
		})
	}
}

func TestStatusObservationTimes(t *testing.T) {
	base := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	lastSeen := base.Add(-time.Minute)
	unit := &Unit{Name: "demo", Type: "simple"}
	manifest, _ := buildStatusParticipationManifest(statusManifestInput{Unit: unit, Mode: visionapi.ModeSystem, Enabled: false, GeneratedAt: base})
	snapshot := visionapi.UnitSnapshot{Name: "demo.service", State: "STARTED", MainPID: "42", UpdatedAt: lastSeen.Format(time.RFC3339Nano)}
	deps := basicStatusCollectionDependencies(unit, snapshot, manifest, base)
	deps.probeParticipants = func(_ context.Context, got visionapi.StatusParticipationManifest, _ statusProbeInput) map[string]statusProbeResult {
		return map[string]statusProbeResult{got.Components[0].Key: {
			State: "active", PID: 42,
			Evidence: []statusview.Evidence{{Source: statusview.EvidenceSysvisionSnapshot, Result: statusview.EvidenceHealthy, Authoritative: true, CheckedAt: base, SourceObservedAt: &lastSeen}},
		}}
	}
	got, err := collectStatusModel(context.Background(), "demo", visionapi.ModeSystem, 0, deps)
	if err != nil {
		t.Fatal(err)
	}
	node := findStatusNode(got, manifest.Components[0].Key)
	if node == nil || !node.ObservedAt.Equal(base) || node.PID != 42 {
		t.Fatalf("node = %#v", node)
	}
}

func TestStatusObservationTimeIgnoresLaterExpectedEvidence(t *testing.T) {
	probeTime := time.Date(2026, 7, 12, 10, 29, 59, 0, time.UTC)
	manifestTime := probeTime.Add(time.Second)
	evidence := []statusview.Evidence{
		{Source: statusview.EvidenceManager, Result: statusview.EvidenceExpected, Authoritative: true, CheckedAt: manifestTime},
		{Source: statusview.EvidenceComponentStatus, Result: statusview.EvidenceHealthy, CheckedAt: probeTime},
	}
	if got := latestStatusEvidenceCheck(evidence); !got.Equal(probeTime) {
		t.Fatalf("observed_at = %s, want health probe time %s", got, probeTime)
	}
}

func TestCollectStatusModelStaleServiceObservationIsUnknown(t *testing.T) {
	base := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	unit := &Unit{Name: "demo", Type: "simple"}
	manifest, _ := buildStatusParticipationManifest(statusManifestInput{Unit: unit, Mode: visionapi.ModeSystem, Enabled: false, GeneratedAt: base})
	snapshot := visionapi.UnitSnapshot{Name: "demo.service", State: "STARTED", UpdatedAt: base.Add(-time.Minute).Format(time.RFC3339Nano)}
	deps := basicStatusCollectionDependencies(unit, snapshot, manifest, base)
	deps.probeParticipants = func(_ context.Context, got visionapi.StatusParticipationManifest, _ statusProbeInput) map[string]statusProbeResult {
		return map[string]statusProbeResult{got.Components[0].Key: {
			State:    "active",
			Evidence: []statusview.Evidence{{Source: statusview.EvidenceSysvisionSnapshot, Result: statusview.EvidenceStale, CheckedAt: base}},
		}}
	}
	got, err := collectStatusModel(context.Background(), "demo", visionapi.ModeSystem, 0, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.RuntimeState != statusview.RuntimeActive || got.Summary.AggregateHealth != statusview.HealthUnknown || got.Summary.DisplayState != "active (unknown)" {
		t.Fatalf("summary = %#v", got.Summary)
	}
	if len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "runtime_unobservable" || got.Diagnostics[0].Domain != statusview.DomainRuntime {
		t.Fatalf("diagnostics = %#v", got.Diagnostics)
	}
}

func basicStatusCollectionDependencies(unit *Unit, snapshot visionapi.UnitSnapshot, manifest visionapi.StatusParticipationManifest, now time.Time) statusCollectionDependencies {
	return statusCollectionDependencies{
		resolveUnit:   func(string) (*Unit, *SocketUnit, error) { return unit, nil, nil },
		querySnapshot: func(context.Context, string, string) (visionapi.UnitSnapshot, error) { return snapshot, nil },
		queryManifest: func(context.Context, string, string) (visionapi.StatusParticipationManifest, error) {
			return manifest, nil
		},
		buildFallbackManifest: func(*Unit, *SocketUnit, string, uint32, string, time.Time) (visionapi.StatusParticipationManifest, error) {
			return manifest, nil
		},
		enabled: func(string) bool { return true },
		resolveOrchestrator: func(string, string) (string, error) {
			if manifest.Namespaces[2].Applicable {
				return "demo-orchestrd", nil
			}
			return "", nil
		},
		probeParticipants: func(_ context.Context, got visionapi.StatusParticipationManifest, _ statusProbeInput) map[string]statusProbeResult {
			return healthyProbeResults(got, snapshot, now)
		},
		collectLogs: func(context.Context, string, string, time.Time) ([]statusview.LogEntry, []statusview.Diagnostic) {
			return nil, nil
		},
		processStartedAt: func(context.Context, int) (time.Time, error) { return time.Time{}, errors.New("unavailable") },
		now:              func() time.Time { return now },
	}
}

func healthyProbeResults(manifest visionapi.StatusParticipationManifest, snapshot visionapi.UnitSnapshot, checkedAt time.Time) map[string]statusProbeResult {
	results := make(map[string]statusProbeResult, len(manifest.Components))
	for _, component := range manifest.Components {
		state := "active"
		pid := 0
		if component.Type == "service" {
			state = string(canonicalRuntimeState(snapshot))
			pid = parseStatusPID(snapshot.MainPID)
		}
		results[component.Key] = statusProbeResult{State: state, PID: pid, Evidence: []statusview.Evidence{{Source: statusview.EvidenceComponentStatus, Result: statusview.EvidenceHealthy, CheckedAt: checkedAt}}}
	}
	return results
}

func findStatusNode(model statusview.Model, id string) *statusview.Node {
	for i := range model.Orchestration.Nodes {
		if model.Orchestration.Nodes[i].ID == id {
			return &model.Orchestration.Nodes[i]
		}
	}
	return nil
}

func cloneStatusManifest(in visionapi.StatusParticipationManifest) visionapi.StatusParticipationManifest {
	out := in
	out.Namespaces = append([]visionapi.StatusManifestNamespace(nil), in.Namespaces...)
	out.Components = append([]visionapi.StatusManifestComponent(nil), in.Components...)
	out.Relationships = append([]visionapi.StatusManifestRelationship(nil), in.Relationships...)
	return out
}

type sequenceClock struct {
	times []time.Time
	index int
}

func (c *sequenceClock) Now() time.Time {
	if len(c.times) == 0 {
		return time.Time{}
	}
	if c.index >= len(c.times) {
		return c.times[len(c.times)-1]
	}
	value := c.times[c.index]
	c.index++
	return value
}
