package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"servicectl/internal/util"
	"servicectl/internal/visionapi"
)

type propertyStore struct {
	Persistent    map[string]string            `json:"persistent"`
	RuntimeByMode map[string]map[string]string `json:"runtime_by_mode"`
}

type groupDefinition struct {
	Name    string
	Units   []string
	Targets []string
}

type definitionSet struct {
	Groups  map[string]groupDefinition
	Targets map[string]string
}

type daemon struct {
	runtimeDir          string
	statePath           string
	logger              *syncLogger
	mu                  sync.Mutex
	store               propertyStore
	defsByMode          map[string]definitionSet
	enabledUnitsByMode  map[string]map[string]bool
	enabledGroupsByMode map[string]map[string]bool
	runnerUnitsByMode   map[string]map[string]bool
	enabledInitialized  map[string]bool
	readyWriter         io.Writer
	readySent           bool
}

type propertyUpdateRequest struct {
	Key        string `json:"key"`
	Value      string `json:"value"`
	Persistent bool   `json:"persistent"`
	Mode       string `json:"mode,omitempty"`
}

type resolveTargetResponse struct {
	Input  string `json:"input"`
	Group  string `json:"group"`
	Target string `json:"target,omitempty"`
}

type syncLogger struct{ mu sync.Mutex }

func (l *syncLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(os.Stdout, "sys-propertyd: "+format+"\n", args...)
}

func main() {
	userMode := flag.Bool("user", false, "deprecated no-op")
	readyFD := flag.Int("ready-fd", -1, "write a newline to this file descriptor after the API socket is ready")
	flag.Parse()
	_ = *userMode
	if *readyFD >= 0 && *readyFD < 3 {
		fmt.Fprintln(os.Stderr, "ready fd must be at least 3")
		os.Exit(1)
	}
	d := &daemon{
		runtimeDir: visionapi.SystemRuntimeDir,
		statePath:  "/var/lib/servicectl/properties/state",
		logger:     &syncLogger{},
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
	if *readyFD >= 3 {
		d.readyWriter = os.NewFile(uintptr(*readyFD), "sys-propertyd-ready")
	}
	if err := d.run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (d *daemon) setEnabledUnit(mode, unit string, enabled bool) error {
	mode, err := normalizeMode(mode)
	if err != nil {
		return err
	}
	unit = normalizeUnitName(unit)
	if unit == "" {
		return fmt.Errorf("unit is required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	changed := d.enabledUnitsLocked(mode)[unit] != enabled
	initialized := d.enabledInitialized[mode]
	if !changed && initialized {
		return nil
	}
	d.enabledInitialized[mode] = true
	setMembership(d.enabledUnitsLocked(mode), unit, enabled)
	if err := d.saveStateLocked(); err != nil {
		return err
	}
	d.publishModeLocked(mode, visionapi.KindUnitListChanged, map[string]string{"list": "enabled", "unit": unit, "present": util.YesNo(enabled)})
	return nil
}

func (d *daemon) replaceEnabledList(mode string, units []string) error {
	mode, err := normalizeMode(mode)
	if err != nil {
		return err
	}
	replacement := make(map[string]bool)
	for _, unit := range units {
		if normalized := normalizeUnitName(unit); normalized != "" {
			replacement[normalized] = true
		}
	}
	d.mu.Lock()
	changed := !membershipEqual(d.enabledUnitsLocked(mode), replacement)
	initialized := d.enabledInitialized[mode]
	if !changed && initialized {
		d.mu.Unlock()
		return nil
	}
	d.enabledInitialized[mode] = true
	d.enabledUnitsByMode[mode] = replacement
	if err := d.saveStateLocked(); err != nil {
		d.mu.Unlock()
		return err
	}
	d.publishModeLocked(mode, visionapi.KindUnitListChanged, map[string]string{"list": "enabled", "replace": "yes"})
	d.mu.Unlock()
	return nil
}

func (d *daemon) setEnabledGroup(mode, group string, enabled bool) error {
	mode, err := normalizeMode(mode)
	if err != nil {
		return err
	}
	group = strings.TrimSpace(group)
	if group == "" {
		return fmt.Errorf("group is required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	changed := d.enabledGroupsLocked(mode)[group] != enabled
	initialized := d.enabledInitialized[mode]
	if !changed && initialized {
		return nil
	}
	d.enabledInitialized[mode] = true
	delete(d.store.Persistent, "persist.group."+group)
	setMembership(d.enabledGroupsLocked(mode), group, enabled)
	if err := d.saveStateLocked(); err != nil {
		return err
	}
	d.publishModeLocked(mode, visionapi.KindUnitListChanged, map[string]string{"list": "enabled-groups", "group": group, "present": util.YesNo(enabled)})
	return nil
}

func (d *daemon) replaceRunnerList(mode string, units []string) error {
	mode, err := normalizeMode(mode)
	if err != nil {
		return err
	}
	replacement := make(map[string]bool)
	for _, unit := range units {
		if normalized := normalizeUnitName(unit); normalized != "" {
			replacement[normalized] = true
		}
	}
	d.mu.Lock()
	if membershipEqual(d.runnerUnitsLocked(mode), replacement) {
		d.mu.Unlock()
		return nil
	}
	d.runnerUnitsByMode[mode] = replacement
	d.publishModeLocked(mode, visionapi.KindUnitListChanged, map[string]string{"list": "runner", "replace": "yes"})
	d.mu.Unlock()
	return nil
}

func (d *daemon) setRunnerUnit(mode, unit string, running bool) error {
	mode, err := normalizeMode(mode)
	if err != nil {
		return err
	}
	unit = normalizeUnitName(unit)
	if unit == "" {
		return fmt.Errorf("unit is required")
	}
	d.mu.Lock()
	if d.runnerUnitsLocked(mode)[unit] == running {
		d.mu.Unlock()
		return nil
	}
	setMembership(d.runnerUnitsLocked(mode), unit, running)
	d.publishModeLocked(mode, visionapi.KindUnitListChanged, map[string]string{"list": "runner", "unit": unit, "present": util.YesNo(running)})
	d.mu.Unlock()
	return nil
}

func (d *daemon) unitLists(mode string) visionapi.UnitListsResponse {
	d.mu.Lock()
	defer d.mu.Unlock()
	enabledUnits := sortedMembership(d.enabledUnitsLocked(mode))
	enabledGroups := sortedMembership(d.enabledGroupsLocked(mode))
	runnerUnits := sortedMembership(d.runnerUnitsLocked(mode))
	effective := make(map[string]bool, len(enabledUnits)+len(runnerUnits))
	for _, unit := range enabledUnits {
		effective[unit] = true
	}
	for _, unit := range runnerUnits {
		effective[unit] = true
	}
	definitions := d.definitionsLocked(mode)
	for _, group := range enabledGroups {
		for _, unit := range definitions.Groups[group].Units {
			if normalized := normalizeUnitName(unit); normalized != "" {
				effective[normalized] = true
			}
		}
	}
	return visionapi.UnitListsResponse{
		Mode: mode, EnabledInitialized: d.enabledInitialized[mode], EnabledUnits: enabledUnits, EnabledGroups: enabledGroups,
		RunnerUnits: runnerUnits, EffectiveUnits: sortedMembership(effective),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func (d *daemon) enabledUnitsLocked(mode string) map[string]bool {
	if d.enabledUnitsByMode[mode] == nil {
		d.enabledUnitsByMode[mode] = make(map[string]bool)
	}
	return d.enabledUnitsByMode[mode]
}

func (d *daemon) enabledGroupsLocked(mode string) map[string]bool {
	if d.enabledGroupsByMode[mode] == nil {
		d.enabledGroupsByMode[mode] = make(map[string]bool)
	}
	return d.enabledGroupsByMode[mode]
}

func (d *daemon) runnerUnitsLocked(mode string) map[string]bool {
	if d.runnerUnitsByMode[mode] == nil {
		d.runnerUnitsByMode[mode] = make(map[string]bool)
	}
	return d.runnerUnitsByMode[mode]
}

func setMembership(values map[string]bool, name string, present bool) {
	if present {
		values[name] = true
	} else {
		delete(values, name)
	}
}

func membershipEqual(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for name, present := range left {
		if present != right[name] {
			return false
		}
	}
	return true
}

func sortedMembership(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for name, present := range values {
		if present {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func (d *daemon) run() error {
	if err := os.MkdirAll(filepath.Dir(d.statePath), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(d.runtimeDir, 0755); err != nil {
		return err
	}
	if err := d.reloadDefinitionsLocked(visionapi.ModeSystem); err != nil {
		return err
	}
	if err := d.reloadDefinitionsLocked(visionapi.ModeUser); err != nil {
		return err
	}
	if err := d.loadState(); err != nil {
		return err
	}
	socketPath := visionapi.SystemPropertySocketPath()
	if err := visionapi.PrepareUnixStreamListener(socketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0660); err != nil {
		return err
	}
	if err := d.notifyReady(); err != nil {
		return err
	}
	server := &http.Server{Handler: d.handler()}
	return server.Serve(listener)
}

func (d *daemon) notifyReady() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.readySent || d.readyWriter == nil {
		return nil
	}
	if _, err := io.WriteString(d.readyWriter, "\n"); err != nil {
		return err
	}
	d.readySent = true
	return nil
}

func (d *daemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/properties", d.handleProperties)
	mux.HandleFunc("/v1/groups", d.handleGroups)
	mux.HandleFunc("/v1/group/", d.handleGroup)
	mux.HandleFunc("/v1/unit-groups/", d.handleUnitGroups)
	mux.HandleFunc("/v1/resolve-target", d.handleResolveTarget)
	mux.HandleFunc("/v1/reload", d.handleReload)
	mux.HandleFunc("/v1/property", d.handlePropertyUpdate)
	mux.HandleFunc("/v1/unit-lists", d.handleUnitLists)
	mux.HandleFunc("/v1/enabled-list", d.handleEnabledList)
	mux.HandleFunc("/v1/enabled-unit", d.handleEnabledUnit)
	mux.HandleFunc("/v1/enabled-group", d.handleEnabledGroup)
	mux.HandleFunc("/v1/runner-list", d.handleRunnerList)
	mux.HandleFunc("/v1/runner-unit", d.handleRunnerUnit)
	return mux
}

func (d *daemon) handleEnabledList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request visionapi.UnitListReplaceRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writePropertyError(w, http.StatusBadRequest, err)
		return
	}
	if err := d.replaceEnabledList(request.Mode, request.Units); err != nil {
		writePropertyError(w, http.StatusBadRequest, err)
		return
	}
	util.WriteJSON(w, map[string]string{"result": "ok"})
}

func (d *daemon) handleUnitLists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mode, err := requestMode(r)
	if err != nil {
		writePropertyError(w, http.StatusBadRequest, err)
		return
	}
	util.WriteJSON(w, d.unitLists(mode))
}

func (d *daemon) handleEnabledUnit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request visionapi.UnitListMembershipRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writePropertyError(w, http.StatusBadRequest, err)
		return
	}
	if err := d.setEnabledUnit(request.Mode, request.Unit, request.Present); err != nil {
		writePropertyError(w, http.StatusBadRequest, err)
		return
	}
	util.WriteJSON(w, map[string]string{"result": "ok"})
}

func (d *daemon) handleEnabledGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request visionapi.UnitListMembershipRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writePropertyError(w, http.StatusBadRequest, err)
		return
	}
	if err := d.setEnabledGroup(request.Mode, request.Group, request.Present); err != nil {
		writePropertyError(w, http.StatusBadRequest, err)
		return
	}
	util.WriteJSON(w, map[string]string{"result": "ok"})
}

func (d *daemon) handleRunnerList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request visionapi.UnitListReplaceRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writePropertyError(w, http.StatusBadRequest, err)
		return
	}
	if err := d.replaceRunnerList(request.Mode, request.Units); err != nil {
		writePropertyError(w, http.StatusBadRequest, err)
		return
	}
	util.WriteJSON(w, map[string]string{"result": "ok"})
}

func (d *daemon) handleRunnerUnit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request visionapi.UnitListMembershipRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writePropertyError(w, http.StatusBadRequest, err)
		return
	}
	if err := d.setRunnerUnit(request.Mode, request.Unit, request.Present); err != nil {
		writePropertyError(w, http.StatusBadRequest, err)
		return
	}
	util.WriteJSON(w, map[string]string{"result": "ok"})
}

func writePropertyError(w http.ResponseWriter, status int, err error) {
	w.WriteHeader(status)
	util.WriteJSON(w, map[string]string{"error": err.Error()})
}

func (d *daemon) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mode, err := requestMode(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		util.WriteJSON(w, map[string]string{"error": err.Error()})
		return
	}
	d.mu.Lock()
	err = d.reloadDefinitionsLocked(mode)
	d.mu.Unlock()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		util.WriteJSON(w, map[string]string{"error": err.Error()})
		return
	}
	util.WriteJSON(w, map[string]string{"result": "ok"})
}

func (d *daemon) handleProperties(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mode, err := requestMode(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		util.WriteJSON(w, map[string]string{"error": err.Error()})
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	runtimeState := d.runtimeStateLocked(mode)
	keys := make([]string, 0, len(d.store.Persistent)+len(runtimeState))
	seen := make(map[string]bool)
	for key := range d.store.Persistent {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	for key := range runtimeState {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	resp := visionapi.PropertiesResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, key := range keys {
		value, persistent := d.effectiveValueLocked(mode, key)
		resp.Properties = append(resp.Properties, visionapi.PropertyState{Key: key, Value: value, Persistent: persistent})
	}
	util.WriteJSON(w, resp)
}

func (d *daemon) handleGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mode, err := requestMode(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		util.WriteJSON(w, map[string]string{"error": err.Error()})
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	defs := d.definitionsLocked(mode)
	names := make([]string, 0, len(defs.Groups))
	for name := range defs.Groups {
		names = append(names, name)
	}
	sort.Strings(names)
	resp := visionapi.GroupsResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, name := range names {
		resp.Groups = append(resp.Groups, d.groupStateLocked(mode, name))
	}
	util.WriteJSON(w, resp)
}

func (d *daemon) handleGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mode, err := requestMode(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		util.WriteJSON(w, map[string]string{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/group/"))
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	defs := d.definitionsLocked(mode)
	if _, ok := defs.Groups[name]; !ok {
		w.WriteHeader(http.StatusNotFound)
		util.WriteJSON(w, map[string]string{"error": "group not found"})
		return
	}
	util.WriteJSON(w, d.groupStateLocked(mode, name))
}

func (d *daemon) handleUnitGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mode, err := requestMode(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		util.WriteJSON(w, map[string]string{"error": err.Error()})
		return
	}
	unit := normalizeUnitName(strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/unit-groups/")))
	if unit == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	defs := d.definitionsLocked(mode)
	resp := visionapi.UnitGroupsResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Unit: unit}
	for _, name := range sortedGroupNames(defs.Groups) {
		group := d.groupStateLocked(mode, name)
		if groupContainsUnit(group, unit) {
			resp.Groups = append(resp.Groups, group)
		}
	}
	util.WriteJSON(w, resp)
}

func (d *daemon) handleResolveTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mode, err := requestMode(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		util.WriteJSON(w, map[string]string{"error": err.Error()})
		return
	}
	input := strings.TrimSpace(r.URL.Query().Get("name"))
	d.mu.Lock()
	defer d.mu.Unlock()
	group, ok := d.resolveVirtualLocked(mode, input)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		util.WriteJSON(w, map[string]string{"error": "target/group not found"})
		return
	}
	util.WriteJSON(w, resolveTargetResponse{Input: input, Group: group, Target: input})
}

func (d *daemon) handlePropertyUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req propertyUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		util.WriteJSON(w, map[string]string{"error": err.Error()})
		return
	}
	if err := d.setProperty(strings.TrimSpace(req.Key), strings.TrimSpace(req.Value), req.Persistent, strings.TrimSpace(req.Mode)); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		util.WriteJSON(w, map[string]string{"error": err.Error()})
		return
	}
	util.WriteJSON(w, map[string]string{"result": "ok"})
}

func (d *daemon) setProperty(key string, value string, persistent bool, mode string) error {
	if key == "" {
		return fmt.Errorf("property key is required")
	}
	if !persistent {
		var err error
		mode, err = normalizeMode(mode)
		if err != nil {
			return err
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if persistent && strings.HasPrefix(key, "persist.group.") {
		group := strings.TrimPrefix(key, "persist.group.")
		enabled := util.ExternalManagedValueEnabled(value)
		setMembership(d.enabledGroupsLocked(visionapi.ModeSystem), group, enabled)
		setMembership(d.enabledGroupsLocked(visionapi.ModeUser), group, enabled)
		d.enabledInitialized[visionapi.ModeSystem] = true
		d.enabledInitialized[visionapi.ModeUser] = true
		delete(d.store.Persistent, key)
		if err := d.saveStateLocked(); err != nil {
			return err
		}
		if enabled {
			value = "1"
		} else {
			value = "0"
		}
		d.publishPropertyChangeLocked(key, value, true, mode)
		return nil
	}
	store := d.store.Persistent
	if !persistent {
		store = d.runtimeStateLocked(mode)
	}
	if value == "" || value == "0" || strings.EqualFold(value, "false") {
		delete(store, key)
		value = "0"
	} else {
		store[key] = value
	}
	if err := d.saveStateLocked(); err != nil {
		return err
	}
	d.publishPropertyChangeLocked(key, value, persistent, mode)
	return nil
}

func (d *daemon) loadState() error {
	data, err := os.ReadFile(d.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch {
		case strings.HasPrefix(key, "enable.system.unit."):
			d.enabledInitialized[visionapi.ModeSystem] = true
			d.enabledUnitsLocked(visionapi.ModeSystem)[strings.TrimPrefix(key, "enable.system.unit.")] = util.ExternalManagedValueEnabled(value)
		case strings.HasPrefix(key, "enable.user.unit."):
			d.enabledInitialized[visionapi.ModeUser] = true
			d.enabledUnitsLocked(visionapi.ModeUser)[strings.TrimPrefix(key, "enable.user.unit.")] = util.ExternalManagedValueEnabled(value)
		case strings.HasPrefix(key, "enable.system.group."):
			d.enabledInitialized[visionapi.ModeSystem] = true
			d.enabledGroupsLocked(visionapi.ModeSystem)[strings.TrimPrefix(key, "enable.system.group.")] = util.ExternalManagedValueEnabled(value)
		case strings.HasPrefix(key, "enable.user.group."):
			d.enabledInitialized[visionapi.ModeUser] = true
			d.enabledGroupsLocked(visionapi.ModeUser)[strings.TrimPrefix(key, "enable.user.group.")] = util.ExternalManagedValueEnabled(value)
		case strings.HasPrefix(key, "persist.group."):
			group := strings.TrimPrefix(key, "persist.group.")
			enabled := util.ExternalManagedValueEnabled(value)
			d.enabledGroupsLocked(visionapi.ModeSystem)[group] = enabled
			d.enabledGroupsLocked(visionapi.ModeUser)[group] = enabled
			d.enabledInitialized[visionapi.ModeSystem] = true
			d.enabledInitialized[visionapi.ModeUser] = true
		case key == "enable.system.initialized":
			d.enabledInitialized[visionapi.ModeSystem] = util.ExternalManagedValueEnabled(value)
		case key == "enable.user.initialized":
			d.enabledInitialized[visionapi.ModeUser] = util.ExternalManagedValueEnabled(value)
		case strings.HasPrefix(key, "persist."):
			d.store.Persistent[key] = value
		case strings.HasPrefix(key, "prop.system."):
			d.runtimeStateLocked(visionapi.ModeSystem)[strings.TrimPrefix(key, "prop.system.")] = value
		case strings.HasPrefix(key, "prop.user."):
			d.runtimeStateLocked(visionapi.ModeUser)[strings.TrimPrefix(key, "prop.user.")] = value
		case strings.HasPrefix(key, "prop."):
			d.runtimeStateLocked(visionapi.ModeSystem)[key] = value
		}
	}
	return scanner.Err()
}

func (d *daemon) saveStateLocked() error {
	lines := make([]string, 0, len(d.store.Persistent)+len(d.runtimeStateLocked(visionapi.ModeSystem))+len(d.runtimeStateLocked(visionapi.ModeUser)))
	for _, key := range sortedMapKeys(d.store.Persistent) {
		lines = append(lines, key+"="+d.store.Persistent[key])
	}
	for _, key := range sortedMapKeys(d.runtimeStateLocked(visionapi.ModeSystem)) {
		lines = append(lines, "prop.system."+key+"="+d.runtimeStateLocked(visionapi.ModeSystem)[key])
	}
	for _, key := range sortedMapKeys(d.runtimeStateLocked(visionapi.ModeUser)) {
		lines = append(lines, "prop.user."+key+"="+d.runtimeStateLocked(visionapi.ModeUser)[key])
	}
	for _, mode := range []string{visionapi.ModeSystem, visionapi.ModeUser} {
		if d.enabledInitialized[mode] {
			lines = append(lines, "enable."+mode+".initialized=1")
		}
		for _, unit := range sortedMembership(d.enabledUnitsLocked(mode)) {
			lines = append(lines, "enable."+mode+".unit."+unit+"=1")
		}
		for _, group := range sortedMembership(d.enabledGroupsLocked(mode)) {
			lines = append(lines, "enable."+mode+".group."+group+"=1")
		}
	}
	sort.Strings(lines)
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(d.statePath, []byte(content), 0644)
}

func (d *daemon) reloadDefinitionsLocked(mode string) error {
	autoGroups, autoTargets, err := importSystemdTargets(systemdUnitDirs(mode))
	if err != nil {
		return err
	}
	groupDirs, targetDirs := propertyDefinitionDirs(mode)
	explicitGroups := map[string]groupDefinition{}
	explicitTargets := map[string]string{}
	for _, dir := range groupDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
				continue
			}
			defs, err := parseGroupConfig(filepath.Join(dir, entry.Name()))
			if err != nil {
				return err
			}
			for _, def := range defs {
				explicitGroups[def.Name] = def
				for _, target := range def.Targets {
					explicitTargets[target] = def.Name
				}
			}
		}
	}
	for _, dir := range targetDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
				continue
			}
			aliases, err := parseTargetConfig(filepath.Join(dir, entry.Name()))
			if err != nil {
				return err
			}
			for target, group := range aliases {
				explicitTargets[target] = group
			}
		}
	}
	groups := overrideGroups(autoGroups, explicitGroups)
	targets := overrideTargets(autoTargets, explicitTargets)
	if err := validateTargetGroups(groups, targets); err != nil {
		return err
	}
	d.defsByMode[mode] = definitionSet{Groups: groups, Targets: targets}
	return nil
}

func propertyDefinitionDirs(mode string) ([]string, []string) {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if mode == visionapi.ModeUser {
		return []string{filepath.Join(home, ".config/servicectl/groups.d"), "/usr/lib/servicectl/groups.d"}, []string{filepath.Join(home, ".config/servicectl/targets.d"), "/usr/lib/servicectl/targets.d"}
	}
	return []string{"/etc/servicectl/groups.d", "/usr/lib/servicectl/groups.d"}, []string{"/etc/servicectl/targets.d", "/usr/lib/servicectl/targets.d"}
}

func parseGroupConfig(path string) ([]groupDefinition, error) {
	lines, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defs := []groupDefinition{}
	current := groupDefinition{}
	section := ""
	flush := func() {
		if strings.TrimSpace(current.Name) == "" {
			return
		}
		current.Units = uniquePreserveOrderStrings(current.Units)
		current.Targets = uniqueSortedStrings(current.Targets)
		defs = append(defs, current)
		current = groupDefinition{}
	}
	scanner := bufio.NewScanner(strings.NewReader(string(lines)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if section == "Group" {
				flush()
			}
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		if section != "Group" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "Name":
			current.Name = value
		case "Units":
			current.Units = append(current.Units, strings.Fields(value)...)
		case "Targets":
			current.Targets = append(current.Targets, strings.Fields(value)...)
		}
	}
	if section == "Group" {
		flush()
	}
	return defs, scanner.Err()
}

func parseTargetConfig(path string) (map[string]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	aliases := map[string]string{}
	section := ""
	name := ""
	group := ""
	flush := func() {
		if strings.TrimSpace(name) != "" && strings.TrimSpace(group) != "" {
			aliases[name] = group
		}
		name = ""
		group = ""
	}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if section == "Target" {
				flush()
			}
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		if section != "Target" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "Name":
			name = value
		case "Group":
			group = value
		}
	}
	if section == "Target" {
		flush()
	}
	return aliases, scanner.Err()
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func uniquePreserveOrderStrings(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func (d *daemon) resolveVirtualLocked(mode string, input string) (string, bool) {
	defs := d.definitionsLocked(mode)
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "group:") {
		group := strings.TrimSpace(strings.TrimPrefix(input, "group:"))
		_, ok := defs.Groups[group]
		return group, ok
	}
	group, ok := defs.Targets[input]
	return group, ok
}

func (d *daemon) effectiveValueLocked(mode string, key string) (string, bool) {
	if value, ok := d.runtimeStateLocked(mode)[key]; ok {
		return value, false
	}
	if value, ok := d.store.Persistent[key]; ok {
		return value, true
	}
	return "", false
}

func (d *daemon) groupStateLocked(mode string, name string) visionapi.GroupState {
	defs := d.definitionsLocked(mode)
	def := defs.Groups[name]
	value := strings.TrimSpace(d.runtimeStateLocked(mode)["prop.group."+name])
	persistent := false
	if value == "" {
		if d.enabledGroupsLocked(mode)[name] {
			value = "1"
			persistent = true
		}
	}
	return visionapi.GroupState{
		Name:       name,
		Units:      append([]string{}, def.Units...),
		Enabled:    util.ExternalManagedValueEnabled(value),
		Persistent: persistent,
		Targets:    append([]string{}, def.Targets...),
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func (d *daemon) publishPropertyChangeLocked(key string, value string, persistent bool, mode string) {
	payload := map[string]string{"key": key, "value": value, "persistent": util.YesNo(persistent)}
	if !persistent {
		d.publishModeLocked(mode, visionapi.KindPropertyChanged, payload)
	} else {
		d.publishModeLocked(visionapi.ModeSystem, visionapi.KindPropertyChanged, payload)
		d.publishModeLocked(visionapi.ModeUser, visionapi.KindPropertyChanged, payload)
	}
	if group := strings.TrimPrefix(key, "persist.group."); group != key {
		groupPayload := map[string]string{"group": group, "enabled": util.YesNo(value == "1"), "persistent": "yes"}
		d.publishModeLocked(visionapi.ModeSystem, visionapi.KindGroupChanged, groupPayload)
		d.publishModeLocked(visionapi.ModeUser, visionapi.KindGroupChanged, groupPayload)
	}
	if group := strings.TrimPrefix(key, "prop.group."); group != key {
		d.publishModeLocked(mode, visionapi.KindGroupChanged, map[string]string{"group": group, "enabled": util.YesNo(value == "1"), "persistent": "no"})
	}
}

func (d *daemon) publishModeLocked(mode string, kind string, payload map[string]string) {
	envelope := visionapi.NewEvent(mode, visionapi.SourceSysPropertyd, kind, "", payload)
	data, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	addr := &net.UnixAddr{Name: visionapi.SysvisionIngressSocketPathForMode(mode), Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write(data)
}

func (d *daemon) runtimeStateLocked(mode string) map[string]string {
	state, ok := d.store.RuntimeByMode[mode]
	if !ok {
		state = map[string]string{}
		d.store.RuntimeByMode[mode] = state
	}
	return state
}

func (d *daemon) definitionsLocked(mode string) definitionSet {
	defs, ok := d.defsByMode[mode]
	if !ok {
		return definitionSet{Groups: map[string]groupDefinition{}, Targets: map[string]string{}}
	}
	return defs
}

func normalizeMode(mode string) (string, error) {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == visionapi.ModeSystem || mode == visionapi.ModeUser {
		return mode, nil
	}
	return "", fmt.Errorf("mode is required")
}

func requestMode(r *http.Request) (string, error) {
	return normalizeMode(r.URL.Query().Get("mode"))
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedGroupNames(values map[string]groupDefinition) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeUnitName(name string) string {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return ""
	}
	if !strings.HasSuffix(clean, ".service") {
		clean += ".service"
	}
	return clean
}

func groupContainsUnit(group visionapi.GroupState, unit string) bool {
	for _, candidate := range group.Units {
		if normalizeUnitName(candidate) == unit {
			return true
		}
	}
	return false
}
