package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"servicectl/internal/statusview"
)

func TestParseStatusDisplayArgsVerbose(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want displayOptions
	}{
		{name: "verbose", args: []string{"demo", "--verbose"}, want: displayOptions{verbose: true}},
		{name: "verbose first", args: []string{"--verbose", "demo"}, want: displayOptions{verbose: true}},
		{name: "plain verbose", args: []string{"--plain", "--verbose", "demo.service"}, want: displayOptions{mode: displayModePlain, verbose: true}},
		{name: "json", args: []string{"--json", "demo"}, want: displayOptions{mode: displayModeJSON}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unit, got, err := parseStatusDisplayArgs(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if unit != "demo" || got != tt.want {
				t.Fatalf("unit/options = %q %#v, want demo %#v", unit, got, tt.want)
			}
		})
	}
}

func TestParseStatusDisplayArgsRejectsVerboseJSON(t *testing.T) {
	for _, args := range [][]string{
		{"demo", "--json", "--verbose"},
		{"--verbose", "--json", "demo"},
		{"demo", "--plain", "--json"},
		{"one", "two"},
		{},
		{"demo", "--unknown"},
	} {
		if _, _, err := parseStatusDisplayArgs(args); err == nil {
			t.Fatalf("args %#v unexpectedly accepted", args)
		}
	}
}

func TestRunStatusDisplayExitCodes(t *testing.T) {
	tests := []struct {
		name   string
		health statusview.Health
		state  statusview.RuntimeState
		want   int
	}{
		{name: "healthy active", health: statusview.HealthHealthy, state: statusview.RuntimeActive, want: 0},
		{name: "healthy inactive", health: statusview.HealthHealthy, state: statusview.RuntimeInactive, want: 0},
		{name: "healthy activating", health: statusview.HealthHealthy, state: statusview.RuntimeActivating, want: 0},
		{name: "failed", health: statusview.HealthFailed, state: statusview.RuntimeFailed, want: 3},
		{name: "degraded", health: statusview.HealthDegraded, state: statusview.RuntimeActive, want: 3},
		{name: "unknown", health: statusview.HealthUnknown, state: statusview.RuntimeActive, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := statusCLITestModel(t, tt.health, tt.state)
			collector := func(context.Context, string, string, uint32) (statusview.Model, error) { return model, nil }
			var output bytes.Buffer
			code := runStatusDisplay(&output, "demo", displayOptions{mode: displayModeJSON}, displayRuntime{TTY: true, Width: 96}, collector)
			if code != tt.want {
				t.Fatalf("code = %d, want %d; output=%s", code, tt.want, output.String())
			}
			var decoded statusview.Model
			if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
				t.Fatalf("JSON output: %v\n%s", err, output.String())
			}
			if decoded.SchemaVersion != 2 {
				t.Fatalf("schema = %d", decoded.SchemaVersion)
			}
		})
	}
}

func TestRunStatusDisplayNotFoundJSON(t *testing.T) {
	collector := func(context.Context, string, string, uint32) (statusview.Model, error) {
		return statusview.Model{}, &statusCollectionError{Kind: statusCollectionNotFound, Unit: "missing.service", Err: errStatusUnitNotFound}
	}
	var output bytes.Buffer
	code := runStatusDisplay(&output, "missing", displayOptions{mode: displayModeJSON}, displayRuntime{TTY: true, Width: 96}, collector)
	if code != 4 {
		t.Fatalf("code = %d, output=%s", code, output.String())
	}
	var decoded statusJSONErrorV2
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != 2 || decoded.Error.Code != "unit_not_found" || decoded.Error.Unit != "missing.service" {
		t.Fatalf("error = %#v", decoded)
	}
}

func TestRunStatusDisplayCollectionErrorJSON(t *testing.T) {
	collector := func(context.Context, string, string, uint32) (statusview.Model, error) {
		return statusview.Model{}, &statusCollectionError{Kind: statusCollectionMandatory, Unit: "demo.service", Err: errors.New("snapshot unavailable")}
	}
	var output bytes.Buffer
	code := runStatusDisplay(&output, "demo", displayOptions{mode: displayModeJSON}, displayRuntime{TTY: true, Width: 96}, collector)
	if code != 1 {
		t.Fatalf("code = %d, output=%s", code, output.String())
	}
	var decoded statusJSONErrorV2
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("error output is not JSON: %v\n%s", err, output.String())
	}
	if decoded.Error.Code != "status_collection_failed" || !strings.Contains(decoded.Error.Message, "snapshot unavailable") {
		t.Fatalf("error = %#v", decoded)
	}
}

func TestRunStatusDisplaySelectsOutputMode(t *testing.T) {
	model := statusCLITestModel(t, statusview.HealthHealthy, statusview.RuntimeActive)
	collector := func(context.Context, string, string, uint32) (statusview.Model, error) { return model, nil }
	tests := []struct {
		name    string
		options displayOptions
		runtime displayRuntime
		want    string
		reject  string
	}{
		{name: "non tty plain", runtime: displayRuntime{TTY: false, Width: 96}, want: "ORCHESTRATION", reject: "●"},
		{name: "tty terminal", runtime: displayRuntime{TTY: true, Width: 96}, want: "● active"},
		{name: "explicit plain", options: displayOptions{mode: displayModePlain}, runtime: displayRuntime{TTY: true, Width: 96, Color: true}, want: "active  enabled", reject: "\x1b["},
		{name: "explicit json", options: displayOptions{mode: displayModeJSON}, runtime: displayRuntime{TTY: false, Width: 96}, want: `"schema_version":2`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if code := runStatusDisplay(&output, "demo", tt.options, tt.runtime, collector); code != 0 {
				t.Fatalf("code = %d, output=%s", code, output.String())
			}
			if !strings.Contains(output.String(), tt.want) || tt.reject != "" && strings.Contains(output.String(), tt.reject) {
				t.Fatalf("output mismatch:\n%s", output.String())
			}
		})
	}
}

func TestRunStatusDisplayVerboseChangesHumanOutputOnly(t *testing.T) {
	model := renderTestUserBusModel(t)
	collector := func(context.Context, string, string, uint32) (statusview.Model, error) { return model, nil }
	var normal bytes.Buffer
	if code := runStatusDisplay(&normal, "portal", displayOptions{mode: displayModePlain}, displayRuntime{}, collector); code != 0 {
		t.Fatalf("normal code = %d", code)
	}
	var verbose bytes.Buffer
	if code := runStatusDisplay(&verbose, "portal", displayOptions{mode: displayModePlain, verbose: true}, displayRuntime{}, collector); code != 0 {
		t.Fatalf("verbose code = %d", code)
	}
	if strings.Contains(normal.String(), "evidence ") || !strings.Contains(verbose.String(), "evidence ") {
		t.Fatalf("normal/verbose outputs:\n--- normal ---\n%s--- verbose ---\n%s", normal.String(), verbose.String())
	}
}

func TestRunStatusDisplayWriterError(t *testing.T) {
	model := statusCLITestModel(t, statusview.HealthHealthy, statusview.RuntimeActive)
	collector := func(context.Context, string, string, uint32) (statusview.Model, error) { return model, nil }
	code := runStatusDisplay(failingStatusWriter{}, "demo", displayOptions{mode: displayModeJSON}, displayRuntime{}, collector)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRunStatusDisplayLogDiagnosticDoesNotChangeExit(t *testing.T) {
	model := statusCLITestModel(t, statusview.HealthHealthy, statusview.RuntimeActive)
	model.Diagnostics = append(model.Diagnostics, statusview.Diagnostic{Severity: statusview.SeverityInfo, Domain: statusview.DomainOutput, Code: "logs_unavailable", Message: "logs unavailable", AffectsHealth: false, ObservedAt: model.ObservedAt})
	collector := func(context.Context, string, string, uint32) (statusview.Model, error) { return model, nil }
	if code := runStatusDisplay(io.Discard, "demo", displayOptions{mode: displayModePlain}, displayRuntime{}, collector); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
}

func statusCLITestModel(t *testing.T, health statusview.Health, state statusview.RuntimeState) statusview.Model {
	t.Helper()
	model := renderTestStatusModel(t, statusview.HealthHealthy)
	model.Summary.RuntimeState = state
	for i := range model.Orchestration.Nodes {
		if model.Orchestration.Nodes[i].Type == "service" {
			model.Orchestration.Nodes[i].State = string(state)
		}
		if health == statusview.HealthDegraded && model.Orchestration.Nodes[i].Type == "sys-cgroupd" {
			model.Orchestration.Nodes[i].Health = statusview.HealthDegraded
			model.Orchestration.Nodes[i].State = "missing"
		}
		if health == statusview.HealthUnknown && model.Orchestration.Nodes[i].Type == "sys-cgroupd" {
			model.Orchestration.Nodes[i].Health = statusview.HealthUnknown
			model.Orchestration.Nodes[i].State = "unknown"
		}
	}
	if health == statusview.HealthDegraded {
		model.Diagnostics = append(model.Diagnostics, statusview.Diagnostic{Severity: statusview.SeverityDegraded, Domain: statusview.DomainOrchestration, Code: "expected_node_missing", Message: "missing", AffectsHealth: true, ObservedAt: model.ObservedAt})
	}
	if health == statusview.HealthUnknown {
		model.Diagnostics = append(model.Diagnostics, statusview.Diagnostic{Severity: statusview.SeverityUnknown, Domain: statusview.DomainOrchestration, Code: "component_unobservable", Message: "unknown", AffectsHealth: true, ObservedAt: model.ObservedAt})
	}
	finalized, err := statusview.Finalize(model)
	if err != nil {
		t.Fatal(err)
	}
	return finalized
}
