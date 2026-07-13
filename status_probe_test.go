package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"servicectl/internal/cgrouptrack"
	"servicectl/internal/statusview"
	"servicectl/internal/visionapi"
)

func TestProbeStatusComponent(t *testing.T) {
	checkedAt := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	snapshotTime := checkedAt.Add(-time.Second)
	baseSnapshot := visionapi.UnitSnapshot{
		Name:       "demo.service",
		State:      "STARTED",
		MainPID:    "42",
		ManagerPID: "41",
		Phase:      "ready",
		ChildState: "running",
		Status:     "healthy",
		BusName:    "org.example.Demo",
		BusOwner:   ":1.42",
		UpdatedAt:  snapshotTime.Format(time.RFC3339Nano),
	}
	baseDeps := statusProbeDependencies{
		now: func() time.Time { return checkedAt },
		s6Status: func(_ context.Context, serviceName string) (statusRuntimeObservation, error) {
			return statusRuntimeObservation{State: "active", PID: 100, Healthy: true}, nil
		},
		dinitStatus: func(_ context.Context, mode, serviceName string) (statusRuntimeObservation, error) {
			if mode != visionapi.ModeSystem {
				t.Fatalf("dinit mode = %q, want system", mode)
			}
			return statusRuntimeObservation{State: "active", PID: 41, Healthy: true}, nil
		},
		busMeta: func(context.Context, string) (sysvisionMetaResponse, error) {
			return sysvisionMetaResponse{MetaResponse: visionapi.MetaResponse{ServicectlEventsConnected: true}}, nil
		},
		dbusOwner: func(context.Context, string, string) (string, error) { return ":1.42", nil },
		cgroupUnit: func(context.Context, string, uint32, string) (cgrouptrack.UnitStatus, error) {
			return cgrouptrack.UnitStatus{State: cgrouptrack.StateTracked, Path: "/servicectl.slice/demo", MemberCount: 1}, nil
		},
	}

	tests := []struct {
		name       string
		component  visionapi.StatusManifestComponent
		snapshot   visionapi.UnitSnapshot
		mutateDeps func(*statusProbeDependencies)
		wantResult statusview.EvidenceResult
		wantState  string
		wantPID    int
		wantOwner  string
		wantPath   string
	}{
		{
			name:       "service healthy",
			component:  probeComponent("service", "demo.service", "demo.service"),
			snapshot:   baseSnapshot,
			wantResult: statusview.EvidenceHealthy,
			wantState:  "active",
			wantPID:    42,
		},
		{
			name:      "service failed",
			component: probeComponent("service", "demo.service", "demo.service"),
			snapshot: func() visionapi.UnitSnapshot {
				value := baseSnapshot
				value.State = "STOPPED (terminated; exited - status 1)"
				value.MainPID = ""
				return value
			}(),
			wantResult: statusview.EvidenceUnhealthy,
			wantState:  "failed",
		},
		{
			name:      "service stale",
			component: probeComponent("service", "demo.service", "demo.service"),
			snapshot: func() visionapi.UnitSnapshot {
				value := baseSnapshot
				value.UpdatedAt = checkedAt.Add(-statusSnapshotFreshness - time.Second).Format(time.RFC3339Nano)
				return value
			}(),
			wantResult: statusview.EvidenceStale,
			wantState:  "active",
			wantPID:    42,
		},
		{
			name:       "s6 exact service healthy",
			component:  probeComponent("sys-orchestrd", "demo-orchestrd", "demo.service"),
			snapshot:   baseSnapshot,
			wantResult: statusview.EvidenceHealthy,
			wantState:  "active",
			wantPID:    100,
		},
		{
			name:      "s6 missing",
			component: probeComponent("sysvisiond", "sysvisiond", "system"),
			snapshot:  baseSnapshot,
			mutateDeps: func(deps *statusProbeDependencies) {
				deps.s6Status = func(context.Context, string) (statusRuntimeObservation, error) {
					return statusRuntimeObservation{}, os.ErrNotExist
				}
			},
			wantResult: statusview.EvidenceNotFound,
			wantState:  "missing",
		},
		{
			name:      "s6 unhealthy",
			component: probeComponent("s6", "s6", "system"),
			snapshot:  baseSnapshot,
			mutateDeps: func(deps *statusProbeDependencies) {
				deps.s6Status = func(context.Context, string) (statusRuntimeObservation, error) {
					return statusRuntimeObservation{State: "down", Healthy: false}, nil
				}
			},
			wantResult: statusview.EvidenceUnhealthy,
			wantState:  "down",
		},
		{
			name:      "s6 timeout",
			component: probeComponent("servicectl-api", "servicectl-api", "system"),
			snapshot:  baseSnapshot,
			mutateDeps: func(deps *statusProbeDependencies) {
				deps.s6Status = func(context.Context, string) (statusRuntimeObservation, error) {
					return statusRuntimeObservation{}, context.DeadlineExceeded
				}
			},
			wantResult: statusview.EvidenceTimeout,
			wantState:  "unknown",
		},
		{
			name:       "dinit healthy",
			component:  probeComponent("dinit", "demo-notifyd", "demo-notifyd"),
			snapshot:   baseSnapshot,
			wantResult: statusview.EvidenceHealthy,
			wantState:  "active",
			wantPID:    41,
		},
		{
			name:       "notify helper from snapshot",
			component:  probeComponent("sys-notifyd", "demo-notifyd", "demo-notifyd"),
			snapshot:   baseSnapshot,
			wantResult: statusview.EvidenceHealthy,
			wantState:  "running",
			wantPID:    41,
			wantOwner:  ":1.42",
		},
		{
			name:       "sysvbus connected",
			component:  probeComponent("sysvbus", "system", "system"),
			snapshot:   baseSnapshot,
			wantResult: statusview.EvidenceHealthy,
			wantState:  "connected",
		},
		{
			name:      "sysvbus operational error",
			component: probeComponent("sysvbus", "system", "system"),
			snapshot:  baseSnapshot,
			mutateDeps: func(deps *statusProbeDependencies) {
				deps.busMeta = func(context.Context, string) (sysvisionMetaResponse, error) {
					return sysvisionMetaResponse{}, errors.New("offline")
				}
			},
			wantResult: statusview.EvidenceError,
			wantState:  "unknown",
		},
		{
			name:       "dbus owner",
			component:  probeComponentWithBus("dbus", "system", "system", "org.example.Demo"),
			snapshot:   baseSnapshot,
			wantResult: statusview.EvidenceHealthy,
			wantState:  "owned",
			wantOwner:  ":1.42",
		},
		{
			name:      "dbus no owner",
			component: probeComponentWithBus("dbus", "system", "system", "org.example.Demo"),
			snapshot:  baseSnapshot,
			mutateDeps: func(deps *statusProbeDependencies) {
				deps.dbusOwner = func(context.Context, string, string) (string, error) { return "", errStatusDBusNoOwner }
			},
			wantResult: statusview.EvidenceNotFound,
			wantState:  "missing",
		},
		{
			name:       "cgroup tracked",
			component:  probeComponent("sys-cgroupd", "sys-cgroupd", "system"),
			snapshot:   baseSnapshot,
			wantResult: statusview.EvidenceHealthy,
			wantState:  "tracked",
			wantPath:   "/servicectl.slice/demo",
		},
		{
			name:      "cgroup missing",
			component: probeComponent("sys-cgroupd", "sys-cgroupd", "system"),
			snapshot:  baseSnapshot,
			mutateDeps: func(deps *statusProbeDependencies) {
				deps.cgroupUnit = func(context.Context, string, uint32, string) (cgrouptrack.UnitStatus, error) {
					return cgrouptrack.UnitStatus{}, os.ErrNotExist
				}
			},
			wantResult: statusview.EvidenceNotFound,
			wantState:  "missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := baseDeps
			if tt.mutateDeps != nil {
				tt.mutateDeps(&deps)
			}
			got := probeStatusComponent(context.Background(), tt.component, statusProbeInput{Mode: visionapi.ModeSystem, Unit: "demo.service", Snapshot: tt.snapshot}, deps)
			if got.State != tt.wantState || got.PID != tt.wantPID || got.BusOwner != tt.wantOwner || got.CgroupPath != tt.wantPath {
				t.Fatalf("probe result = %#v", got)
			}
			if len(got.Evidence) != 1 || got.Evidence[0].Result != tt.wantResult || !got.Evidence[0].CheckedAt.Equal(checkedAt) {
				t.Fatalf("evidence = %#v", got.Evidence)
			}
		})
	}
}

func TestProbeStatusComponentUsesExactDeclaredNames(t *testing.T) {
	called := []string{}
	deps := statusProbeDependencies{
		now: func() time.Time { return time.Now() },
		s6Status: func(_ context.Context, name string) (statusRuntimeObservation, error) {
			called = append(called, "s6:"+name)
			return statusRuntimeObservation{State: "active", Healthy: true}, nil
		},
		dinitStatus: func(_ context.Context, mode, name string) (statusRuntimeObservation, error) {
			if mode != visionapi.ModeSystem {
				t.Fatalf("dinit mode = %q, want system", mode)
			}
			called = append(called, "dinit:"+name)
			return statusRuntimeObservation{State: "active", Healthy: true}, nil
		},
	}
	probeStatusComponent(context.Background(), probeComponent("s6", "exact-orchestrd", "system"), statusProbeInput{}, deps)
	probeStatusComponent(context.Background(), probeComponent("sys-orchestrd", "exact-orchestrd", "demo.service"), statusProbeInput{}, deps)
	probeStatusComponent(context.Background(), probeComponent("dinit", "exact-backend", "exact-backend"), statusProbeInput{Mode: visionapi.ModeSystem}, deps)
	if !reflect.DeepEqual(called, []string{"s6:exact-orchestrd", "s6:exact-orchestrd", "dinit:exact-backend"}) {
		t.Fatalf("calls = %v", called)
	}
}

func TestQueryStatusDinitServiceUsesExplicitMode(t *testing.T) {
	tests := []struct {
		mode     string
		wantArgs []string
	}{
		{mode: visionapi.ModeSystem, wantArgs: []string{"status", "demo"}},
		{mode: visionapi.ModeUser, wantArgs: []string{"--user", "status", "demo"}},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			got, err := queryStatusDinitServiceWithCommand(context.Background(), tt.mode, "demo", func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "dinitctl" || !reflect.DeepEqual(args, tt.wantArgs) {
					t.Fatalf("command = %q %#v, want dinitctl %#v", name, args, tt.wantArgs)
				}
				return []byte("State: STARTED\nProcess ID: 42\n"), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.State != "active" || got.PID != 42 || !got.Healthy {
				t.Fatalf("observation = %#v", got)
			}
		})
	}
}

func TestProbeDoesNotInventParticipants(t *testing.T) {
	manifest := testStatusManifest("demo.service", "system")
	manifest.Components = append(manifest.Components, probeComponent("sysvisiond", "sysvisiond", "system"))
	deps := statusProbeDependencies{
		now: func() time.Time { return time.Now() },
		s6Status: func(context.Context, string) (statusRuntimeObservation, error) {
			return statusRuntimeObservation{State: "active", Healthy: true}, nil
		},
	}
	results := probeStatusParticipants(context.Background(), manifest, statusProbeInput{Snapshot: visionapi.UnitSnapshot{State: "STARTED"}}, deps)
	want := []string{manifest.Components[0].Key, manifest.Components[1].Key}
	got := make([]string, 0, len(results))
	for _, component := range manifest.Components {
		if _, ok := results[component.Key]; ok {
			got = append(got, component.Key)
		}
	}
	if !reflect.DeepEqual(got, want) || len(results) != len(want) {
		t.Fatalf("result keys = %v, want %v", got, want)
	}
}

func TestProbeStatusParticipantsDoesNotLetBlockedProbeStarveOthers(t *testing.T) {
	manifest := testStatusManifest("demo.service", "system")
	manifest.Components = []visionapi.StatusManifestComponent{
		probeComponent("sys-orchestrd", "blocked-orchestrd", "demo.service"),
		probeComponent("dinit", "demo", "demo"),
	}
	deps := statusProbeDependencies{
		now: time.Now,
		s6Status: func(ctx context.Context, _ string) (statusRuntimeObservation, error) {
			<-ctx.Done()
			return statusRuntimeObservation{}, ctx.Err()
		},
		dinitStatus: func(ctx context.Context, _, _ string) (statusRuntimeObservation, error) {
			if err := ctx.Err(); err != nil {
				return statusRuntimeObservation{}, err
			}
			return statusRuntimeObservation{State: "active", Healthy: true}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	results := probeStatusParticipants(ctx, manifest, statusProbeInput{Mode: visionapi.ModeSystem}, deps)
	dinitResult := results[manifest.Components[1].Key]
	if len(dinitResult.Evidence) != 1 || dinitResult.Evidence[0].Result != statusview.EvidenceHealthy {
		t.Fatalf("dinit evidence = %#v, want healthy despite blocked s6 probe", dinitResult.Evidence)
	}
}

func TestQueryStatusCgroupUnit(t *testing.T) {
	requestFn := func(_ context.Context, path string, request cgrouptrack.Request) (cgrouptrack.Response, error) {
		if path != "/test/cgroup.sock" || request.Operation != cgrouptrack.OpGetUnit || request.Mode != cgrouptrack.ModeUser || request.UID != 1000 || request.Unit != "demo.service" {
			t.Fatalf("request = path=%q %#v", path, request)
		}
		return cgrouptrack.Response{OK: true, Unit: &cgrouptrack.UnitStatus{State: cgrouptrack.StateTracked}}, nil
	}
	got, err := queryStatusCgroupUnit(context.Background(), "/test/cgroup.sock", "user", 1000, "demo", requestFn)
	if err != nil || got.State != cgrouptrack.StateTracked {
		t.Fatalf("result = %#v, err=%v", got, err)
	}
}

func probeComponent(typeName, serviceName, identity string) visionapi.StatusManifestComponent {
	key, _ := statusview.NewNodeID(typeName, "system", identity)
	return visionapi.StatusManifestComponent{Key: key, Type: typeName, Name: typeName, Scope: "system", Identity: identity, ServiceName: serviceName}
}

func probeComponentWithBus(typeName, serviceName, identity, busName string) visionapi.StatusManifestComponent {
	component := probeComponent(typeName, serviceName, identity)
	component.BusName = busName
	return component
}
