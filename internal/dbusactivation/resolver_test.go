package dbusactivation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSystemdUnitResolverBuildsDbusdWrapperRoute(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "systemd-localed.service", "notify", "org.freedesktop.locale1")
	resolver := NewSystemdUnitResolver([]string{dir}, "/run/servicectl/managed")

	route, err := resolver.ResolveExplicit("systemd-localed.service")
	if err != nil {
		t.Fatal(err)
	}
	if route.Unit != "systemd-localed" || route.ServiceName != "systemd-localed-dbusd" {
		t.Fatalf("route = %#v", route)
	}
	wantControl := "/run/servicectl/managed/systemd-localed-dbusd/control.sock"
	if route.ControlPath != wantControl {
		t.Fatalf("control path = %q, want %q", route.ControlPath, wantControl)
	}
}

func TestSystemdUnitResolverFindsBusNameAcrossPaths(t *testing.T) {
	high := t.TempDir()
	low := t.TempDir()
	writeUnit(t, low, "one.service", "dbus", "org.example.One")
	writeUnit(t, high, "two.service", "notify", "org.example.Two")
	resolver := NewSystemdUnitResolver([]string{high, low}, "/run/managed")

	matches, err := resolver.ResolveBusName("org.example.One")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Unit != "one" {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestSystemdUnitResolverReportsAmbiguousBusName(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "one.service", "dbus", "org.example.Shared")
	writeUnit(t, dir, "two.service", "notify", "org.example.Shared")
	resolver := NewSystemdUnitResolver([]string{dir}, "/run/managed")

	matches, err := resolver.ResolveBusName("org.example.Shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %#v, want 2", matches)
	}
}

func TestSystemdUnitResolverRejectsPathTraversal(t *testing.T) {
	resolver := NewSystemdUnitResolver([]string{t.TempDir()}, "/run/managed")
	for _, name := range []string{"../outside.service", "nested/unit.service", ".service"} {
		if _, err := resolver.ResolveExplicit(name); !errors.Is(err, ErrUnitNotFound) {
			t.Fatalf("ResolveExplicit(%q) error = %v, want ErrUnitNotFound", name, err)
		}
	}
}

func TestDefinitionResolverSelectsRouteFromIndex(t *testing.T) {
	dir := t.TempDir()
	writeServiceDefinition(t, dir, "service.service", "org.example.Service", "/bin/native")
	index, errs := BuildIndex([]string{dir})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	unitDir := t.TempDir()
	writeUnit(t, unitDir, "managed.service", "notify", "org.example.Service")
	resolver := DefinitionResolver{
		Index: func() *Index { return index },
		Units: NewSystemdUnitResolver([]string{unitDir}, "/run/managed"),
	}
	route, err := resolver.Resolve("org.example.Service")
	if err != nil {
		t.Fatal(err)
	}
	if route.Kind != RouteManaged || route.Managed.ServiceName != "managed-dbusd" {
		t.Fatalf("route = %#v", route)
	}
}

func writeUnit(t *testing.T, dir, name, serviceType, busName string) {
	t.Helper()
	content := "[Service]\nType=" + serviceType + "\nBusName=" + busName + "\nExecStart=/bin/true\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
