package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestS6PathsForModeAreDistinct(t *testing.T) {
	systemPaths := s6PathsForMode("system")
	userPaths := s6PathsForMode("user")

	if systemPaths.SourceRoot == userPaths.SourceRoot {
		t.Fatalf("SourceRoot should differ by mode: %q", systemPaths.SourceRoot)
	}
	if systemPaths.BundleContents == userPaths.BundleContents {
		t.Fatalf("BundleContents should differ by mode: %q", systemPaths.BundleContents)
	}
	if systemPaths.LiveDir == userPaths.LiveDir {
		t.Fatalf("LiveDir should differ by mode: %q", systemPaths.LiveDir)
	}
}

func TestBuildEnabledServiceDAGSkipsMissingTransitiveDependency(t *testing.T) {
	roots := enabledRootSet{Standalone: []string{"app.service"}, Groups: map[string][]string{}}

	lookup := func(name string) (*Unit, error) {
		switch strings.TrimSpace(name) {
		case "app.service":
			return &Unit{Name: "app", Wants: []string{"optional-missing.service"}}, nil
		case "optional-missing.service":
			return nil, fmt.Errorf("unit %s not found", name)
		default:
			return nil, nil
		}
	}

	graph, err := buildEnabledServiceDAG(roots, lookup)
	if err != nil {
		t.Fatalf("buildEnabledServiceDAG returned unexpected error: %v", err)
	}

	order, err := graph.TopologicalServices()
	if err != nil {
		t.Fatalf("TopologicalServices returned unexpected error: %v", err)
	}

	if indexOf(order, "app.service") < 0 {
		t.Fatalf("order missing root unit: %v", order)
	}
}

func TestBuildEnabledServiceDAGSkipsMissingExplicitRoot(t *testing.T) {
	roots := enabledRootSet{Standalone: []string{"missing-root.service"}, Groups: map[string][]string{}}

	lookup := func(name string) (*Unit, error) {
		if strings.TrimSpace(name) == "missing-root.service" {
			return nil, fmt.Errorf("unit %s not found", name)
		}
		return nil, nil
	}

	graph, err := buildEnabledServiceDAG(roots, lookup)
	if err != nil {
		t.Fatalf("buildEnabledServiceDAG returned unexpected error: %v", err)
	}
	order, err := graph.TopologicalServices()
	if err != nil {
		t.Fatalf("TopologicalServices returned unexpected error: %v", err)
	}
	if len(order) != 1 || order[0] != "missing-root.service" {
		t.Fatalf("unexpected topological order for missing root: %v", order)
	}
}

func TestUserActivationGateReady(t *testing.T) {
	metaReady := sysvisionMetaResponse{SystemServicectlEventsConnected: true}
	if !userActivationGateReady(true, metaReady, false, false) {
		t.Fatal("expected gate to be ready when sysvision meta says system bus connected")
	}

	metaNotReady := sysvisionMetaResponse{SystemServicectlEventsConnected: false}
	if userActivationGateReady(true, metaNotReady, true, true) {
		t.Fatal("expected gate to be blocked when sysvision meta says system bus disconnected")
	}

	if !userActivationGateReady(false, sysvisionMetaResponse{}, true, true) {
		t.Fatal("expected fallback socket check to allow activation when sockets exist")
	}

	if userActivationGateReady(false, sysvisionMetaResponse{}, true, false) {
		t.Fatal("expected fallback socket check to block activation when socket missing")
	}
}
