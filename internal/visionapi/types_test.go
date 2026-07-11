package visionapi

import (
	"path/filepath"
	"testing"
)

func TestSystemPropertySocketPathUsesSystemRuntimeOverride(t *testing.T) {
	t.Setenv("SERVICECTL_SYSTEM_RUNTIME_ROOT", "/tmp/servicectl-system-runtime")

	want := filepath.Join("/tmp/servicectl-system-runtime", SystemPropertySockName)
	if got := SystemPropertySocketPath(); got != want {
		t.Fatalf("SystemPropertySocketPath() = %q, want %q", got, want)
	}

	if got := PropertySocketPath(false, ""); got != want {
		t.Fatalf("PropertySocketPath(false, \"\") = %q, want %q", got, want)
	}

	if got := PropertySocketPathForMode(ModeSystem); got != want {
		t.Fatalf("PropertySocketPathForMode(system) = %q, want %q", got, want)
	}
	if got := PropertySocketPathForMode(ModeUser); got != want {
		t.Fatalf("PropertySocketPathForMode(user) = %q, want %q", got, want)
	}
}

func TestLifecycleIdentityIsComplete(t *testing.T) {
	snapshot := UnitSnapshot{
		Name:             "demo.service",
		Mode:             ModeUser,
		UID:              1000,
		MainPID:          "42",
		MainPIDStartTime: 1234,
		VisionEpoch:      "epoch-a",
		Generation:       7,
		Lifecycle:        LifecycleReady,
	}
	if snapshot.UID != 1000 || snapshot.MainPIDStartTime != 1234 || snapshot.Generation != 7 {
		t.Fatalf("incomplete snapshot: %#v", snapshot)
	}
	if KindUnitReady == KindUnitStopped || KindUnitMainPIDChanged == KindUnitReady {
		t.Fatal("lifecycle event kinds are not distinct")
	}
}

func TestRuntimeDirForUID(t *testing.T) {
	if got := RuntimeDirForUID(1000); got != "/run/user/1000/servicectl" {
		t.Fatalf("RuntimeDirForUID = %q", got)
	}
	if got := SysvisionSocketPathForUID(1000); got != "/run/user/1000/servicectl/sysvision/sysvisiond.sock" {
		t.Fatalf("SysvisionSocketPathForUID = %q", got)
	}
}

func TestWatchFilterMatchesExplicitUID(t *testing.T) {
	uid := uint32(1000)
	filter := WatchFilter{UID: &uid}
	if !filter.Matches(EventEnvelope{UID: 1000}) {
		t.Fatal("matching UID was rejected")
	}
	if filter.Matches(EventEnvelope{UID: 1001}) {
		t.Fatal("different UID was accepted")
	}
	if !(WatchFilter{}).Matches(EventEnvelope{UID: 1001}) {
		t.Fatal("unset UID filter rejected event")
	}
}
