package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"servicectl/internal/visionapi"
)

func TestNotifyReadyWritesOnce(t *testing.T) {
	var output bytes.Buffer
	d := newTestDaemon(t)
	d.readyWriter = &output
	if err := d.notifyReady(); err != nil {
		t.Fatal(err)
	}
	if err := d.notifyReady(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "\n" {
		t.Fatalf("readiness output = %q", got)
	}
}

func TestUnitListsAreModeIsolatedAndExpandEnabledGroups(t *testing.T) {
	d := newTestDaemon(t)
	d.defsByMode[visionapi.ModeSystem] = definitionSet{
		Groups: map[string]groupDefinition{
			"default": {Name: "default", Units: []string{"group-a.service", "shared.service"}},
		},
		Targets: map[string]string{},
	}
	if err := d.setEnabledUnit(visionapi.ModeSystem, "direct.service", true); err != nil {
		t.Fatal(err)
	}
	if err := d.setEnabledGroup(visionapi.ModeSystem, "default", true); err != nil {
		t.Fatal(err)
	}
	d.replaceRunnerList(visionapi.ModeSystem, []string{"shared.service", "temporary.service"})
	d.replaceRunnerList(visionapi.ModeUser, []string{"user-only.service"})

	system := d.unitLists(visionapi.ModeSystem)
	if !reflect.DeepEqual(system.EnabledUnits, []string{"direct.service"}) {
		t.Fatalf("enabled units = %#v", system.EnabledUnits)
	}
	if !reflect.DeepEqual(system.EnabledGroups, []string{"default"}) {
		t.Fatalf("enabled groups = %#v", system.EnabledGroups)
	}
	if !reflect.DeepEqual(system.RunnerUnits, []string{"shared.service", "temporary.service"}) {
		t.Fatalf("runner units = %#v", system.RunnerUnits)
	}
	if !reflect.DeepEqual(system.EffectiveUnits, []string{"direct.service", "group-a.service", "shared.service", "temporary.service"}) {
		t.Fatalf("effective units = %#v", system.EffectiveUnits)
	}

	user := d.unitLists(visionapi.ModeUser)
	if !reflect.DeepEqual(user.RunnerUnits, []string{"user-only.service"}) || !reflect.DeepEqual(user.EffectiveUnits, []string{"user-only.service"}) {
		t.Fatalf("user lists = %#v", user)
	}
}

func TestRunnerListReplacementAndIncrementalUpdateAreRuntimeOnly(t *testing.T) {
	d := newTestDaemon(t)
	d.replaceRunnerList(visionapi.ModeSystem, []string{"a", "b.service", "a.service"})
	d.setRunnerUnit(visionapi.ModeSystem, "c", true)
	d.setRunnerUnit(visionapi.ModeSystem, "a.service", false)
	if got := d.unitLists(visionapi.ModeSystem).RunnerUnits; !reflect.DeepEqual(got, []string{"b.service", "c.service"}) {
		t.Fatalf("runner units = %#v", got)
	}
	if err := d.saveStateLocked(); err != nil {
		t.Fatal(err)
	}
	reloaded := newTestDaemon(t)
	reloaded.statePath = d.statePath
	if err := reloaded.loadState(); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.unitLists(visionapi.ModeSystem).RunnerUnits; len(got) != 0 {
		t.Fatalf("runner list persisted: %#v", got)
	}
}

func TestEnabledListsPersistPerMode(t *testing.T) {
	d := newTestDaemon(t)
	if err := d.setEnabledUnit(visionapi.ModeSystem, "system-demo", true); err != nil {
		t.Fatal(err)
	}
	if err := d.setEnabledUnit(visionapi.ModeUser, "user-demo", true); err != nil {
		t.Fatal(err)
	}
	if err := d.setEnabledGroup(visionapi.ModeUser, "desktop", true); err != nil {
		t.Fatal(err)
	}
	reloaded := newTestDaemon(t)
	reloaded.statePath = d.statePath
	if err := reloaded.loadState(); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.unitLists(visionapi.ModeSystem).EnabledUnits; !reflect.DeepEqual(got, []string{"system-demo.service"}) {
		t.Fatalf("system enabled = %#v", got)
	}
	user := reloaded.unitLists(visionapi.ModeUser)
	if !reflect.DeepEqual(user.EnabledUnits, []string{"user-demo.service"}) || !reflect.DeepEqual(user.EnabledGroups, []string{"desktop"}) {
		t.Fatalf("user enabled = %#v", user)
	}
}

func TestUnitListHTTPAPI(t *testing.T) {
	d := newTestDaemon(t)
	server := httptest.NewServer(d.handler())
	defer server.Close()

	requestJSON(t, http.MethodPut, server.URL+"/v1/runner-list", visionapi.UnitListReplaceRequest{
		Mode: visionapi.ModeSystem, Units: []string{"a", "b.service"},
	})
	requestJSON(t, http.MethodPost, server.URL+"/v1/enabled-unit", visionapi.UnitListMembershipRequest{
		Mode: visionapi.ModeSystem, Unit: "enabled", Present: true,
	})
	requestJSON(t, http.MethodPut, server.URL+"/v1/enabled-list", visionapi.UnitListReplaceRequest{
		Mode: visionapi.ModeSystem, Units: []string{"enabled", "replacement"},
	})
	response, err := http.Get(server.URL + "/v1/unit-lists?mode=system")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var lists visionapi.UnitListsResponse
	if err := json.NewDecoder(response.Body).Decode(&lists); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lists.RunnerUnits, []string{"a.service", "b.service"}) || !reflect.DeepEqual(lists.EnabledUnits, []string{"enabled.service", "replacement.service"}) {
		t.Fatalf("lists = %#v", lists)
	}
}

func requestJSON(t *testing.T, method, endpoint string, body any) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, endpoint, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s %s returned %s", method, endpoint, response.Status)
	}
}

func newTestDaemon(t *testing.T) *daemon {
	t.Helper()
	return &daemon{
		statePath: filepath.Join(t.TempDir(), "state"),
		logger:    &syncLogger{},
		store: propertyStore{
			Persistent: map[string]string{},
			RuntimeByMode: map[string]map[string]string{
				visionapi.ModeSystem: {},
				visionapi.ModeUser:   {},
			},
		},
		defsByMode: map[string]definitionSet{
			visionapi.ModeSystem: {Groups: map[string]groupDefinition{}, Targets: map[string]string{}},
			visionapi.ModeUser:   {Groups: map[string]groupDefinition{}, Targets: map[string]string{}},
		},
		enabledUnitsByMode: map[string]map[string]bool{
			visionapi.ModeSystem: {},
			visionapi.ModeUser:   {},
		},
		enabledGroupsByMode: map[string]map[string]bool{
			visionapi.ModeSystem: {},
			visionapi.ModeUser:   {},
		},
		runnerUnitsByMode: map[string]map[string]bool{
			visionapi.ModeSystem: {},
			visionapi.ModeUser:   {},
		},
		enabledInitialized: map[string]bool{},
	}
}
