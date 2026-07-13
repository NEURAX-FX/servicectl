package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"servicectl/internal/statusview"
	"servicectl/internal/visionapi"
)

func TestRenderStatusJSONV2(t *testing.T) {
	model := sampleStatusModelV2(t)
	var output bytes.Buffer
	if err := renderStatusJSONV2(&output, model); err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"schema_version", "observed_at", "identity", "summary", "orchestration", "diagnostics", "logs"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("missing top-level field %q: %#v", field, decoded)
		}
	}
	if decoded["schema_version"] != float64(2) {
		t.Fatalf("schema_version = %#v", decoded["schema_version"])
	}
	for _, legacy := range []string{"state", "process", "control_plane", "failure"} {
		if _, ok := decoded[legacy]; ok {
			t.Fatalf("legacy field %q is present: %#v", legacy, decoded)
		}
	}

	identity := decoded["identity"].(map[string]any)
	if identity["unit"] != "demo.service" || identity["description"] != "Demo 雪 worker" {
		t.Fatalf("identity = %#v", identity)
	}
	summary := decoded["summary"].(map[string]any)
	if summary["runtime_state"] != "active" || summary["orchestration_health"] != "degraded" || summary["aggregate_health"] != "degraded" || summary["display_state"] != "active (degraded)" {
		t.Fatalf("summary = %#v", summary)
	}
	if summary["main_pid"] != float64(42) || summary["active_duration_seconds"] != float64(90) {
		t.Fatalf("summary numeric fields = %#v", summary)
	}
	if _, err := time.Parse(time.RFC3339Nano, summary["started_at"].(string)); err != nil {
		t.Fatalf("started_at = %#v: %v", summary["started_at"], err)
	}

	orchestration := decoded["orchestration"].(map[string]any)
	nodes := orchestration["nodes"].([]any)
	edges := orchestration["edges"].([]any)
	if len(nodes) != 2 || len(edges) != 1 {
		t.Fatalf("orchestration = %#v", orchestration)
	}
	nodeIDs := map[string]bool{}
	for _, raw := range nodes {
		node := raw.(map[string]any)
		nodeIDs[node["id"].(string)] = true
		if _, ok := node["evidence"].([]any); !ok {
			t.Fatalf("node evidence missing: %#v", node)
		}
		if _, err := time.Parse(time.RFC3339Nano, node["observed_at"].(string)); err != nil {
			t.Fatalf("node observed_at = %#v: %v", node["observed_at"], err)
		}
	}
	for _, raw := range edges {
		edge := raw.(map[string]any)
		if !nodeIDs[edge["from"].(string)] || !nodeIDs[edge["to"].(string)] {
			t.Fatalf("edge has dangling endpoint: %#v", edge)
		}
	}

	diagnostics := decoded["diagnostics"].([]any)
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	diagnostic := diagnostics[0].(map[string]any)
	for _, field := range []string{"severity", "domain", "code", "message", "affects_health", "observed_at"} {
		if _, ok := diagnostic[field]; !ok {
			t.Fatalf("diagnostic missing %q: %#v", field, diagnostic)
		}
	}

	logs := decoded["logs"].([]any)
	if len(logs) != 1 {
		t.Fatalf("logs = %#v", logs)
	}
	logEntry := logs[0].(map[string]any)
	if logEntry["message"] != "ready 雪" || logEntry["source_sequence"] != float64(81) {
		t.Fatalf("log = %#v", logEntry)
	}
}

func TestRenderStatusJSONV2KeepsRequiredEmptyArrays(t *testing.T) {
	model := statusview.NewModel()
	model.ObservedAt = time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	model.Identity = statusview.Identity{Unit: "demo.service", Name: "demo", Description: "Demo", Type: "simple", Scope: "system", SourcePath: "/demo.service"}
	model.Summary = statusview.Summary{RuntimeState: statusview.RuntimeInactive, OrchestrationHealth: statusview.HealthHealthy, AggregateHealth: statusview.HealthHealthy, DisplayState: "inactive", EnabledState: "disabled"}
	var output bytes.Buffer
	if err := renderStatusJSONV2(&output, model); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Orchestration struct {
			Nodes []json.RawMessage `json:"nodes"`
			Edges []json.RawMessage `json:"edges"`
		} `json:"orchestration"`
		Diagnostics []json.RawMessage `json:"diagnostics"`
		Logs        []json.RawMessage `json:"logs"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Orchestration.Nodes == nil || decoded.Orchestration.Edges == nil || decoded.Diagnostics == nil || decoded.Logs == nil {
		t.Fatalf("required arrays decoded as nil: %#v", decoded)
	}
}

func TestStatusJSONErrorV2(t *testing.T) {
	var output bytes.Buffer
	if err := renderStatusJSONErrorV2(&output, "unit_not_found", "Unit missing.service could not be found.", "missing.service"); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["schema_version"] != float64(2) {
		t.Fatalf("error schema = %#v", decoded)
	}
	errorObject := decoded["error"].(map[string]any)
	if errorObject["code"] != "unit_not_found" || errorObject["unit"] != "missing.service" || errorObject["message"] == "" {
		t.Fatalf("error = %#v", errorObject)
	}
}

func TestRenderStatusJSONV2ReturnsWriterError(t *testing.T) {
	err := renderStatusJSONV2(failingStatusWriter{}, sampleStatusModelV2(t))
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error = %v, want closed pipe", err)
	}
}

func TestRenderStatusJSONV2DoesNotMutateModel(t *testing.T) {
	model := sampleStatusModelV2(t)
	before := cloneStatusModelForJSONTest(model)
	var output bytes.Buffer
	if err := renderStatusJSONV2(&output, model); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(model, before) {
		t.Fatal("JSON rendering mutated the model")
	}
}

type failingStatusWriter struct{}

func (failingStatusWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func sampleStatusModelV2(t *testing.T) statusview.Model {
	t.Helper()
	observedAt := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	startedAt := observedAt.Add(-90 * time.Second)
	managerID := "sys-cgroupd:system:system"
	serviceID := "service:system:demo.service"
	model := statusview.NewModel()
	model.ObservedAt = observedAt
	model.Identity = statusview.Identity{Unit: "demo.service", Name: "demo", Description: "Demo 雪 worker", Type: "notify", Scope: "system", SourcePath: "/etc/systemd/system/demo.service"}
	model.Summary = statusview.Summary{RuntimeState: statusview.RuntimeActive, EnabledState: "enabled", MainPID: 42, StartedAt: &startedAt, ActiveDurationSeconds: 90}
	manager := statusview.NewNode(managerID, "sys-cgroupd", "sys-cgroupd · system", "system")
	manager.Health = statusview.HealthDegraded
	manager.State = "missing"
	manager.Expected = true
	manager.ObservedAt = observedAt
	manager.Evidence = append(manager.Evidence, statusview.Evidence{Source: statusview.EvidenceCgroupProbe, Result: statusview.EvidenceNotFound, CheckedAt: observedAt, Detail: "not found"})
	service := statusview.NewNode(serviceID, "service", "demo.service", "system")
	service.Health = statusview.HealthHealthy
	service.State = "active"
	service.Expected = true
	service.ObservedAt = observedAt
	service.PID = 42
	service.ProcessStartedAt = &startedAt
	service.ActiveDurationSeconds = 90
	service.Evidence = append(service.Evidence, statusview.Evidence{Source: statusview.EvidenceSysvisionSnapshot, Result: statusview.EvidenceHealthy, Authoritative: true, CheckedAt: observedAt})
	model.Orchestration.Nodes = append(model.Orchestration.Nodes, manager, service)
	model.Orchestration.Edges = append(model.Orchestration.Edges, statusview.Edge{From: managerID, To: serviceID, Relation: statusview.RelationAccounts})
	model.Diagnostics = append(model.Diagnostics, statusview.Diagnostic{Severity: statusview.SeverityDegraded, Domain: statusview.DomainOrchestration, Code: "expected_node_missing", Message: "Expected component sys-cgroupd is missing.", AffectsHealth: true, ObservedAt: observedAt, NodeID: managerID})
	model.Logs = append(model.Logs, statusview.LogEntry{Timestamp: observedAt.Add(-2 * time.Second), SourceSequence: 81, Stream: "stdout", Severity: statusview.LogInfo, Message: "ready 雪"})
	finalized, err := statusview.Finalize(model)
	if err != nil {
		t.Fatal(err)
	}
	return finalized
}

func cloneStatusModelForJSONTest(in statusview.Model) statusview.Model {
	out := in
	out.Orchestration.Nodes = make([]statusview.Node, len(in.Orchestration.Nodes))
	for i, node := range in.Orchestration.Nodes {
		out.Orchestration.Nodes[i] = node
		out.Orchestration.Nodes[i].Evidence = append([]statusview.Evidence(nil), node.Evidence...)
		out.Orchestration.Nodes[i].ChildPIDs = append([]int(nil), node.ChildPIDs...)
	}
	out.Orchestration.Edges = append([]statusview.Edge(nil), in.Orchestration.Edges...)
	out.Diagnostics = append([]statusview.Diagnostic(nil), in.Diagnostics...)
	out.Logs = append([]statusview.LogEntry(nil), in.Logs...)
	return out
}

func TestProcessStartTimeCommandUsesStableLocale(t *testing.T) {
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	command := processStartTimeCommand("42")
	found := 0
	for _, value := range command.Env {
		if value == "LC_ALL=C" {
			found++
		}
		if strings.HasPrefix(value, "LC_ALL=") && value != "LC_ALL=C" {
			t.Fatalf("command retained conflicting locale: %#v", command.Env)
		}
	}
	if found != 1 {
		t.Fatalf("command environment does not force LC_ALL=C: %#v", command.Env)
	}
}

func TestParseListDisplayArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want displayOptions
	}{
		{name: "default", want: displayOptions{}},
		{name: "all", args: []string{"--all"}, want: displayOptions{all: true}},
		{name: "plain", args: []string{"--plain"}, want: displayOptions{mode: displayModePlain}},
		{name: "json", args: []string{"--json"}, want: displayOptions{mode: displayModeJSON}},
		{name: "combined", args: []string{"--all", "--json"}, want: displayOptions{all: true, mode: displayModeJSON}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseListDisplayArgs(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("options = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseListDisplayArgsRejectsInvalidCombinations(t *testing.T) {
	for _, args := range [][]string{{"--plain", "--json"}, {"unexpected"}, {"--unknown"}} {
		if _, err := parseListDisplayArgs(args); err == nil {
			t.Fatalf("args %#v unexpectedly accepted", args)
		}
	}
}

func TestParseStatusDisplayArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantUnit string
		wantOpts displayOptions
	}{
		{name: "default", args: []string{"demo"}, wantUnit: "demo"},
		{name: "plain", args: []string{"demo.service", "--plain"}, wantUnit: "demo", wantOpts: displayOptions{mode: displayModePlain}},
		{name: "json first", args: []string{"--json", "demo"}, wantUnit: "demo", wantOpts: displayOptions{mode: displayModeJSON}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unit, opts, err := parseStatusDisplayArgs(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if unit != test.wantUnit || opts != test.wantOpts {
				t.Fatalf("unit=%q opts=%#v, want unit=%q opts=%#v", unit, opts, test.wantUnit, test.wantOpts)
			}
		})
	}
}

func TestParseStatusDisplayArgsRejectsInvalidCombinations(t *testing.T) {
	for _, args := range [][]string{{}, {"--plain"}, {"one", "two"}, {"demo", "--plain", "--json"}, {"demo", "--all"}} {
		if _, _, err := parseStatusDisplayArgs(args); err == nil {
			t.Fatalf("args %#v unexpectedly accepted", args)
		}
	}
}

func TestParseDisplayInvocation(t *testing.T) {
	parsed, err := parseDisplayInvocation("list", []string{"--all", "--json"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Handled || parsed.List == nil || !parsed.List.all || parsed.List.mode != displayModeJSON {
		t.Fatalf("list invocation = %#v", parsed)
	}
	parsed, err = parseDisplayInvocation("status", []string{"demo", "--plain"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Handled || parsed.Status == nil || parsed.Status.Unit != "demo" || parsed.Status.Options.mode != displayModePlain {
		t.Fatalf("status invocation = %#v", parsed)
	}
	parsed, err = parseDisplayInvocation("start", []string{"demo"}, "")
	if err != nil || parsed.Handled {
		t.Fatalf("start invocation = %#v err=%v", parsed, err)
	}
}

func TestParseDisplayInvocationLeavesGroupStatusUntouched(t *testing.T) {
	parsed, err := parseDisplayInvocation("status", nil, "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Handled {
		t.Fatalf("group status was intercepted: %#v", parsed)
	}
}

func TestParseDisplayInvocationRejectsInvalidArgsBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		action string
		args   []string
	}{{"list", []string{"--bad"}}, {"status", []string{"demo", "--plain", "--json"}}} {
		if _, err := parseDisplayInvocation(test.action, test.args, ""); err == nil {
			t.Fatalf("%s %#v unexpectedly accepted", test.action, test.args)
		}
	}
}

func TestCanonicalRuntimeState(t *testing.T) {
	tests := []struct {
		name     string
		snapshot visionapi.UnitSnapshot
		want     string
	}{
		{name: "failure", snapshot: visionapi.UnitSnapshot{State: "STARTED", Failure: "readiness timeout"}, want: "failed"},
		{name: "lifecycle failed", snapshot: visionapi.UnitSnapshot{Lifecycle: "failed"}, want: "failed"},
		{name: "starting child", snapshot: visionapi.UnitSnapshot{State: "STARTED", ChildState: "starting"}, want: "activating"},
		{name: "stopping child", snapshot: visionapi.UnitSnapshot{State: "STARTED", ChildState: "stopping"}, want: "deactivating"},
		{name: "started", snapshot: visionapi.UnitSnapshot{State: "STARTED"}, want: "active"},
		{name: "failed exit", snapshot: visionapi.UnitSnapshot{State: "STOPPED (terminated; exited - status 1)"}, want: "failed"},
		{name: "stopped", snapshot: visionapi.UnitSnapshot{State: "STOPPED"}, want: "inactive"},
		{name: "missing", snapshot: visionapi.UnitSnapshot{}, want: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := canonicalRuntimeState(test.snapshot); got != test.want {
				t.Fatalf("state = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSortListUnitViewsFailureFirst(t *testing.T) {
	units := []listUnitView{
		{Name: "zeta", RuntimeState: "active"},
		{Name: "beta", RuntimeState: "failed"},
		{Name: "alpha", RuntimeState: "failed"},
		{Name: "gamma", RuntimeState: "inactive"},
		{Name: "delta", RuntimeState: "activating"},
	}
	sortListUnitViews(units)
	got := make([]string, 0, len(units))
	for _, unit := range units {
		got = append(got, unit.Name)
	}
	want := []string{"alpha", "beta", "delta", "zeta", "gamma"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %#v, want %#v", got, want)
	}
}

func TestBuildListUnitView(t *testing.T) {
	unit := &Unit{Name: "demo.service", Description: "Demo worker", Type: "notify"}
	snapshot := visionapi.UnitSnapshot{Name: "demo", State: "STARTED", MainPID: "42", ManagedBy: "sys-notifyd"}
	got := buildListUnitView(snapshot, unit, true)
	if got.Unit != "demo.service" || got.Name != "demo" || got.Description != "Demo worker" {
		t.Fatalf("identity = %#v", got)
	}
	if got.RuntimeState != "active" || got.EnabledState != "enabled" || got.Type != "notify" || got.MainPID != 42 {
		t.Fatalf("state = %#v", got)
	}
	if got.Backend != "sys-notifyd" || got.Internal {
		t.Fatalf("metadata = %#v", got)
	}
}

func TestBuildListUnitViewUsesSafeFallbacks(t *testing.T) {
	got := buildListUnitView(visionapi.UnitSnapshot{Name: "legacy"}, nil, false)
	if got.Unit != "legacy.service" || got.Name != "legacy" || got.Type != "service" || got.RuntimeState != "unknown" || got.MainPID != 0 {
		t.Fatalf("view = %#v", got)
	}
}

func TestInternalServiceRole(t *testing.T) {
	tests := map[string]string{
		"demo-orchestrd": "orchestrator",
		"demo-notifyd":   "manager",
		"demo-dbusd":     "manager",
		"demo-socketd":   "manager",
		"demo-log":       "logger",
		"other":          "service",
	}
	for name, want := range tests {
		if got := internalServiceRole(name); got != want {
			t.Fatalf("role(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestResolveDisplayMode(t *testing.T) {
	tests := []struct {
		explicit displayMode
		tty      bool
		want     displayMode
	}{
		{explicit: displayModeJSON, tty: true, want: displayModeJSON},
		{explicit: displayModePlain, tty: true, want: displayModePlain},
		{explicit: displayModeAuto, tty: true, want: displayModeTerminal},
		{explicit: displayModeAuto, tty: false, want: displayModePlain},
	}
	for _, test := range tests {
		if got := resolveDisplayMode(test.explicit, test.tty); got != test.want {
			t.Fatalf("resolve(%v, %v) = %v, want %v", test.explicit, test.tty, got, test.want)
		}
	}
}

func TestVisibleWidthIgnoresANSI(t *testing.T) {
	if got := visibleWidth("\x1b[31mfailed\x1b[0m"); got != 6 {
		t.Fatalf("width = %d", got)
	}
}

func TestVisibleWidthCountsWideRunes(t *testing.T) {
	if got := visibleWidth("六 7月"); got != 6 {
		t.Fatalf("width = %d", got)
	}
}

func TestTruncateVisible(t *testing.T) {
	if got := truncateVisible("a-very-long-service-name", 12); got != "a-very-long…" {
		t.Fatalf("truncated = %q", got)
	}
	if got := truncateVisible("short", 12); got != "short" {
		t.Fatalf("short = %q", got)
	}
}

func TestTruncateVisibleHandlesWideRunes(t *testing.T) {
	got := truncateVisible("六 7月 11 15:55", 10)
	if visibleWidth(got) > 10 || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated=%q width=%d", got, visibleWidth(got))
	}
}

func TestInternalS6ServiceMatchesMode(t *testing.T) {
	tests := []struct {
		name    string
		run     string
		mode    string
		matches bool
	}{
		{name: "sysvisiond", mode: "system", matches: true},
		{name: "sysvisiond", mode: "user", matches: false},
		{name: "sysvisiond-user-0", mode: "user", matches: true},
		{name: "servicectl-api-user-0", mode: "system", matches: false},
		{name: "demo-orchestrd", run: "/usr/bin/sys-orchestrd --unit demo.service", mode: "system", matches: true},
		{name: "demo-orchestrd", run: "/usr/bin/sys-orchestrd --user --unit demo.service", mode: "user", matches: true},
		{name: "demo-orchestrd", run: "/usr/bin/sys-orchestrd --user --unit demo.service", mode: "system", matches: false},
	}
	for _, test := range tests {
		if got := internalS6ServiceMatchesMode(test.name, test.run, test.mode); got != test.matches {
			t.Fatalf("matches(%q, %q, %q)=%v, want %v", test.name, test.run, test.mode, got, test.matches)
		}
	}
}

func TestRenderListTerminal(t *testing.T) {
	view := listView{
		Mode: "system",
		Units: []listUnitView{
			{Name: "api", Unit: "api.service", RuntimeState: "failed", EnabledState: "enabled", Type: "notify", MainPID: 1842},
			{Name: "worker", Unit: "worker.service", RuntimeState: "active", EnabledState: "disabled", Type: "simple"},
			{Name: "api-orchestrd", Unit: "api-orchestrd", RuntimeState: "active", Type: "orchestrator", Role: "orchestrator", MainPID: 2011, Internal: true},
		},
	}
	var output bytes.Buffer
	if err := renderListTerminal(&output, view, renderCapabilities{Width: 80}); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{"SERVICES · system · 2 units", "failed", "api", "[notify]", "enabled", "pid 1842", "INTERNAL · 1 service", "api-orchestrd", "[orchestrator]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "api[notify]") {
		t.Fatalf("name and metadata are not separated:\n%s", got)
	}
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if visibleWidth(line) > 80 {
			t.Fatalf("line exceeds width: %d %q", visibleWidth(line), line)
		}
	}
}

func TestRenderListTerminalLeftAlignsNamesInCompactColumn(t *testing.T) {
	view := listView{Mode: "system", Units: []listUnitView{
		{Name: "short", RuntimeState: "active", EnabledState: "enabled", Type: "simple"},
		{Name: "longer-name", RuntimeState: "active", EnabledState: "enabled", Type: "simple"},
	}}
	var output bytes.Buffer
	if err := renderListTerminal(&output, view, renderCapabilities{Width: 80}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	firstName := strings.Index(lines[2], "short")
	secondName := strings.Index(lines[3], "longer-name")
	if firstName != secondName {
		t.Fatalf("name starts differ: first=%d second=%d\n%s", firstName, secondName, output.String())
	}
	if gap := strings.Index(lines[3], "[simple]") - (secondName + len("longer-name")); gap != 2 {
		t.Fatalf("metadata gap = %d, want 2\n%s", gap, output.String())
	}
}

func TestRenderListTerminalRespectsNarrowWidth(t *testing.T) {
	view := listView{Mode: "system", Units: []listUnitView{{Name: "very-long-service-name", RuntimeState: "activating", EnabledState: "enabled", Type: "notify", MainPID: 12345}}}
	var output bytes.Buffer
	if err := renderListTerminal(&output, view, renderCapabilities{Width: 36}); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if visibleWidth(line) > 36 {
			t.Fatalf("line width=%d exceeds 36: %q", visibleWidth(line), line)
		}
	}
}

func TestRenderListPlain(t *testing.T) {
	view := listView{Mode: "user", Units: []listUnitView{{Unit: "demo.service", Name: "demo", RuntimeState: "active", EnabledState: "enabled", Type: "notify", MainPID: 42}}}
	var output bytes.Buffer
	if err := renderListPlain(&output, view); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.ContainsAny(got, "●┌└") || strings.Contains(got, "\x1b[") {
		t.Fatalf("plain output contains terminal decoration: %q", got)
	}
	if !strings.Contains(got, "demo.service\tactive\tenabled\tnotify\t42") {
		t.Fatalf("plain output = %q", got)
	}
}

func TestRenderListJSON(t *testing.T) {
	view := listView{SchemaVersion: 1, Mode: "system", GeneratedAt: "2026-07-11T12:34:56Z", Units: []listUnitView{{Unit: "demo.service", Name: "demo", RuntimeState: "inactive", EnabledState: "disabled", Type: "oneshot"}}}
	var output bytes.Buffer
	if err := renderListJSON(&output, view); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["schema_version"] != float64(1) || decoded["mode"] != "system" {
		t.Fatalf("json = %#v", decoded)
	}
	units, ok := decoded["units"].([]any)
	if !ok || len(units) != 1 {
		t.Fatalf("units = %#v", decoded["units"])
	}
}

func TestParseDinitListRows(t *testing.T) {
	rows := parseDinitListRows("[[+]     ] demo-notifyd (pid: 42)\n[     {X}] broken-orchestrd (exit status: 1)\n[{+}     ] demo-log (pid: 43)\n[     {-}] stopped-log\n")
	want := []dinitListRow{
		{Name: "demo-notifyd", RuntimeState: "active", PID: 42},
		{Name: "broken-orchestrd", RuntimeState: "failed"},
		{Name: "demo-log", RuntimeState: "active", PID: 43},
		{Name: "stopped-log", RuntimeState: "inactive"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %#v, want %#v", rows, want)
	}
}

func TestCollectListViewUsesSysvisionAndEnrichesRows(t *testing.T) {
	source := displayDataSource{
		queryUnits: func() (visionapi.UnitsResponse, bool) {
			return visionapi.UnitsResponse{GeneratedAt: "2026-07-11T12:34:56Z", Units: []visionapi.UnitSnapshot{
				{Name: "healthy", State: "STARTED", MainPID: "42"},
				{Name: "broken", State: "STOPPED", Failure: "boom"},
			}}, true
		},
		parseUnit: func(name string) (*Unit, error) {
			return &Unit{Name: name + ".service", Description: strings.Title(name), Type: "notify"}, nil
		},
		enabled: func(name string) bool { return name == "healthy" },
	}
	view, err := collectListView(source, false)
	if err != nil {
		t.Fatal(err)
	}
	if view.SchemaVersion != 1 || view.Mode != config.Mode || view.GeneratedAt != "2026-07-11T12:34:56Z" {
		t.Fatalf("metadata = %#v", view)
	}
	if len(view.Units) != 2 || view.Units[0].Name != "broken" || view.Units[0].RuntimeState != "failed" || view.Units[1].EnabledState != "enabled" {
		t.Fatalf("units = %#v", view.Units)
	}
}

func TestCollectListViewFallsBackToPropertyUnits(t *testing.T) {
	built := make([]string, 0)
	source := displayDataSource{
		queryUnits: func() (visionapi.UnitsResponse, bool) { return visionapi.UnitsResponse{}, false },
		propertyLists: func() (visionapi.UnitListsResponse, error) {
			return visionapi.UnitListsResponse{EffectiveUnits: []string{"demo.service"}}, nil
		},
		buildSnapshot: func(_ Config, name string) (visionapi.UnitSnapshot, error) {
			built = append(built, name)
			return visionapi.UnitSnapshot{Name: name, State: "STARTED"}, nil
		},
		parseUnit: func(name string) (*Unit, error) { return &Unit{Name: name + ".service", Type: "simple"}, nil },
		enabled:   func(string) bool { return false },
	}
	view, err := collectListView(source, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(built, []string{"demo"}) || len(view.Units) != 1 || view.Units[0].Name != "demo" {
		t.Fatalf("built=%#v view=%#v", built, view)
	}
}

func TestCollectListViewIncludesInternalRowsWithAll(t *testing.T) {
	source := displayDataSource{
		queryUnits: func() (visionapi.UnitsResponse, bool) {
			return visionapi.UnitsResponse{Units: []visionapi.UnitSnapshot{{Name: "demo", State: "STARTED", DinitName: "demo-notifyd"}}}, true
		},
		parseUnit: func(name string) (*Unit, error) { return &Unit{Name: name + ".service", Type: "notify"}, nil },
		enabled:   func(string) bool { return true },
		dinitList: func(...string) (string, int, error) {
			return "[[+]     ] demo-notifyd (pid: 41)\n[{+}     ] demo-log (pid: 40)\n", 0, nil
		},
		orchestratorExists: func(name string) bool { return name == "demo" },
		orchestratorPID:    func(string) string { return "39" },
		s6Services: func() []dinitListRow {
			return []dinitListRow{{Name: "sysvisiond", RuntimeState: "active", PID: 38}}
		},
	}
	view, err := collectListView(source, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Units) != 5 {
		t.Fatalf("units = %#v", view.Units)
	}
	roles := map[string]string{}
	for _, unit := range view.Units {
		if unit.Internal {
			roles[unit.Name] = unit.Role
		}
	}
	if roles["demo-notifyd"] != "manager" || roles["demo-log"] != "logger" || roles["demo-orchestrd"] != "orchestrator" {
		t.Fatalf("roles = %#v", roles)
	}
	if roles["sysvisiond"] != "service" {
		t.Fatalf("missing core service: %#v", roles)
	}
}

func TestRunListDisplaySelectsPlainForNonTTY(t *testing.T) {
	source := displayDataSource{
		queryUnits: func() (visionapi.UnitsResponse, bool) {
			return visionapi.UnitsResponse{Units: []visionapi.UnitSnapshot{{Name: "demo", State: "STARTED"}}}, true
		},
		parseUnit: func(name string) (*Unit, error) { return &Unit{Name: name + ".service", Type: "simple"}, nil },
		enabled:   func(string) bool { return true },
	}
	var output bytes.Buffer
	code := runListDisplay(&output, displayOptions{}, displayRuntime{TTY: false, Width: 80}, source)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if got := output.String(); got != "demo.service\tactive\tenabled\tsimple\t\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestRunListDisplayUsesJSONWhenRequested(t *testing.T) {
	source := displayDataSource{
		queryUnits: func() (visionapi.UnitsResponse, bool) { return visionapi.UnitsResponse{}, true },
	}
	var output bytes.Buffer
	code := runListDisplay(&output, displayOptions{mode: displayModeJSON}, displayRuntime{TTY: true, Width: 80}, source)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(output.String(), `"schema_version":1`) {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunStatusDisplayReturnsJSONErrorForMissingUnit(t *testing.T) {
	collector := func(context.Context, string, string, uint32) (statusview.Model, error) {
		return statusview.Model{}, &statusCollectionError{Kind: statusCollectionNotFound, Unit: "missing.service", Err: errStatusUnitNotFound}
	}
	var output bytes.Buffer
	code := runStatusDisplay(&output, "missing", displayOptions{mode: displayModeJSON}, displayRuntime{TTY: true, Width: 80}, collector)
	if code != 4 {
		t.Fatalf("code = %d", code)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	errorObject := decoded["error"].(map[string]any)
	if errorObject["code"] != "unit_not_found" || errorObject["unit"] != "missing.service" {
		t.Fatalf("error = %#v", errorObject)
	}
}

func TestRunStatusDisplayRendersTerminal(t *testing.T) {
	model := statusCLITestModel(t, statusview.HealthHealthy, statusview.RuntimeActive)
	collector := func(context.Context, string, string, uint32) (statusview.Model, error) {
		return model, nil
	}
	var output bytes.Buffer
	code := runStatusDisplay(&output, "demo", displayOptions{}, displayRuntime{TTY: true, Width: 90}, collector)
	if code != 0 || !strings.Contains(output.String(), "ORCHESTRATION") || !strings.Contains(output.String(), "Demo") {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
}
