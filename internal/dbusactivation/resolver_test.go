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

func TestSystemdUnitResolverDeduplicatesSymlinkAlias(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "systemd-localed.service", "notify", "org.freedesktop.locale1")
	if err := os.Symlink("systemd-localed.service", filepath.Join(dir, "dbus-org.freedesktop.locale1.service")); err != nil {
		t.Fatal(err)
	}
	resolver := NewSystemdUnitResolver([]string{dir}, "/run/managed")

	matches, err := resolver.ResolveBusName("org.freedesktop.locale1")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Unit != "systemd-localed" {
		t.Fatalf("matches = %#v, want canonical systemd-localed route", matches)
	}
}

func TestSystemdUnitResolverResolvesExplicitSymlinkAliasToCanonicalUnit(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "systemd-timedated.service", "notify", "org.freedesktop.timedate1")
	if err := os.Symlink("systemd-timedated.service", filepath.Join(dir, "dbus-org.freedesktop.timedate1.service")); err != nil {
		t.Fatal(err)
	}
	resolver := NewSystemdUnitResolver([]string{dir}, "/run/managed")

	// SystemdService= in a D-Bus service file usually names the dbus-<busname> alias symlink. Routing it
	// under the alias name would create a second managed service for a unit that ResolveBusName already
	// tracks under its canonical name.
	alias, err := resolver.ResolveExplicit("dbus-org.freedesktop.timedate1.service")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := resolver.ResolveExplicit("systemd-timedated.service")
	if err != nil {
		t.Fatal(err)
	}
	if alias != canonical {
		t.Fatalf("alias route = %#v, canonical route = %#v, want identical", alias, canonical)
	}
	if alias.Unit != "systemd-timedated" {
		t.Fatalf("alias route unit = %q, want systemd-timedated", alias.Unit)
	}
}

func TestSystemdUnitResolverKeepsExplicitUnitWhenSymlinkEscapesSearchPath(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeUnit(t, outside, "hidden.service", "notify", "org.example.Hidden")
	if err := os.Symlink(filepath.Join(outside, "hidden.service"), filepath.Join(dir, "visible.service")); err != nil {
		t.Fatal(err)
	}
	resolver := NewSystemdUnitResolver([]string{dir}, "/run/managed")

	// Following a symlink out of the search path would route to a unit servicectl cannot load, so the
	// requested name has to win.
	route, err := resolver.ResolveExplicit("visible.service")
	if err != nil {
		t.Fatal(err)
	}
	if route.Unit != "visible" {
		t.Fatalf("route unit = %q, want visible", route.Unit)
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
