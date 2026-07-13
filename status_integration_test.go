package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"servicectl/internal/statusview"
)

func TestStatusTopologyIntegrationHealthyTerminal(t *testing.T) {
	model := renderTestStatusModel(t, statusview.HealthHealthy)
	collector := staticStatusIntegrationCollector(model, nil)
	var output bytes.Buffer
	if code := runStatusDisplay(&output, "demo", displayOptions{}, displayRuntime{TTY: true, Width: 96}, collector); code != 0 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	for _, want := range []string{"servicectl-api", "s6 supervisor", "sys-orchestrd", "dinit", "sys-notifyd", "demo.service", "├── accounts"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, output.String())
		}
	}
}

func TestStatusTopologyIntegrationDegradedAndUnknown(t *testing.T) {
	for _, tt := range []struct {
		name   string
		health statusview.Health
		want   string
	}{
		{name: "degraded", health: statusview.HealthDegraded, want: "active (degraded)"},
		{name: "unknown", health: statusview.HealthUnknown, want: "active (unknown)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := statusCLITestModel(t, tt.health, statusview.RuntimeActive)
			var output bytes.Buffer
			code := runStatusDisplay(&output, "demo", displayOptions{mode: displayModePlain}, displayRuntime{}, staticStatusIntegrationCollector(model, nil))
			if code != 3 || !strings.Contains(output.String(), tt.want) {
				t.Fatalf("code=%d output=%s", code, output.String())
			}
		})
	}
}

func TestStatusTopologyIntegrationJSONV2(t *testing.T) {
	model := renderTestStatusModel(t, statusview.HealthHealthy)
	var output bytes.Buffer
	if code := runStatusDisplay(&output, "demo", displayOptions{mode: displayModeJSON}, displayRuntime{}, staticStatusIntegrationCollector(model, nil)); code != 0 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	var decoded statusview.Model
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != 2 {
		t.Fatalf("schema=%d", decoded.SchemaVersion)
	}
	nodes := map[string]bool{}
	for _, node := range decoded.Orchestration.Nodes {
		nodes[node.ID] = true
	}
	for _, edge := range decoded.Orchestration.Edges {
		if !nodes[edge.From] || !nodes[edge.To] {
			t.Fatalf("dangling edge=%#v", edge)
		}
	}
}

func TestStatusTopologyIntegrationMissingUnitJSONV2(t *testing.T) {
	err := &statusCollectionError{Kind: statusCollectionNotFound, Unit: "missing.service", Err: errStatusUnitNotFound}
	var output bytes.Buffer
	if code := runStatusDisplay(&output, "missing", displayOptions{mode: displayModeJSON}, displayRuntime{}, staticStatusIntegrationCollector(statusview.Model{}, err)); code != 4 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	var decoded statusJSONErrorV2
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != 2 || decoded.Error.Code != "unit_not_found" {
		t.Fatalf("error=%#v", decoded)
	}
}

func TestStatusTopologyIntegrationListV1AndPlainASCII(t *testing.T) {
	var listOutput bytes.Buffer
	if err := renderListJSON(&listOutput, listView{SchemaVersion: 1, Mode: "system", Units: []listUnitView{}}); err != nil {
		t.Fatal(err)
	}
	var listJSON map[string]any
	if err := json.Unmarshal(listOutput.Bytes(), &listJSON); err != nil || listJSON["schema_version"] != float64(1) {
		t.Fatalf("list JSON=%#v err=%v", listJSON, err)
	}

	model := renderTestStatusModel(t, statusview.HealthHealthy)
	var plain bytes.Buffer
	if code := runStatusDisplay(&plain, "demo", displayOptions{mode: displayModePlain}, displayRuntime{TTY: true, Color: true}, staticStatusIntegrationCollector(model, nil)); code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, forbidden := range []string{"\x1b[", "┌", "├", "│", "▼", "●"} {
		if strings.Contains(plain.String(), forbidden) {
			t.Fatalf("plain output contains %q:\n%s", forbidden, plain.String())
		}
	}
}

func staticStatusIntegrationCollector(model statusview.Model, err error) statusModelCollector {
	return func(context.Context, string, string, uint32) (statusview.Model, error) {
		return model, err
	}
}
