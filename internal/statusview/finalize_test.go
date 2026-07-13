package statusview

import (
	"reflect"
	"strings"
	"testing"
)

func TestFinalizeValidPrimaryPath(t *testing.T) {
	model := topologyModel(
		[]Node{
			healthyNode("service:system:demo.service", "service"),
			healthyNode("s6:system:system", "s6"),
			healthyNode("sys-orchestrd:system:demo.service", "sys-orchestrd"),
			healthyNode("sys-cgroupd:system:system", "sys-cgroupd"),
		},
		[]Edge{
			{From: "s6:system:system", To: "sys-orchestrd:system:demo.service", Relation: RelationSupervises, Primary: true},
			{From: "sys-orchestrd:system:demo.service", To: "service:system:demo.service", Relation: RelationActivates, Primary: true},
			{From: "sys-orchestrd:system:demo.service", To: "sys-cgroupd:system:system", Relation: RelationAccounts},
		},
	)

	got, err := Finalize(model)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if got.Summary.AggregateHealth != HealthHealthy {
		t.Fatalf("aggregate health = %q, want healthy", got.Summary.AggregateHealth)
	}
	if got.Summary.DisplayState != "active" {
		t.Fatalf("display state = %q, want active", got.Summary.DisplayState)
	}
}

func TestFinalizeValidServiceOnlyPath(t *testing.T) {
	model := topologyModel(
		[]Node{healthyNode("service:system:demo.service", "service")},
		nil,
	)

	if _, err := Finalize(model); err != nil {
		t.Fatalf("Finalize service-only topology: %v", err)
	}
}

func TestFinalizeRejectsInvalidGraph(t *testing.T) {
	tests := []struct {
		name  string
		nodes []Node
		edges []Edge
		want  string
	}{
		{
			name:  "missing endpoint",
			nodes: []Node{healthyNode("service:system:demo.service", "service")},
			edges: []Edge{{From: "missing:system:x", To: "service:system:demo.service", Relation: RelationControls, Primary: true}},
			want:  "missing endpoint",
		},
		{
			name: "two primary roots",
			nodes: []Node{
				healthyNode("service:system:demo.service", "service"),
				healthyNode("manager:system:a", "manager"),
				healthyNode("manager:system:b", "manager"),
			},
			edges: []Edge{
				{From: "manager:system:a", To: "service:system:demo.service", Relation: RelationControls, Primary: true},
				{From: "manager:system:b", To: "service:system:demo.service", Relation: RelationControls, Primary: true},
			},
			want: "incoming primary edges",
		},
		{
			name: "disconnected primary edge",
			nodes: []Node{
				healthyNode("service:system:demo.service", "service"),
				healthyNode("manager:system:a", "manager"),
				healthyNode("observer:system:a", "observer"),
				healthyNode("observer:system:b", "observer"),
			},
			edges: []Edge{
				{From: "manager:system:a", To: "service:system:demo.service", Relation: RelationControls, Primary: true},
				{From: "observer:system:a", To: "observer:system:b", Relation: RelationObserves, Primary: true},
			},
			want: "primary roots",
		},
		{
			name: "two outgoing primary edges",
			nodes: []Node{
				healthyNode("service:system:demo.service", "service"),
				healthyNode("manager:system:a", "manager"),
				healthyNode("helper:system:a", "helper"),
			},
			edges: []Edge{
				{From: "manager:system:a", To: "service:system:demo.service", Relation: RelationControls, Primary: true},
				{From: "manager:system:a", To: "helper:system:a", Relation: RelationActivates, Primary: true},
			},
			want: "outgoing primary edges",
		},
		{
			name: "primary cycle",
			nodes: []Node{
				healthyNode("service:system:demo.service", "service"),
				healthyNode("manager:system:a", "manager"),
			},
			edges: []Edge{
				{From: "manager:system:a", To: "service:system:demo.service", Relation: RelationControls, Primary: true},
				{From: "service:system:demo.service", To: "manager:system:a", Relation: RelationObserves, Primary: true},
			},
			want: "primary root",
		},
		{
			name: "primary path does not end at service",
			nodes: []Node{
				healthyNode("service:system:demo.service", "service"),
				healthyNode("manager:system:a", "manager"),
			},
			edges: []Edge{{From: "service:system:demo.service", To: "manager:system:a", Relation: RelationControls, Primary: true}},
			want:  "terminal node",
		},
		{
			name: "duplicate node id",
			nodes: []Node{
				healthyNode("service:system:demo.service", "service"),
				healthyNode("service:system:demo.service", "service"),
			},
			want: "duplicate node ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := topologyModel(tt.nodes, tt.edges)
			before := cloneModelForTest(model)
			_, err := Finalize(model)
			if err == nil {
				t.Fatal("Finalize succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Finalize error = %q, want substring %q", err, tt.want)
			}
			if !reflect.DeepEqual(model, before) {
				t.Fatal("Finalize mutated caller model on failure")
			}
		})
	}
}

func TestFinalizeAllowsNonPrimaryCycle(t *testing.T) {
	model := topologyModel(
		[]Node{
			healthyNode("service:system:demo.service", "service"),
			healthyNode("observer:system:a", "observer"),
			healthyNode("observer:system:b", "observer"),
		},
		[]Edge{
			{From: "observer:system:a", To: "observer:system:b", Relation: RelationObserves},
			{From: "observer:system:b", To: "observer:system:a", Relation: RelationObserves},
		},
	)

	if _, err := Finalize(model); err != nil {
		t.Fatalf("Finalize non-primary cycle: %v", err)
	}
}

func TestFinalizeSortsDeterministically(t *testing.T) {
	nodes := []Node{
		healthyNode("service:system:demo.service", "service"),
		healthyNode("zeta:system:z", "zeta"),
		healthyNode("alpha:user@1000:b", "alpha"),
		healthyNode("alpha:system:a", "alpha"),
	}
	edges := []Edge{
		{From: "zeta:system:z", To: "alpha:user@1000:b", Relation: RelationObserves},
		{From: "zeta:system:z", To: "alpha:system:a", Relation: RelationAccounts},
	}
	first, err := Finalize(topologyModel(nodes, edges))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Finalize(topologyModel(reverseNodes(nodes), reverseEdges(edges)))
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(first.Orchestration, second.Orchestration) {
		t.Fatalf("ordering depends on input\nfirst: %#v\nsecond: %#v", first.Orchestration, second.Orchestration)
	}
	wantNodeIDs := []string{
		"alpha:system:a",
		"alpha:user@1000:b",
		"service:system:demo.service",
		"zeta:system:z",
	}
	for i, want := range wantNodeIDs {
		if first.Orchestration.Nodes[i].ID != want {
			t.Fatalf("node %d = %q, want %q", i, first.Orchestration.Nodes[i].ID, want)
		}
	}
	if first.Orchestration.Edges[0].Relation != RelationAccounts {
		t.Fatalf("first edge relation = %q, want accounts", first.Orchestration.Edges[0].Relation)
	}
}

func TestAggregateHealthPriority(t *testing.T) {
	tests := []struct {
		name        string
		runtime     RuntimeState
		component   Health
		diagnostic  Diagnostic
		wantOrch    Health
		wantOverall Health
		wantDisplay string
	}{
		{
			name:        "healthy",
			runtime:     RuntimeActive,
			component:   HealthHealthy,
			wantOrch:    HealthHealthy,
			wantOverall: HealthHealthy,
			wantDisplay: "active",
		},
		{
			name:        "unknown",
			runtime:     RuntimeActive,
			component:   HealthUnknown,
			wantOrch:    HealthUnknown,
			wantOverall: HealthUnknown,
			wantDisplay: "active (unknown)",
		},
		{
			name:        "degraded outranks unknown diagnostic",
			runtime:     RuntimeActive,
			component:   HealthDegraded,
			diagnostic:  healthDiagnostic(DomainOrchestration, SeverityUnknown),
			wantOrch:    HealthDegraded,
			wantOverall: HealthDegraded,
			wantDisplay: "active (degraded)",
		},
		{
			name:        "failed runtime outranks degraded",
			runtime:     RuntimeFailed,
			component:   HealthDegraded,
			wantOrch:    HealthDegraded,
			wantOverall: HealthFailed,
			wantDisplay: "failed",
		},
		{
			name:        "runtime failed diagnostic does not promote active",
			runtime:     RuntimeActive,
			component:   HealthHealthy,
			diagnostic:  healthDiagnostic(DomainRuntime, SeverityFailed),
			wantOrch:    HealthHealthy,
			wantOverall: HealthHealthy,
			wantDisplay: "active",
		},
		{
			name:        "runtime unknown diagnostic affects aggregate only",
			runtime:     RuntimeActive,
			component:   HealthHealthy,
			diagnostic:  healthDiagnostic(DomainRuntime, SeverityUnknown),
			wantOrch:    HealthHealthy,
			wantOverall: HealthUnknown,
			wantDisplay: "active (unknown)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := healthyNode("service:system:demo.service", "service")
			component := healthyNode("manager:system:system", "manager")
			component.Health = tt.component
			model := topologyModel([]Node{service, component}, nil)
			model.Summary.RuntimeState = tt.runtime
			if tt.diagnostic.Code != "" {
				model.Diagnostics = append(model.Diagnostics, tt.diagnostic)
			}

			got, err := Finalize(model)
			if err != nil {
				t.Fatal(err)
			}
			if got.Summary.OrchestrationHealth != tt.wantOrch {
				t.Fatalf("orchestration health = %q, want %q", got.Summary.OrchestrationHealth, tt.wantOrch)
			}
			if got.Summary.AggregateHealth != tt.wantOverall {
				t.Fatalf("aggregate health = %q, want %q", got.Summary.AggregateHealth, tt.wantOverall)
			}
			if got.Summary.DisplayState != tt.wantDisplay {
				t.Fatalf("display state = %q, want %q", got.Summary.DisplayState, tt.wantDisplay)
			}
			if len(got.Diagnostics) != len(model.Diagnostics) {
				t.Fatalf("diagnostics count = %d, want %d", len(got.Diagnostics), len(model.Diagnostics))
			}
		})
	}
}

func TestAggregateLifecycleStateHealth(t *testing.T) {
	states := []RuntimeState{RuntimeActive, RuntimeInactive, RuntimeActivating, RuntimeDeactivating}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			model := topologyModel([]Node{healthyNode("service:system:demo.service", "service")}, nil)
			model.Summary.RuntimeState = state
			got, err := Finalize(model)
			if err != nil {
				t.Fatal(err)
			}
			if got.Orchestration.Nodes[0].Health != HealthHealthy {
				t.Fatalf("service health = %q, want healthy", got.Orchestration.Nodes[0].Health)
			}
			if got.Summary.AggregateHealth != HealthHealthy {
				t.Fatalf("aggregate health = %q, want healthy", got.Summary.AggregateHealth)
			}
		})
	}
}

func TestDisplayStateFailedNeverIncludesSuffix(t *testing.T) {
	service := healthyNode("service:system:demo.service", "service")
	component := healthyNode("manager:system:system", "manager")
	component.Health = HealthDegraded
	model := topologyModel([]Node{service, component}, nil)
	model.Summary.RuntimeState = RuntimeFailed

	got, err := Finalize(model)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.DisplayState != "failed" {
		t.Fatalf("display state = %q, want failed", got.Summary.DisplayState)
	}
}

func TestExitCode(t *testing.T) {
	for _, tt := range []struct {
		health Health
		want   int
	}{
		{health: HealthHealthy, want: 0},
		{health: HealthFailed, want: 3},
		{health: HealthDegraded, want: 3},
		{health: HealthUnknown, want: 3},
	} {
		model := NewModel()
		model.Summary.AggregateHealth = tt.health
		if got := ExitCode(model); got != tt.want {
			t.Fatalf("ExitCode(%q) = %d, want %d", tt.health, got, tt.want)
		}
	}
}

func TestFinalizePreservesRequiredEmptyArrays(t *testing.T) {
	model := NewModel()
	model.Orchestration.Nodes = []Node{
		healthyNode("service:system:demo.service", "service"),
	}

	finalized, err := Finalize(model)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Orchestration.Nodes == nil || finalized.Orchestration.Edges == nil || finalized.Diagnostics == nil || finalized.Logs == nil {
		t.Fatalf("required arrays = %#v", finalized)
	}
}

func topologyModel(nodes []Node, edges []Edge) Model {
	model := NewModel()
	model.Summary.RuntimeState = RuntimeActive
	model.Orchestration.Nodes = append(model.Orchestration.Nodes, nodes...)
	model.Orchestration.Edges = append(model.Orchestration.Edges, edges...)
	return model
}

func healthyNode(id, typeName string) Node {
	node := NewNode(id, typeName, id, scopeFromID(id))
	node.Health = HealthHealthy
	node.State = "active"
	node.Expected = true
	return node
}

func scopeFromID(id string) string {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) != 3 {
		return "system"
	}
	return parts[1]
}

func healthDiagnostic(domain DiagnosticDomain, severity DiagnosticSeverity) Diagnostic {
	return Diagnostic{
		Severity:      severity,
		Domain:        domain,
		Code:          "test_diagnostic",
		Message:       "test diagnostic",
		AffectsHealth: true,
	}
}

func reverseNodes(in []Node) []Node {
	out := append([]Node(nil), in...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func reverseEdges(in []Edge) []Edge {
	out := append([]Edge(nil), in...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func cloneModelForTest(in Model) Model {
	out := in
	out.Orchestration.Nodes = make([]Node, len(in.Orchestration.Nodes))
	copy(out.Orchestration.Nodes, in.Orchestration.Nodes)
	out.Orchestration.Edges = make([]Edge, len(in.Orchestration.Edges))
	copy(out.Orchestration.Edges, in.Orchestration.Edges)
	out.Diagnostics = make([]Diagnostic, len(in.Diagnostics))
	copy(out.Diagnostics, in.Diagnostics)
	out.Logs = make([]LogEntry, len(in.Logs))
	copy(out.Logs, in.Logs)
	return out
}
