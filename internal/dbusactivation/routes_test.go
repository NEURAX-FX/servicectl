package dbusactivation

import (
	"errors"
	"testing"
)

type fakeUnitResolver struct {
	explicit map[string]ManagedRoute
	byBus    map[string][]ManagedRoute
	err      error
}

func (r fakeUnitResolver) ResolveExplicit(name string) (ManagedRoute, error) {
	if r.err != nil {
		return ManagedRoute{}, r.err
	}
	route, ok := r.explicit[name]
	if !ok {
		return ManagedRoute{}, ErrUnitNotFound
	}
	return route, nil
}

func (r fakeUnitResolver) ResolveBusName(name string) ([]ManagedRoute, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]ManagedRoute(nil), r.byBus[name]...), nil
}

func TestSelectRoutePriority(t *testing.T) {
	resolver := fakeUnitResolver{
		explicit: map[string]ManagedRoute{
			"explicit.service": {Unit: "explicit"},
			"legacy": {
				Unit:        "legacy",
				ServiceName: "legacy-dbusd",
				ControlPath: "/run/managed/legacy-dbusd/control.sock",
			},
		},
		byBus: map[string][]ManagedRoute{"org.example.Service": {{Unit: "lookup"}}},
	}
	tests := []struct {
		name string
		def  ServiceDefinition
		want string
	}{
		{
			name: "systemd service wins",
			def:  ServiceDefinition{Name: "org.example.Service", SystemdService: "explicit.service", Argv: []string{"/usr/bin/servicectl", "activate-dbus", "legacy"}},
			want: "explicit",
		},
		{
			name: "legacy servicectl wins over lookup",
			def:  ServiceDefinition{Name: "org.example.Service", Argv: []string{"/usr/bin/servicectl", "activate-dbus", "legacy.service"}},
			want: "legacy",
		},
		{
			name: "bus lookup wins over native",
			def:  ServiceDefinition{Name: "org.example.Service", Argv: []string{"/bin/native"}},
			want: "lookup",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectRoute(tt.def, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != RouteManaged || got.Managed.Unit != tt.want {
				t.Fatalf("route = %#v, want managed %q", got, tt.want)
			}
			if tt.name == "legacy servicectl wins over lookup" &&
				(got.Managed.ServiceName != "legacy-dbusd" || got.Managed.ControlPath != "/run/managed/legacy-dbusd/control.sock") {
				t.Fatalf("legacy route = %#v, want resolved managed route", got.Managed)
			}
		})
	}
}

func TestSelectRouteFallsBackToNative(t *testing.T) {
	def := ServiceDefinition{Name: "org.example.Native", Argv: []string{"/usr/libexec/native", "--flag"}, User: "daemon"}
	got, err := SelectRoute(def, fakeUnitResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != RouteNative || got.Native.User != "daemon" || got.Native.Argv[0] != "/usr/libexec/native" {
		t.Fatalf("route = %#v", got)
	}
}

func TestSelectRouteRejectsAmbiguousBusLookup(t *testing.T) {
	resolver := fakeUnitResolver{byBus: map[string][]ManagedRoute{
		"org.example.Service": {{Unit: "one"}, {Unit: "two"}},
	}}
	_, err := SelectRoute(ServiceDefinition{Name: "org.example.Service", Argv: []string{"/bin/true"}}, resolver)
	if !errors.Is(err, ErrAmbiguousUnit) {
		t.Fatalf("error = %v, want ErrAmbiguousUnit", err)
	}
}

func TestLegacyRouteRequiresExactCommandShape(t *testing.T) {
	bad := [][]string{
		{"servicectl", "activate-dbus", "unit"},
		{"/usr/bin/servicectl", "activate-dbus", "unit", "extra"},
		{"/usr/bin/servicectl", "start", "unit"},
		{"/bin/sh", "-c", "/usr/bin/servicectl activate-dbus unit"},
	}
	for _, argv := range bad {
		if _, ok := LegacyManagedRoute(argv); ok {
			t.Fatalf("LegacyManagedRoute(%#v) unexpectedly matched", argv)
		}
	}
	for _, path := range []string{"/usr/bin/servicectl", "/usr/local/bin/servicectl"} {
		got, ok := LegacyManagedRoute([]string{path, "activate-dbus", "unit.service"})
		if !ok || got.Unit != "unit" {
			t.Fatalf("LegacyManagedRoute(%q) = %#v, %v", path, got, ok)
		}
	}
}
