package main

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
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

func TestServicectlAPIRefreshReplacesListsBeforeSnapshot(t *testing.T) {
	server := newServicectlPlaneServer(visionapi.ModeSystem, newServicectlEventHub())
	updates := make([]string, 0, 2)
	server.enabledUnits = func() []string { return []string{"enabled.service"} }
	server.scanRunnerUnits = func(Config) ([]string, error) { return []string{"runner.service"}, nil }
	server.replaceUnitList = func(mode, path string, units []string) error {
		updates = append(updates, path+":"+strings.Join(units, ","))
		return nil
	}
	server.queryUnitLists = func(mode string) (visionapi.UnitListsResponse, error) {
		if len(updates) == 0 {
			return visionapi.UnitListsResponse{}, nil
		}
		if len(updates) != 2 {
			t.Fatalf("snapshot query ran before both list updates: %#v", updates)
		}
		return visionapi.UnitListsResponse{EffectiveUnits: []string{"enabled.service", "runner.service"}}, nil
	}
	server.collectSnapshots = func(cfg Config, units []string) visionapi.UnitsResponse {
		return visionapi.UnitsResponse{Units: []visionapi.UnitSnapshot{{Name: strings.Join(units, ",")}}}
	}
	request := httptest.NewRequest("GET", "/v1/units?refresh=1", nil)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("code=%d body=%s", response.Code, response.Body.String())
	}
	var payload visionapi.UnitsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Units) != 1 || payload.Units[0].Name != "enabled.service,runner.service" {
		t.Fatalf("payload = %#v", payload)
	}
	want := []string{"/v1/enabled-list:enabled.service", "/v1/runner-list:runner.service"}
	if !reflect.DeepEqual(updates, want) {
		t.Fatalf("updates = %#v, want %#v", updates, want)
	}
}

func TestServicectlAPIRefreshDoesNotReplaceInitializedEnabledList(t *testing.T) {
	server := newServicectlPlaneServer(visionapi.ModeSystem, newServicectlEventHub())
	updates := make([]string, 0, 1)
	server.queryUnitLists = func(mode string) (visionapi.UnitListsResponse, error) {
		return visionapi.UnitListsResponse{EnabledInitialized: true}, nil
	}
	server.scanRunnerUnits = func(Config) ([]string, error) { return []string{"runner.service"}, nil }
	server.replaceUnitList = func(mode, path string, units []string) error {
		updates = append(updates, path)
		return nil
	}
	server.enabledUnits = func() []string { return []string{"must-not-replace.service"} }
	if err := server.refreshPropertyLists(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updates, []string{"/v1/runner-list"}) {
		t.Fatalf("updates = %#v", updates)
	}
}

func TestNormalizeUnitListNames(t *testing.T) {
	got := normalizeUnitListNames([]string{"b", "a.service", "b.service", "", "a"})
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("units = %#v, want %#v", got, want)
	}
}

func TestRunnerEventUpdate(t *testing.T) {
	tests := []struct {
		action  string
		result  string
		present bool
		ok      bool
	}{
		{action: "start", result: "ok", present: true, ok: true},
		{action: "restart", result: "ok", present: true, ok: true},
		{action: "stop", result: "ok", present: false, ok: true},
		{action: "reload", result: "ok", ok: false},
		{action: "start", result: "error", ok: false},
	}
	for _, test := range tests {
		present, ok := runnerEventUpdate(visionapi.EventEnvelope{Payload: map[string]string{"action": test.action, "result": test.result}})
		if present != test.present || ok != test.ok {
			t.Fatalf("action=%s result=%s => present=%v ok=%v", test.action, test.result, present, ok)
		}
	}
}

func TestSnapshotIsRunning(t *testing.T) {
	if !snapshotIsRunning(visionapi.UnitSnapshot{State: "STARTED"}) {
		t.Fatal("started unit was not running")
	}
	if !snapshotIsRunning(visionapi.UnitSnapshot{State: "STOPPED", MainPID: "42"}) {
		t.Fatal("live MainPID was not running")
	}
	if snapshotIsRunning(visionapi.UnitSnapshot{State: "STOPPED", MainPID: "not-a-pid"}) {
		t.Fatal("stopped unit was running")
	}
}
