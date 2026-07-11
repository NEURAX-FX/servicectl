package dbusactivation

import (
	"fmt"
	"strings"
	"testing"
)

func TestEnvironmentFiltering(t *testing.T) {
	got, err := FilterEnvironment([]string{
		"LANG=C.UTF-8",
		"LANGUAGE=en_US:en",
		"LC_TIME=C",
		"TZ=UTC",
		"PYTHONPATH=/tmp/python",
		"PERL5LIB=/tmp/perl",
		"PATH=/usr/bin",
		"DBUS_STARTER_ADDRESS=unix:path=/run/dbus/system_bus_socket",
		"LD_PRELOAD=/tmp/evil.so",
		"LD_LIBRARY_PATH=/tmp/lib",
		"LD_AUDIT=evil.so",
		"GCONV_PATH=/tmp/gconv",
		"GLIBC_TUNABLES=x=y",
		"MALLOC_TRACE=/tmp/trace",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/session",
		"DBUS_SESSION_BUS_PID=123",
		"DBUS_SESSION_BUS_WINDOWID=456",
		"DISPLAY=:0",
		"WAYLAND_DISPLAY=wayland-0",
		"XAUTHORITY=/tmp/xauthority",
		"SSH_AUTH_SOCK=/tmp/agent",
		"=bad",
		"BAD\x00NAME=value",
		"NOVALUE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["LANG"] != "C.UTF-8" || got["LANGUAGE"] != "en_US:en" || got["LC_TIME"] != "C" || got["TZ"] != "UTC" || got["PATH"] != "/usr/bin" {
		t.Fatalf("kept environment = %#v", got)
	}
	for _, key := range []string{"PYTHONPATH", "PERL5LIB", "LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "GCONV_PATH", "GLIBC_TUNABLES", "MALLOC_TRACE", "DBUS_SESSION_BUS_ADDRESS", "DBUS_SESSION_BUS_PID", "DBUS_SESSION_BUS_WINDOWID", "DBUS_STARTER_ADDRESS", "DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "SSH_AUTH_SOCK", ""} {
		if _, exists := got[key]; exists {
			t.Fatalf("dangerous variable %q was retained", key)
		}
	}
}

func TestEnvironmentLimits(t *testing.T) {
	tooMany := make([]string, 257)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("KEY_%03d=value", i)
	}
	if _, err := FilterEnvironment(tooMany); err == nil {
		t.Fatal("accepted more than 256 entries")
	}
	if _, err := FilterEnvironment([]string{"BIG=" + strings.Repeat("x", MaxEnvironmentValue+1)}); err == nil {
		t.Fatal("accepted oversized value")
	}
	large := make([]string, 5)
	for i := range large {
		large[i] = fmt.Sprintf("KEY%d=%s", i, strings.Repeat("x", 7000))
	}
	if _, err := FilterEnvironment(large); err == nil {
		t.Fatal("accepted oversized total environment")
	}
}

func TestEnvironmentStoreGenerationsAndCopies(t *testing.T) {
	var store EnvironmentStore
	first := store.Replace(FrontendDaemonHelper, map[string]string{"LANG": "C"})
	second := store.Replace(FrontendDaemonHelper, map[string]string{"LANG": "C.UTF-8"})
	if first != 1 || second != 2 {
		t.Fatalf("generations = %d, %d", first, second)
	}
	generation, values := store.Snapshot(FrontendDaemonHelper)
	if generation != 2 || len(values) != 1 || values[0] != "LANG=C.UTF-8" {
		t.Fatalf("snapshot = %d, %#v", generation, values)
	}
	values[0] = "mutated"
	_, again := store.Snapshot(FrontendDaemonHelper)
	if again[0] != "LANG=C.UTF-8" {
		t.Fatalf("store was mutated: %#v", again)
	}
	if generation, values := store.Snapshot(FrontendAdmin); generation != 0 || values != nil {
		t.Fatalf("unexpected admin snapshot = %d, %#v", generation, values)
	}
}
