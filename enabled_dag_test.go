package main

import "testing"

func lookupUnitFromMap(units map[string]*Unit) func(string) (*Unit, error) {
	return func(name string) (*Unit, error) {
		if unit, ok := units[name]; ok {
			return unit, nil
		}
		return nil, nil
	}
}

func indexOf(values []string, target string) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}

func TestBuildEnabledServiceDAG_LooseEdgesAndProjection(t *testing.T) {
	roots := enabledRootSet{
		Standalone: []string{"pipewire.service"},
		Groups: map[string][]string{
			"pipewire": {"pipewire.service", "pipewire-pulse.service", "wireplumber.service"},
		},
	}

	units := map[string]*Unit{
		"wireplumber.service": {
			Name: "wireplumber",
		},
		"pipewire.service": {
			Name:  "pipewire",
			After: []string{"wireplumber.service"},
			Wants: []string{"wireplumber.service"},
		},
		"pipewire-pulse.service": {
			Name:  "pipewire-pulse",
			After: []string{"pipewire.service"},
			Wants: []string{"pipewire.service"},
		},
	}

	graph, err := buildEnabledServiceDAG(roots, lookupUnitFromMap(units))
	if err != nil {
		t.Fatalf("buildEnabledServiceDAG error: %v", err)
	}

	order, err := graph.TopologicalServices()
	if err != nil {
		t.Fatalf("TopologicalServices error: %v", err)
	}

	idxWire := indexOf(order, "wireplumber.service")
	idxPipe := indexOf(order, "pipewire.service")
	idxPulse := indexOf(order, "pipewire-pulse.service")
	if idxWire < 0 || idxPipe < 0 || idxPulse < 0 {
		t.Fatalf("topological order missing expected services: %v", order)
	}
	if !(idxWire < idxPipe && idxPipe < idxPulse) {
		t.Fatalf("unexpected order %v", order)
	}

	if got := graph.OwnerOf("pipewire.service"); got != "pipewire-orchestrd" {
		t.Fatalf("OwnerOf(pipewire.service) = %q, want %q", got, "pipewire-orchestrd")
	}

	if got := graph.OwnerOf("pipewire-pulse.service"); got != s6GroupOrchestrdServiceName("pipewire") {
		t.Fatalf("OwnerOf(pipewire-pulse.service) = %q, want %q", got, s6GroupOrchestrdServiceName("pipewire"))
	}

	deps := graph.ProjectOrchestrdDependencies()
	groupOwner := s6GroupOrchestrdServiceName("pipewire")
	if !deps[groupOwner]["pipewire-orchestrd"] {
		t.Fatalf("missing projected dependency %s -> pipewire-orchestrd: %#v", groupOwner, deps)
	}
}

func TestUniqueLinesPreserveOrder(t *testing.T) {
	lines := uniqueLinesPreserveOrder("b\na\nb\n c \n\n#ignored?\n")
	if len(lines) != 4 {
		t.Fatalf("len(lines) = %d, want 4 (%v)", len(lines), lines)
	}
	if lines[0] != "b" || lines[1] != "a" || lines[2] != "c" || lines[3] != "#ignored?" {
		t.Fatalf("unexpected lines: %v", lines)
	}
}

func TestProjectOrchestrdDependenciesIgnoresUnownedTransitiveServices(t *testing.T) {
	roots := enabledRootSet{
		Standalone: []string{},
		Groups: map[string][]string{
			"pipewire": {"pipewire-pulse.service"},
		},
	}

	units := map[string]*Unit{
		"pipewire-pulse.service": {
			Name:  "pipewire-pulse",
			Wants: []string{"dbus.service"},
		},
		"dbus.service": {
			Name: "dbus",
		},
	}

	graph, err := buildEnabledServiceDAG(roots, lookupUnitFromMap(units))
	if err != nil {
		t.Fatalf("buildEnabledServiceDAG error: %v", err)
	}

	deps := graph.ProjectOrchestrdDependencies()
	groupOwner := s6GroupOrchestrdServiceName("pipewire")
	if deps[groupOwner]["dbus-orchestrd"] {
		t.Fatalf("unexpected projected dependency to unowned transitive service: %#v", deps)
	}
}
