package main

import "testing"

func TestVersionStringDefaultsToDevelopment(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = ""

	if got := versionString(); got != "devel" {
		t.Fatalf("versionString() = %q, want devel", got)
	}
}

func TestVersionStringUsesInjectedValue(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "0.1.0"

	if got := versionString(); got != "0.1.0" {
		t.Fatalf("versionString() = %q, want 0.1.0", got)
	}
}
