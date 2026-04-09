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
