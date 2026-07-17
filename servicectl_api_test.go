package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"servicectl/internal/statusview"
	"servicectl/internal/visionapi"
)

func TestParseServicectlAPIReadyFD(t *testing.T) {
	for _, args := range [][]string{{"--ready-fd=3"}, {"--ready-fd", "4"}} {
		got, err := parseServicectlAPIReadyFD(args)
		if err != nil {
			t.Fatalf("args=%#v: %v", args, err)
		}
		if got < 3 {
			t.Fatalf("args=%#v ready fd=%d", args, got)
		}
	}
	for _, args := range [][]string{{"--ready-fd=2"}, {"unexpected"}} {
		if _, err := parseServicectlAPIReadyFD(args); err == nil {
			t.Fatalf("args=%#v unexpectedly accepted", args)
		}
	}
}

func TestWriteServicectlAPIReady(t *testing.T) {
	var output bytes.Buffer
	if err := writeServicectlAPIReady(&output); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "\n" {
		t.Fatalf("readiness output = %q", got)
	}
}

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

func TestServicectlAPIAllUnitsUsesCompleteCatalog(t *testing.T) {
	server := newServicectlPlaneServer(visionapi.ModeSystem, newServicectlEventHub())
	server.discoverUnits = func(Config) []string {
		return []string{"discovered", "duplicate.service"}
	}
	server.queryUnitLists = func(string) (visionapi.UnitListsResponse, error) {
		return visionapi.UnitListsResponse{
			EnabledUnits:   []string{"enabled.service", "duplicate"},
			RunnerUnits:    []string{"runner.service"},
			EffectiveUnits: []string{"effective.service"},
		}, nil
	}
	server.collectSnapshots = func(_ Config, units []string) visionapi.UnitsResponse {
		return visionapi.UnitsResponse{Units: []visionapi.UnitSnapshot{{Name: strings.Join(units, ",")}}}
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/units?all=1", nil)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", response.Code, response.Body.String())
	}
	var payload visionapi.UnitsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Units) != 1 || payload.Units[0].Name != "discovered,duplicate,effective,enabled,runner" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestServicectlAPIDefaultUnitsUsesEffectiveList(t *testing.T) {
	server := newServicectlPlaneServer(visionapi.ModeSystem, newServicectlEventHub())
	server.discoverUnits = func(Config) []string {
		t.Fatal("default list must not discover all units")
		return nil
	}
	server.queryUnitLists = func(string) (visionapi.UnitListsResponse, error) {
		return visionapi.UnitListsResponse{
			EnabledUnits:   []string{"enabled.service"},
			RunnerUnits:    []string{"runner.service"},
			EffectiveUnits: []string{"effective.service"},
		}, nil
	}
	server.collectSnapshots = func(_ Config, units []string) visionapi.UnitsResponse {
		return visionapi.UnitsResponse{Units: []visionapi.UnitSnapshot{{Name: strings.Join(units, ",")}}}
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/units", nil)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", response.Code, response.Body.String())
	}
	var payload visionapi.UnitsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Units) != 1 || payload.Units[0].Name != "effective.service" {
		t.Fatalf("payload = %#v", payload)
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

func TestStatusManifestHandler(t *testing.T) {
	server := newServicectlPlaneServer(visionapi.ModeUser, newServicectlEventHub())
	server.queryUnitLists = func(mode string) (visionapi.UnitListsResponse, error) {
		if mode != visionapi.ModeUser {
			t.Fatalf("query mode = %q, want user", mode)
		}
		return visionapi.UnitListsResponse{
			EnabledUnits: []string{"demo.service"},
			GeneratedAt:  "2026-07-12T10:30:00Z",
		}, nil
	}
	server.buildStatusManifest = func(cfg Config, name string, lists visionapi.UnitListsResponse, orchestrator string) (visionapi.StatusParticipationManifest, error) {
		if cfg.Mode != visionapi.ModeUser {
			t.Fatalf("config mode = %q, want user", cfg.Mode)
		}
		if name != "demo.service" {
			t.Fatalf("manifest unit = %q, want demo.service", name)
		}
		if !reflect.DeepEqual(lists.EnabledUnits, []string{"demo.service"}) {
			t.Fatalf("lists = %#v", lists)
		}
		if orchestrator != "demo-orchestrd" {
			t.Fatalf("orchestrator = %q, want demo-orchestrd", orchestrator)
		}
		return testStatusManifest("demo.service", "user@1000"), nil
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/status-manifest/demo.service", nil)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", response.Code, response.Body.String())
	}
	var got visionapi.StatusParticipationManifest
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Version != visionapi.StatusManifestVersion || !got.Complete || got.Scope != "user@1000" {
		t.Fatalf("manifest = %#v", got)
	}
}

func TestStatusManifestHandlerCanonicalizesAlias(t *testing.T) {
	server := newServicectlPlaneServer(visionapi.ModeSystem, newServicectlEventHub())
	server.queryUnitLists = func(string) (visionapi.UnitListsResponse, error) {
		return visionapi.UnitListsResponse{EnabledUnits: []string{"wireplumber.service"}}, nil
	}
	server.canonicalizeStatusUnit = func(_ Config, name string) (string, error) {
		if name != "pipewire-session-manager.service" {
			t.Fatalf("canonicalization input = %q", name)
		}
		return "wireplumber.service", nil
	}
	server.buildStatusManifest = func(_ Config, name string, _ visionapi.UnitListsResponse, orchestrator string) (visionapi.StatusParticipationManifest, error) {
		if name != "wireplumber.service" || orchestrator != "wireplumber-orchestrd" {
			t.Fatalf("manifest input = unit %q orchestrator %q", name, orchestrator)
		}
		return testStatusManifest("wireplumber.service", "system"), nil
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/status-manifest/pipewire-session-manager", nil)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", response.Code, response.Body.String())
	}
	var got visionapi.StatusParticipationManifest
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Unit != "wireplumber.service" {
		t.Fatalf("unit = %q, want wireplumber.service", got.Unit)
	}
}

func TestStatusManifestHandlerCanonicalizationNotFound(t *testing.T) {
	server := newServicectlPlaneServer(visionapi.ModeSystem, newServicectlEventHub())
	server.canonicalizeStatusUnit = func(Config, string) (string, error) {
		return "", errStatusManifestUnitNotFound
	}
	server.queryUnitLists = func(string) (visionapi.UnitListsResponse, error) {
		t.Fatal("unit lists should not be queried for a missing unit")
		return visionapi.UnitListsResponse{}, nil
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/status-manifest/missing", nil)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want %d, body = %s", response.Code, http.StatusNotFound, response.Body.String())
	}
}

func TestStatusManifestHandlerUsesEnabledGroupOwner(t *testing.T) {
	server := newServicectlPlaneServer(visionapi.ModeUser, newServicectlEventHub())
	server.queryUnitLists = func(string) (visionapi.UnitListsResponse, error) {
		return visionapi.UnitListsResponse{
			EnabledGroups:  []string{"pipewire"},
			RunnerUnits:    []string{"pipewire-pulse.service"},
			EffectiveUnits: []string{"pipewire-pulse.service"},
		}, nil
	}
	server.queryUnitGroups = func(mode, unit string) (visionapi.UnitGroupsResponse, error) {
		if mode != visionapi.ModeUser || unit != "pipewire-pulse.service" {
			t.Fatalf("group query = mode %q unit %q", mode, unit)
		}
		return visionapi.UnitGroupsResponse{Unit: unit, Groups: []visionapi.GroupState{{Name: "pipewire", Enabled: true}}}, nil
	}
	server.buildStatusManifest = func(_ Config, name string, _ visionapi.UnitListsResponse, orchestrator string) (visionapi.StatusParticipationManifest, error) {
		if name != "pipewire-pulse.service" || orchestrator != "group-pipewire-orchestrd" {
			t.Fatalf("manifest input = unit %q orchestrator %q", name, orchestrator)
		}
		return testStatusManifest(name, "user@1000"), nil
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/status-manifest/pipewire-pulse.service", nil)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestStatusManifestHandlerRejectsUnavailableGroupOwnership(t *testing.T) {
	server := newServicectlPlaneServer(visionapi.ModeSystem, newServicectlEventHub())
	server.queryUnitLists = func(string) (visionapi.UnitListsResponse, error) {
		return visionapi.UnitListsResponse{EnabledGroups: []string{"workers"}, EffectiveUnits: []string{"demo.service"}}, nil
	}
	server.queryUnitGroups = func(string, string) (visionapi.UnitGroupsResponse, error) {
		return visionapi.UnitGroupsResponse{}, errors.New("group registry unavailable")
	}
	server.buildStatusManifest = func(Config, string, visionapi.UnitListsResponse, string) (visionapi.StatusParticipationManifest, error) {
		t.Fatal("manifest builder should not run without complete ownership data")
		return visionapi.StatusParticipationManifest{}, nil
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/status-manifest/demo.service", nil)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want %d, body = %s", response.Code, http.StatusBadGateway, response.Body.String())
	}
	var payload statusManifestAPIError
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "manager_unavailable" {
		t.Fatalf("error = %#v", payload)
	}
}

func TestStatusOrchestratorFromLists(t *testing.T) {
	tests := []struct {
		name      string
		lists     visionapi.UnitListsResponse
		groups    visionapi.UnitGroupsResponse
		groupsErr error
		want      string
		wantErr   bool
		wantQuery bool
	}{
		{
			name:  "direct unit",
			lists: visionapi.UnitListsResponse{EnabledUnits: []string{"demo.service"}},
			want:  "demo-orchestrd",
		},
		{
			name: "lexically first enabled group owner",
			lists: visionapi.UnitListsResponse{
				EnabledGroups:  []string{"zeta", "alpha"},
				EffectiveUnits: []string{"demo.service"},
			},
			groups: visionapi.UnitGroupsResponse{Groups: []visionapi.GroupState{
				{Name: "zeta", Enabled: true},
				{Name: "alpha", Enabled: true},
			}},
			want: "group-alpha-orchestrd", wantQuery: true,
		},
		{
			name: "runner only unit",
			lists: visionapi.UnitListsResponse{
				EnabledGroups:  []string{"unrelated"},
				RunnerUnits:    []string{"demo.service"},
				EffectiveUnits: []string{"demo.service"},
			},
			groups:    visionapi.UnitGroupsResponse{},
			wantQuery: true,
		},
		{
			name: "group ownership query failure",
			lists: visionapi.UnitListsResponse{
				EnabledGroups:  []string{"workers"},
				EffectiveUnits: []string{"demo.service"},
			},
			groupsErr: errors.New("group registry unavailable"),
			wantErr:   true, wantQuery: true,
		},
		{
			name: "incomplete group ownership",
			lists: visionapi.UnitListsResponse{
				EnabledGroups:  []string{"workers"},
				EffectiveUnits: []string{"demo.service"},
			},
			groups:  visionapi.UnitGroupsResponse{},
			wantErr: true, wantQuery: true,
		},
		{
			name:  "unmanaged unit",
			lists: visionapi.UnitListsResponse{EnabledGroups: []string{"workers"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queried := false
			got, err := statusOrchestratorFromLists(visionapi.ModeSystem, "demo.service", tt.lists, func(mode, unit string) (visionapi.UnitGroupsResponse, error) {
				queried = true
				if mode != visionapi.ModeSystem || unit != "demo.service" {
					t.Fatalf("group query = mode %q unit %q", mode, unit)
				}
				return tt.groups, tt.groupsErr
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("orchestrator = %q, want %q", got, tt.want)
			}
			if queried != tt.wantQuery {
				t.Fatalf("queried groups = %v, want %v", queried, tt.wantQuery)
			}
		})
	}
}

func TestStatusManifestHandlerErrors(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		queryErr   error
		buildErr   error
		wantStatus int
		wantCode   string
	}{
		{name: "method", method: http.MethodPost, path: "/v1/status-manifest/demo", wantStatus: http.StatusMethodNotAllowed, wantCode: "method_not_allowed"},
		{name: "missing unit", method: http.MethodGet, path: "/v1/status-manifest/", wantStatus: http.StatusBadRequest, wantCode: "invalid_unit"},
		{name: "unit not found", method: http.MethodGet, path: "/v1/status-manifest/missing", buildErr: errStatusManifestUnitNotFound, wantStatus: http.StatusNotFound, wantCode: "unit_not_found"},
		{name: "manager failure", method: http.MethodGet, path: "/v1/status-manifest/demo", queryErr: errors.New("property unavailable"), wantStatus: http.StatusBadGateway, wantCode: "manager_unavailable"},
		{name: "build failure", method: http.MethodGet, path: "/v1/status-manifest/demo", buildErr: errors.New("bad configuration"), wantStatus: http.StatusInternalServerError, wantCode: "manifest_build_failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newServicectlPlaneServer(visionapi.ModeSystem, newServicectlEventHub())
			server.queryUnitLists = func(string) (visionapi.UnitListsResponse, error) {
				return visionapi.UnitListsResponse{}, tt.queryErr
			}
			server.buildStatusManifest = func(Config, string, visionapi.UnitListsResponse, string) (visionapi.StatusParticipationManifest, error) {
				if tt.buildErr != nil {
					return visionapi.StatusParticipationManifest{}, tt.buildErr
				}
				return testStatusManifest("demo.service", "system"), nil
			}
			request := httptest.NewRequest(tt.method, tt.path, nil)
			response := httptest.NewRecorder()
			server.handler().ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("code = %d, want %d, body = %s", response.Code, tt.wantStatus, response.Body.String())
			}
			var payload statusManifestAPIError
			if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Error.Code != tt.wantCode || payload.Error.Message == "" {
				t.Fatalf("error = %#v", payload)
			}
		})
	}
}

func TestQueryStatusManifest(t *testing.T) {
	manifest := testStatusManifest("demo.service", "system")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status-manifest/demo.service" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer server.Close()

	got, err := queryStatusManifestWithClient(context.Background(), server.Client(), server.URL, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, manifest) {
		t.Fatalf("manifest = %#v, want %#v", got, manifest)
	}
}

func TestQueryStatusManifestRejectsInvalidResponse(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantKind   statusManifestQueryErrorKind
		wantStatus int
	}{
		{name: "non 200", status: http.StatusNotFound, body: `{"error":{"code":"unit_not_found","message":"missing"}}`, wantKind: statusManifestQueryHTTP, wantStatus: http.StatusNotFound},
		{name: "incomplete", status: http.StatusOK, body: manifestJSONWith(`"complete":false`), wantKind: statusManifestQueryInvalid},
		{name: "unsupported version", status: http.StatusOK, body: manifestJSONWith(`"version":99`), wantKind: statusManifestQueryInvalid},
		{name: "unknown field", status: http.StatusOK, body: manifestJSONWith(`"extra":true`), wantKind: statusManifestQueryDecode},
		{name: "malformed", status: http.StatusOK, body: `{`, wantKind: statusManifestQueryDecode},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			_, err := queryStatusManifestWithClient(context.Background(), server.Client(), server.URL, "demo")
			var queryErr *statusManifestQueryError
			if !errors.As(err, &queryErr) {
				t.Fatalf("error = %T %v, want statusManifestQueryError", err, err)
			}
			if queryErr.Kind != tt.wantKind || queryErr.StatusCode != tt.wantStatus {
				t.Fatalf("query error = %#v", queryErr)
			}
		})
	}
}

func TestQueryStatusManifestPropagatesTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := queryStatusManifestWithClient(ctx, server.Client(), server.URL, "demo")
	var queryErr *statusManifestQueryError
	if !errors.As(err, &queryErr) || queryErr.Kind != statusManifestQueryTransport {
		t.Fatalf("error = %T %v, want transport query error", err, err)
	}
}

func testStatusManifest(unit, scope string) visionapi.StatusParticipationManifest {
	mode := visionapi.ModeSystem
	uid := uint32(0)
	if strings.HasPrefix(scope, "user@") {
		mode = visionapi.ModeUser
		_, _ = fmt.Sscanf(scope, "user@%d", &uid)
	}
	serviceKey, err := statusview.NewNodeID("service", scope, unit)
	if err != nil {
		panic(err)
	}
	return visionapi.StatusParticipationManifest{
		Version:     visionapi.StatusManifestVersion,
		Complete:    true,
		Unit:        unit,
		Mode:        mode,
		UID:         uid,
		Scope:       scope,
		Source:      "manager",
		GeneratedAt: "2026-07-12T10:30:00Z",
		Namespaces: []visionapi.StatusManifestNamespace{
			{Name: visionapi.StatusNamespaceAccounting, Complete: true},
			{Name: visionapi.StatusNamespaceBus, Complete: true},
			{Name: visionapi.StatusNamespaceControl, Complete: true},
			{Name: visionapi.StatusNamespaceObservation, Complete: true},
		},
		Components:    []visionapi.StatusManifestComponent{{Key: serviceKey, Type: "service", Name: unit, Scope: scope, Identity: unit, ServiceName: unit}},
		Relationships: []visionapi.StatusManifestRelationship{},
	}
}

func manifestJSONWith(field string) string {
	base := `{"version":1,"complete":true,"unit":"demo.service","mode":"system","uid":0,"scope":"system","source":"manager","generation":0,"generated_at":"2026-07-12T10:30:00Z","namespaces":[{"name":"accounting","applicable":false,"complete":true},{"name":"bus","applicable":false,"complete":true},{"name":"control","applicable":false,"complete":true},{"name":"observation","applicable":false,"complete":true}],"components":[{"key":"service:system:demo.service","type":"service","name":"demo.service","scope":"system","identity":"demo.service"}],"relationships":[]}`
	if strings.Contains(field, `"complete"`) {
		return strings.Replace(base, `"complete":true`, field, 1)
	}
	if strings.Contains(field, `"version"`) {
		return strings.Replace(base, `"version":1`, field, 1)
	}
	return strings.TrimSuffix(base, "}") + "," + field + "}"
}
