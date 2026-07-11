package main

import (
	"testing"

	"servicectl/internal/visionapi"
)

func TestServicectlAPIServerUsesSelectedPlane(t *testing.T) {
	previous := config
	t.Cleanup(func() { config = previous })

	config = buildConfig(false)
	if got := selectedServicectlPlane(newServicectlEventHub()).mode; got != visionapi.ModeSystem {
		t.Fatalf("system plane = %q", got)
	}

	config = buildConfig(true)
	if got := selectedServicectlPlane(newServicectlEventHub()).mode; got != visionapi.ModeUser {
		t.Fatalf("user plane = %q", got)
	}
}
