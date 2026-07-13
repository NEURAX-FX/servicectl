package main

import (
	"reflect"
	"testing"
	"time"

	"servicectl/internal/visionapi"
)

func TestBuildStatusParticipationManifest(t *testing.T) {
	generatedAt := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	tests := []struct {
		name              string
		input             statusManifestInput
		wantScope         string
		wantTypes         []string
		wantPrimary       []string
		wantNonPrimary    []string
		wantApplicable    map[string]bool
		wantComponentData map[string]string
	}{
		{
			name: "disabled direct service has no external control path",
			input: statusManifestInput{
				Unit:        &Unit{Name: "demo", Type: "simple"},
				Mode:        visionapi.ModeSystem,
				Enabled:     false,
				GeneratedAt: generatedAt,
			},
			wantScope:      "system",
			wantTypes:      []string{"service"},
			wantApplicable: allManifestNamespaces(false, false, false, false),
		},
		{
			name: "enabled direct system service",
			input: statusManifestInput{
				Unit:        &Unit{Name: "demo", Type: "simple"},
				Mode:        visionapi.ModeSystem,
				Enabled:     true,
				Generation:  42,
				GeneratedAt: generatedAt,
			},
			wantScope:      "system",
			wantTypes:      []string{"servicectl-api", "s6", "sys-orchestrd", "dinit", "service", "sysvbus", "sysvisiond", "sys-cgroupd"},
			wantPrimary:    []string{"servicectl-api>s6:controls", "s6>sys-orchestrd:supervises", "sys-orchestrd>dinit:controls", "dinit>service:supervises"},
			wantNonPrimary: []string{"sys-orchestrd>sysvbus:observes", "sysvisiond>service:observes", "sys-cgroupd>service:accounts"},
			wantApplicable: allManifestNamespaces(true, true, true, true),
		},
		{
			name: "enabled notify service includes helper",
			input: statusManifestInput{
				Unit:        &Unit{Name: "api", Type: "notify"},
				Mode:        visionapi.ModeSystem,
				Enabled:     true,
				GeneratedAt: generatedAt,
			},
			wantScope:      "system",
			wantTypes:      []string{"servicectl-api", "s6", "sys-orchestrd", "dinit", "sys-notifyd", "service", "sysvbus", "sysvisiond", "sys-cgroupd"},
			wantPrimary:    []string{"servicectl-api>s6:controls", "s6>sys-orchestrd:supervises", "sys-orchestrd>dinit:controls", "dinit>sys-notifyd:supervises", "sys-notifyd>service:supervises"},
			wantNonPrimary: []string{"sys-orchestrd>sysvbus:observes", "sysvisiond>service:observes", "sys-cgroupd>service:accounts"},
			wantApplicable: allManifestNamespaces(true, true, true, true),
		},
		{
			name: "enabled dbus service includes typed bus",
			input: statusManifestInput{
				Unit:        &Unit{Name: "hostnamed", Type: "dbus", BusName: "org.example.Hostname"},
				Mode:        visionapi.ModeSystem,
				Enabled:     true,
				GeneratedAt: generatedAt,
			},
			wantScope:      "system",
			wantTypes:      []string{"servicectl-api", "s6", "sys-orchestrd", "dinit", "sys-notifyd", "service", "sysvbus", "dbus", "sysvisiond", "sys-cgroupd"},
			wantPrimary:    []string{"servicectl-api>s6:controls", "s6>sys-orchestrd:supervises", "sys-orchestrd>dinit:controls", "dinit>sys-notifyd:supervises", "sys-notifyd>service:supervises"},
			wantNonPrimary: []string{"sys-orchestrd>sysvbus:observes", "dbus>service:activates", "sysvisiond>service:observes", "sys-cgroupd>service:accounts"},
			wantApplicable: allManifestNamespaces(true, true, true, true),
			wantComponentData: map[string]string{
				"s6.service_name": "hostnamed-orchestrd",
				"dbus.bus_name":   "org.example.Hostname",
				"dbus.name":       "dbus · system",
			},
		},
		{
			name: "enabled socket service includes socket helper",
			input: statusManifestInput{
				Unit:        &Unit{Name: "echo", Type: "simple"},
				SocketUnit:  &SocketUnit{Name: "echo.socket", Service: "echo.service", ListenStreams: []string{"127.0.0.1:9000"}},
				Mode:        visionapi.ModeSystem,
				Enabled:     true,
				GeneratedAt: generatedAt,
			},
			wantScope:      "system",
			wantTypes:      []string{"servicectl-api", "s6", "sys-orchestrd", "dinit", "sys-notifyd", "service", "sysvbus", "sysvisiond", "sys-cgroupd"},
			wantPrimary:    []string{"servicectl-api>s6:controls", "s6>sys-orchestrd:supervises", "sys-orchestrd>dinit:controls", "dinit>sys-notifyd:supervises", "sys-notifyd>service:supervises"},
			wantNonPrimary: []string{"sys-orchestrd>sysvbus:observes", "sysvisiond>service:observes", "sys-cgroupd>service:accounts"},
			wantApplicable: allManifestNamespaces(true, true, true, true),
			wantComponentData: map[string]string{
				"sys-notifyd.service_name": "echo-socketd",
			},
		},
		{
			name: "enabled user service uses user scope with system cgroup accounting",
			input: statusManifestInput{
				Unit:        &Unit{Name: "portal", Type: "notify", BusName: "org.example.Portal"},
				Mode:        visionapi.ModeUser,
				UID:         1000,
				Enabled:     true,
				GeneratedAt: generatedAt,
			},
			wantScope:      "user@1000",
			wantTypes:      []string{"servicectl-api", "s6", "sys-orchestrd", "dinit", "sys-notifyd", "service", "sysvbus", "dbus", "sysvisiond", "sys-cgroupd"},
			wantPrimary:    []string{"servicectl-api>s6:controls", "s6>sys-orchestrd:supervises", "sys-orchestrd>dinit:controls", "dinit>sys-notifyd:supervises", "sys-notifyd>service:supervises"},
			wantNonPrimary: []string{"sys-orchestrd>sysvbus:observes", "dbus>service:activates", "sysvisiond>service:observes", "sys-cgroupd>service:accounts"},
			wantApplicable: allManifestNamespaces(true, true, true, true),
			wantComponentData: map[string]string{
				"servicectl-api.endpoint": "/run/user/1000/servicectl/servicectl.sock",
				"sysvbus.name":            "sysvbus · user@1000",
				"sysvbus.endpoint":        "/run/user/1000/servicectl/servicectl-events.sock",
				"dbus.name":               "dbus · user@1000",
				"sysvisiond.endpoint":     "/run/user/1000/servicectl/sysvision/sysvisiond.sock",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildStatusParticipationManifest(tt.input)
			if err != nil {
				t.Fatalf("buildStatusParticipationManifest: %v", err)
			}
			if got.Version != visionapi.StatusManifestVersion || !got.Complete {
				t.Fatalf("version/complete = %d/%v", got.Version, got.Complete)
			}
			if got.Unit != tt.input.Unit.Name+".service" || got.Scope != tt.wantScope {
				t.Fatalf("identity = %q %q, want %q %q", got.Unit, got.Scope, tt.input.Unit.Name+".service", tt.wantScope)
			}
			if got.Generation != tt.input.Generation || got.GeneratedAt != generatedAt.Format(time.RFC3339Nano) {
				t.Fatalf("generation = %d %q", got.Generation, got.GeneratedAt)
			}
			if types := manifestComponentTypes(got); !reflect.DeepEqual(types, tt.wantTypes) {
				t.Fatalf("component types = %v, want %v", types, tt.wantTypes)
			}
			if primary := manifestRelations(got, true); !reflect.DeepEqual(primary, tt.wantPrimary) {
				t.Fatalf("primary relations = %v, want %v", primary, tt.wantPrimary)
			}
			if side := manifestRelations(got, false); !reflect.DeepEqual(side, tt.wantNonPrimary) {
				t.Fatalf("side relations = %v, want %v", side, tt.wantNonPrimary)
			}
			if applicable := manifestNamespaceApplicability(got); !reflect.DeepEqual(applicable, tt.wantApplicable) {
				t.Fatalf("namespace applicability = %v, want %v", applicable, tt.wantApplicable)
			}
			for path, want := range tt.wantComponentData {
				if gotValue := manifestComponentValue(got, path); gotValue != want {
					t.Fatalf("component %s = %q, want %q", path, gotValue, want)
				}
			}
			if err := visionapi.ValidateStatusParticipationManifest(got); err != nil {
				t.Fatalf("manifest validation: %v", err)
			}
		})
	}
}

func TestBuildStatusParticipationManifestDeterministic(t *testing.T) {
	input := statusManifestInput{
		Unit:        &Unit{Name: "demo", Type: "notify", BusName: "org.example.Demo"},
		Mode:        visionapi.ModeUser,
		UID:         1000,
		Enabled:     true,
		Generation:  9,
		GeneratedAt: time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC),
	}
	first, err := buildStatusParticipationManifest(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildStatusParticipationManifest(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("manifest is not deterministic\nfirst: %#v\nsecond: %#v", first, second)
	}
}

func TestBuildStatusParticipationManifestUsesDeclaredOrchestrator(t *testing.T) {
	manifest, err := buildStatusParticipationManifest(statusManifestInput{
		Unit:                &Unit{Name: "pipewire-pulse", Type: "notify"},
		Mode:                visionapi.ModeUser,
		UID:                 1000,
		Enabled:             true,
		OrchestratorService: "group-pipewire-orchestrd",
		GeneratedAt:         time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := manifestComponentValue(manifest, "s6.service_name"); got != "group-pipewire-orchestrd" {
		t.Fatalf("s6 service_name = %q, want group-pipewire-orchestrd", got)
	}
	if got := manifestComponentValue(manifest, "sys-orchestrd.service_name"); got != "group-pipewire-orchestrd" {
		t.Fatalf("orchestrator service_name = %q, want group-pipewire-orchestrd", got)
	}
}

func TestBuildStatusParticipationManifestDeclaresServicectlAPI(t *testing.T) {
	manifest, err := buildStatusParticipationManifest(statusManifestInput{
		Unit:                &Unit{Name: "demo", Type: "simple"},
		Mode:                visionapi.ModeUser,
		UID:                 1000,
		Enabled:             true,
		OrchestratorService: "demo-orchestrd",
		GeneratedAt:         time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := manifestComponentValue(manifest, "servicectl-api.service_name"); got != "servicectl-api-user-1000" {
		t.Fatalf("servicectl-api service_name = %q, want servicectl-api-user-1000", got)
	}
	primary := manifestRelations(manifest, true)
	if len(primary) == 0 || primary[0] != "servicectl-api>s6:controls" {
		t.Fatalf("primary relations = %v, want servicectl-api>s6:controls first", primary)
	}
}

func TestManifestNamespaces(t *testing.T) {
	manifest, err := buildStatusParticipationManifest(statusManifestInput{
		Unit:        &Unit{Name: "demo", Type: "simple"},
		Mode:        visionapi.ModeSystem,
		Enabled:     true,
		GeneratedAt: time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"accounting", "bus", "control", "observation"}
	if len(manifest.Namespaces) != len(want) {
		t.Fatalf("namespace count = %d, want %d", len(manifest.Namespaces), len(want))
	}
	for i, name := range want {
		got := manifest.Namespaces[i]
		if got.Name != name || !got.Complete {
			t.Fatalf("namespace %d = %#v, want complete %q", i, got, name)
		}
	}
}

func allManifestNamespaces(control, observation, accounting, bus bool) map[string]bool {
	return map[string]bool{
		"accounting":  accounting,
		"bus":         bus,
		"control":     control,
		"observation": observation,
	}
}

func manifestComponentTypes(manifest visionapi.StatusParticipationManifest) []string {
	result := make([]string, 0, len(manifest.Components))
	for _, component := range manifest.Components {
		result = append(result, component.Type)
	}
	return result
}

func manifestRelations(manifest visionapi.StatusParticipationManifest, primary bool) []string {
	components := make(map[string]string, len(manifest.Components))
	for _, component := range manifest.Components {
		components[component.Key] = component.Type
	}
	var result []string
	for _, relationship := range manifest.Relationships {
		if relationship.Primary == primary {
			result = append(result, components[relationship.From]+">"+components[relationship.To]+":"+relationship.Relation)
		}
	}
	return result
}

func manifestNamespaceApplicability(manifest visionapi.StatusParticipationManifest) map[string]bool {
	result := make(map[string]bool, len(manifest.Namespaces))
	for _, namespace := range manifest.Namespaces {
		result[namespace.Name] = namespace.Applicable
	}
	return result
}

func manifestComponentValue(manifest visionapi.StatusParticipationManifest, path string) string {
	for _, component := range manifest.Components {
		if component.Type+".name" == path {
			return component.Name
		}
		if component.Type+".service_name" == path {
			return component.ServiceName
		}
		if component.Type+".bus_name" == path {
			return component.BusName
		}
		if component.Type+".endpoint" == path {
			return component.Endpoint
		}
	}
	return ""
}
