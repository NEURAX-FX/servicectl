package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"servicectl/internal/statusview"
)

func TestRenderStatusTerminalGolden(t *testing.T) {
	tests := []struct {
		name    string
		width   int
		verbose bool
		model   statusview.Model
		golden  string
	}{
		{name: "healthy wide", width: 96, model: renderTestStatusModel(t, statusview.HealthHealthy), golden: "testdata/status/healthy-wide.golden"},
		{name: "degraded wide", width: 96, model: renderTestStatusModel(t, statusview.HealthDegraded), golden: "testdata/status/degraded-wide.golden"},
		{name: "unknown narrow", width: 95, model: renderTestStatusModel(t, statusview.HealthUnknown), golden: "testdata/status/unknown-narrow.golden"},
		{name: "failed narrow", width: 72, model: renderTestFailedStatusModel(t), golden: "testdata/status/failed-narrow.golden"},
		{name: "user buses wide", width: 100, verbose: true, model: renderTestUserBusModel(t), golden: "testdata/status/user-buses-wide.golden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := renderStatusTerminalV2(&output, tt.model, renderCapabilities{Width: tt.width}, tt.verbose); err != nil {
				t.Fatal(err)
			}
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(tt.golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(tt.golden, output.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(tt.golden)
			if err != nil {
				t.Fatal(err)
			}
			if output.String() != string(want) {
				t.Fatalf("output mismatch\n--- got ---\n%s--- want ---\n%s", output.String(), want)
			}
		})
	}
}

func TestRenderStatusTerminalWidth(t *testing.T) {
	model := renderTestStatusModel(t, statusview.HealthUnknown)
	model.Identity.Description = "A long 雪 service description that must wrap without losing diagnostic context"
	model.Diagnostics[0].Message = "The declared observation component timed out while collecting a deliberately long diagnostic message."
	for _, width := range []int{44, 95, 96, 120} {
		var output bytes.Buffer
		if err := renderStatusTerminalV2(&output, model, renderCapabilities{Width: width}, false); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
			if visibleWidth(line) > width {
				t.Fatalf("width %d: line width %d: %q", width, visibleWidth(line), line)
			}
		}
		if !strings.Contains(strings.Join(strings.Fields(output.String()), " "), "deliberately long diagnostic message") {
			t.Fatalf("width %d lost diagnostic text:\n%s", width, output.String())
		}
	}
}

func TestRenderStatusTerminalColor(t *testing.T) {
	for _, health := range []statusview.Health{statusview.HealthHealthy, statusview.HealthDegraded, statusview.HealthUnknown} {
		model := renderTestStatusModel(t, health)
		var colored bytes.Buffer
		if err := renderStatusTerminalV2(&colored, model, renderCapabilities{Width: 96, Color: true}, false); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(colored.String(), "\x1b[") || !strings.Contains(colored.String(), string(health)) {
			t.Fatalf("health %s missing semantic color or label:\n%s", health, colored.String())
		}
		var plain bytes.Buffer
		if err := renderStatusTerminalV2(&plain, model, renderCapabilities{Width: 96, Color: false}, false); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(plain.String(), "\x1b[") || !strings.Contains(plain.String(), string(health)) {
			t.Fatalf("health %s plain output is ambiguous:\n%s", health, plain.String())
		}
	}
}

func TestRenderStatusTerminalColorsOnlyTheSummary(t *testing.T) {
	model := renderTestStatusModel(t, statusview.HealthHealthy)
	var output bytes.Buffer
	if err := renderStatusTerminalV2(&output, model, renderCapabilities{Width: 96, Color: true}, false); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.Contains(got, "\x1b[32m●\x1b[0m \x1b[32mactive\x1b[0m") {
		t.Fatalf("summary does not color the status icon and state:\n%s", got)
	}
	for _, section := range []string{"ORCHESTRATION", "RECENT LOGS"} {
		line := statusRenderLineContaining(got, section)
		if strings.Contains(line, "\x1b[") {
			t.Fatalf("section heading %q is colored: %q", section, line)
		}
		if !strings.Contains(line, "┄") || visibleWidth(line) != 96 {
			t.Fatalf("section heading %q = %q, want a full-width dashed divider", section, line)
		}
	}
	sectionStart := strings.Index(got, "ORCHESTRATION")
	if sectionStart < 0 {
		t.Fatal("missing orchestration section")
	}
	if strings.Contains(got[sectionStart:], "\x1b[") {
		t.Fatalf("non-summary content is colored:\n%s", got[sectionStart:])
	}
}

func statusRenderLineContaining(output, needle string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func TestRenderStatusTerminalUsesWideThreshold(t *testing.T) {
	model := renderTestStatusModel(t, statusview.HealthHealthy)
	for _, tt := range []struct {
		width    int
		wantWide bool
	}{{95, false}, {96, true}} {
		var output bytes.Buffer
		if err := renderStatusTerminalV2(&output, model, renderCapabilities{Width: tt.width}, false); err != nil {
			t.Fatal(err)
		}
		wide := strings.Contains(output.String(), "├──")
		if wide != tt.wantWide {
			t.Fatalf("width %d wide=%v, want %v:\n%s", tt.width, wide, tt.wantWide, output.String())
		}
	}
}

func TestRenderStatusPreservesSideRelationBetweenPrimaryNodes(t *testing.T) {
	model := renderTestStatusModel(t, statusview.HealthHealthy)
	from := "s6:system:system"
	to := "service:system:demo.service"
	model.Orchestration.Edges = append(model.Orchestration.Edges, statusview.Edge{From: from, To: to, Relation: statusview.RelationActivates})
	finalized, err := statusview.Finalize(model)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := renderStatusTerminalV2(&output, finalized, renderCapabilities{Width: 96}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "activates") || !strings.Contains(output.String(), "see demo.service") {
		t.Fatalf("side relation between primary nodes was omitted:\n%s", output.String())
	}
}

func TestStyleStatusSemanticTextUsesWholeWords(t *testing.T) {
	caps := renderCapabilities{Color: true}
	got := styleStatusSemanticText("inactive active deactivating", caps)
	if strings.Contains(got, "in\x1b[") {
		t.Fatalf("inactive was partially styled: %q", got)
	}
	if strings.Count(got, "\x1b[") != 4 {
		t.Fatalf("semantic styling count = %d, want 4: %q", strings.Count(got, "\x1b["), got)
	}
}

func TestRenderStatusPlainGolden(t *testing.T) {
	tests := []struct {
		name    string
		verbose bool
		model   statusview.Model
		golden  string
	}{
		{name: "degraded", model: renderTestStatusModel(t, statusview.HealthDegraded), golden: "testdata/status/degraded-plain.golden"},
		{name: "verbose", verbose: true, model: renderTestUserBusModel(t), golden: "testdata/status/verbose-plain.golden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := renderStatusPlainV2(&output, tt.model, tt.verbose); err != nil {
				t.Fatal(err)
			}
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(tt.golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(tt.golden, output.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(tt.golden)
			if err != nil {
				t.Fatal(err)
			}
			if output.String() != string(want) {
				t.Fatalf("output mismatch\n--- got ---\n%s--- want ---\n%s", output.String(), want)
			}
		})
	}
}

func TestStatusPlainStructureIsASCII(t *testing.T) {
	model := renderTestStatusModel(t, statusview.HealthDegraded)
	model.Identity.Description = "Demo 雪 worker"
	model.Logs[2].Message = "日志 雪"
	var output bytes.Buffer
	if err := renderStatusPlainV2(&output, model, false); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, forbidden := range []string{"┌", "└", "├", "┤", "│", "▼", "●", "─", "\x1b["} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("plain output contains terminal structure %q:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, "Demo 雪 worker") || !strings.Contains(got, "日志 雪") {
		t.Fatalf("plain output changed Unicode user data:\n%s", got)
	}
}

func renderTestStatusModel(t *testing.T, health statusview.Health) statusview.Model {
	t.Helper()
	observedAt := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	startedAt := observedAt.Add(-90 * time.Second)
	ids := map[string]string{
		"api":     "servicectl-api:system:system",
		"s6":      "s6:system:system",
		"orch":    "sys-orchestrd:system:demo.service",
		"dinit":   "dinit:system:demo-notifyd",
		"helper":  "sys-notifyd:system:demo-notifyd",
		"service": "service:system:demo.service",
		"cgroup":  "sys-cgroupd:system:system",
		"vision":  "sysvisiond:system:system",
	}
	model := statusview.NewModel()
	model.ObservedAt = observedAt
	model.Identity = statusview.Identity{Unit: "demo.service", Name: "demo", Description: "Demo worker", Type: "notify", Scope: "system", SourcePath: "/etc/systemd/system/demo.service"}
	model.Summary = statusview.Summary{RuntimeState: statusview.RuntimeActive, EnabledState: "enabled", MainPID: 42, StartedAt: &startedAt, ActiveDurationSeconds: 90}
	add := func(id, typeName, name, state string, nodeHealth statusview.Health, pid int) {
		node := statusview.NewNode(id, typeName, name, "system")
		node.Expected = true
		node.State = state
		node.Health = nodeHealth
		node.PID = pid
		node.ObservedAt = observedAt
		node.Evidence = append(node.Evidence, statusview.Evidence{Source: statusview.EvidenceComponentStatus, Result: statusview.EvidenceHealthy, CheckedAt: observedAt})
		model.Orchestration.Nodes = append(model.Orchestration.Nodes, node)
	}
	add(ids["api"], "servicectl-api", "servicectl-api · system", "active", statusview.HealthHealthy, 9)
	add(ids["s6"], "s6", "s6 supervisor · system", "active", statusview.HealthHealthy, 10)
	add(ids["orch"], "sys-orchestrd", "sys-orchestrd · demo.service", "active", statusview.HealthHealthy, 11)
	add(ids["dinit"], "dinit", "dinit · demo-notifyd", "active", statusview.HealthHealthy, 12)
	add(ids["helper"], "sys-notifyd", "sys-notifyd · demo-notifyd", "running", statusview.HealthHealthy, 41)
	add(ids["service"], "service", "demo.service", "active", statusview.HealthHealthy, 42)
	add(ids["cgroup"], "sys-cgroupd", "sys-cgroupd · system", "tracked", statusview.HealthHealthy, 13)
	add(ids["vision"], "sysvisiond", "sysvisiond · system", "active", statusview.HealthHealthy, 14)
	model.Orchestration.Edges = append(model.Orchestration.Edges,
		statusview.Edge{From: ids["api"], To: ids["s6"], Relation: statusview.RelationControls, Primary: true},
		statusview.Edge{From: ids["s6"], To: ids["orch"], Relation: statusview.RelationSupervises, Primary: true},
		statusview.Edge{From: ids["orch"], To: ids["dinit"], Relation: statusview.RelationControls, Primary: true},
		statusview.Edge{From: ids["dinit"], To: ids["helper"], Relation: statusview.RelationSupervises, Primary: true},
		statusview.Edge{From: ids["helper"], To: ids["service"], Relation: statusview.RelationSupervises, Primary: true},
		statusview.Edge{From: ids["cgroup"], To: ids["service"], Relation: statusview.RelationAccounts},
		statusview.Edge{From: ids["vision"], To: ids["service"], Relation: statusview.RelationObserves},
		statusview.Edge{From: ids["service"], To: ids["vision"], Relation: statusview.RelationObserves},
	)
	if health != statusview.HealthHealthy {
		for i := range model.Orchestration.Nodes {
			if model.Orchestration.Nodes[i].ID == ids["cgroup"] {
				model.Orchestration.Nodes[i].Health = health
				model.Orchestration.Nodes[i].State = "unknown"
				severity := statusview.SeverityUnknown
				code := "component_unobservable"
				if health == statusview.HealthDegraded {
					model.Orchestration.Nodes[i].State = "missing"
					severity = statusview.SeverityDegraded
					code = "expected_node_missing"
				}
				model.Diagnostics = append(model.Diagnostics, statusview.Diagnostic{Severity: severity, Domain: statusview.DomainOrchestration, Code: code, Message: "Expected cgroup controller status is unavailable.", AffectsHealth: true, ObservedAt: observedAt, NodeID: ids["cgroup"], Hint: "Inspect servicectl cgroup status."})
			}
		}
	}
	for i := 1; i <= 7; i++ {
		severity := statusview.LogInfo
		if i == 5 {
			severity = statusview.LogError
		}
		model.Logs = append(model.Logs, statusview.LogEntry{Timestamp: observedAt.Add(time.Duration(i-8) * time.Second), SourceSequence: uint64(i), Stream: "stdout", Severity: severity, Message: "log line " + priorityString(i)})
	}
	finalized, err := statusview.Finalize(model)
	if err != nil {
		t.Fatal(err)
	}
	return finalized
}

func renderTestFailedStatusModel(t *testing.T) statusview.Model {
	model := renderTestStatusModel(t, statusview.HealthDegraded)
	model.Summary.RuntimeState = statusview.RuntimeFailed
	model.Summary.MainPID = 0
	model.Summary.StartedAt = nil
	model.Summary.ActiveDurationSeconds = 0
	for i := range model.Orchestration.Nodes {
		if model.Orchestration.Nodes[i].Type == "service" {
			model.Orchestration.Nodes[i].State = "failed"
			model.Orchestration.Nodes[i].PID = 0
			model.Orchestration.Nodes[i].ProcessStartedAt = nil
			model.Orchestration.Nodes[i].ActiveDurationSeconds = 0
		}
	}
	finalized, err := statusview.Finalize(model)
	if err != nil {
		t.Fatal(err)
	}
	return finalized
}

func renderTestUserBusModel(t *testing.T) statusview.Model {
	t.Helper()
	observedAt := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	model := statusview.NewModel()
	model.ObservedAt = observedAt
	model.Identity = statusview.Identity{Unit: "portal.service", Name: "portal", Description: "Portal service", Type: "dbus", Scope: "user@1000", SourcePath: "/home/demo/.config/systemd/user/portal.service"}
	model.Summary = statusview.Summary{RuntimeState: statusview.RuntimeActive, EnabledState: "enabled", MainPID: 52}
	for _, node := range []statusview.Node{
		func() statusview.Node {
			n := statusview.NewNode("sysvbus:user@1000:user@1000", "sysvbus", "sysvbus · user@1000", "user@1000")
			n.State = "connected"
			n.Health = statusview.HealthHealthy
			n.Expected = true
			n.ObservedAt = observedAt
			n.Endpoint = "/run/user/1000/servicectl/events.sock"
			n.Evidence = append(n.Evidence, statusview.Evidence{Source: statusview.EvidenceComponentStatus, Result: statusview.EvidenceHealthy, CheckedAt: observedAt})
			return n
		}(),
		func() statusview.Node {
			n := statusview.NewNode("dbus:user@1000:user@1000", "dbus", "dbus · user@1000", "user@1000")
			n.State = "owned"
			n.Health = statusview.HealthHealthy
			n.Expected = true
			n.ObservedAt = observedAt
			n.BusOwner = ":1.42"
			n.Evidence = append(n.Evidence, statusview.Evidence{Source: statusview.EvidenceComponentStatus, Result: statusview.EvidenceHealthy, CheckedAt: observedAt})
			return n
		}(),
		func() statusview.Node {
			n := statusview.NewNode("service:user@1000:portal.service", "service", "portal.service", "user@1000")
			n.State = "active"
			n.Health = statusview.HealthHealthy
			n.Expected = true
			n.ObservedAt = observedAt
			n.PID = 52
			n.Evidence = append(n.Evidence, statusview.Evidence{Source: statusview.EvidenceSysvisionSnapshot, Result: statusview.EvidenceHealthy, CheckedAt: observedAt})
			return n
		}(),
	} {
		model.Orchestration.Nodes = append(model.Orchestration.Nodes, node)
	}
	model.Orchestration.Edges = append(model.Orchestration.Edges,
		statusview.Edge{From: "sysvbus:user@1000:user@1000", To: "service:user@1000:portal.service", Relation: statusview.RelationControls, Primary: true},
		statusview.Edge{From: "dbus:user@1000:user@1000", To: "service:user@1000:portal.service", Relation: statusview.RelationActivates},
	)
	finalized, err := statusview.Finalize(model)
	if err != nil {
		t.Fatal(err)
	}
	return finalized
}
