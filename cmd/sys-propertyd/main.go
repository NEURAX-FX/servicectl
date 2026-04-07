package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"servicectl/internal/visionapi"
)

type propertyState struct {
	Persistent map[string]string `json:"persistent"`
	Runtime    map[string]string `json:"runtime"`
}

type groupDefinition struct {
	Name    string
	Units   []string
	Targets []string
}

type daemon struct {
	mode       string
	userMode   bool
	runtimeDir string
	statePath  string
	groupDirs  []string
	targetDirs []string
	logger     *syncLogger
	mu         sync.Mutex
	state      propertyState
	groups     map[string]groupDefinition
	targets    map[string]string
}

type propertyUpdateRequest struct {
	Key        string `json:"key"`
	Value      string `json:"value"`
	Persistent bool   `json:"persistent"`
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
	userMode := flag.Bool("user", false, "run in user mode")
	flag.Parse()
	runtimeDir := visionapi.RuntimeDir(*userMode, os.Getenv("XDG_RUNTIME_DIR"))
	home := strings.TrimSpace(os.Getenv("HOME"))
	statePath := "/var/lib/servicectl/properties/state"
	groupDirs := []string{"/etc/servicectl/groups.d"}
	targetDirs := []string{"/etc/servicectl/targets.d"}
	if *userMode {
		statePath = filepath.Join(home, ".local/state/servicectl/properties/state")
		groupDirs = []string{filepath.Join(home, ".config/servicectl/groups.d"), "/usr/lib/servicectl/groups.d"}
		targetDirs = []string{filepath.Join(home, ".config/servicectl/targets.d"), "/usr/lib/servicectl/targets.d"}
	} else {
		groupDirs = append(groupDirs, "/usr/lib/servicectl/groups.d")
		targetDirs = append(targetDirs, "/usr/lib/servicectl/targets.d")
	}
	d := &daemon{
		mode:       visionapi.ModeForUser(*userMode),
		userMode:   *userMode,
		runtimeDir: runtimeDir,
		statePath:  statePath,
		groupDirs:  groupDirs,
		targetDirs: targetDirs,
		logger:     &syncLogger{},
		state:      propertyState{Persistent: map[string]string{}, Runtime: map[string]string{}},
		groups:     map[string]groupDefinition{},
		targets:    map[string]string{},
	}
	if err := d.run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (d *daemon) run() error {
	if err := os.MkdirAll(filepath.Dir(d.statePath), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(d.runtimeDir, 0755); err != nil {
		return err
	}
	if err := d.reloadDefinitions(); err != nil {
		return err
	}
	if err := d.loadState(); err != nil {
		return err
	}
	socketPath := visionapi.PropertySocketPathForMode(d.mode)
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
	_ = os.Chmod(socketPath, 0660)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/properties", d.handleProperties)
	mux.HandleFunc("/v1/groups", d.handleGroups)
	mux.HandleFunc("/v1/group/", d.handleGroup)
	mux.HandleFunc("/v1/unit-groups/", d.handleUnitGroups)
	mux.HandleFunc("/v1/resolve-target", d.handleResolveTarget)
	mux.HandleFunc("/v1/reload", d.handleReload)
	mux.HandleFunc("/v1/property", d.handlePropertyUpdate)
	server := &http.Server{Handler: mux}
	return server.Serve(listener)
}

func (d *daemon) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	err := d.reloadDefinitions()
	d.mu.Unlock()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

func (d *daemon) handleProperties(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]string, 0, len(d.state.Persistent)+len(d.state.Runtime))
	seen := make(map[string]bool)
	for key := range d.state.Persistent {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	for key := range d.state.Runtime {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	resp := visionapi.PropertiesResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, key := range keys {
		value, persistent := d.effectiveValueLocked(key)
		resp.Properties = append(resp.Properties, visionapi.PropertyState{Key: key, Value: value, Persistent: persistent})
	}
	writeJSON(w, resp)
}

func (d *daemon) handleGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	names := make([]string, 0, len(d.groups))
	for name := range d.groups {
		names = append(names, name)
	}
	sort.Strings(names)
	resp := visionapi.GroupsResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, name := range names {
		resp.Groups = append(resp.Groups, d.groupStateLocked(name))
	}
	writeJSON(w, resp)
}

func (d *daemon) handleGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/group/"))
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.groups[name]; !ok {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "group not found"})
		return
	}
	writeJSON(w, d.groupStateLocked(name))
}

func (d *daemon) handleUnitGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	unit := normalizeUnitName(strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/unit-groups/")))
	if unit == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	resp := visionapi.UnitGroupsResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Unit: unit}
	for _, name := range sortedGroupNames(d.groups) {
		group := d.groupStateLocked(name)
		if groupContainsUnit(group, unit) {
			resp.Groups = append(resp.Groups, group)
		}
	}
	writeJSON(w, resp)
}

func (d *daemon) handleResolveTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	input := strings.TrimSpace(r.URL.Query().Get("name"))
	group, ok := d.resolveVirtualLocked(input)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "target/group not found"})
		return
	}
	writeJSON(w, resolveTargetResponse{Input: input, Group: group, Target: input})
}

func (d *daemon) handlePropertyUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req propertyUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	if err := d.setProperty(strings.TrimSpace(req.Key), strings.TrimSpace(req.Value), req.Persistent); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

func (d *daemon) setProperty(key string, value string, persistent bool) error {
	if key == "" {
		return fmt.Errorf("property key is required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	store := d.state.Runtime
	if persistent {
		store = d.state.Persistent
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
	d.publishLocked(visionapi.KindPropertyChanged, map[string]string{"key": key, "value": value, "persistent": yesNo(persistent)})
	if group := strings.TrimPrefix(key, "persist.group."); group != key {
		d.publishLocked(visionapi.KindGroupChanged, map[string]string{"group": group, "enabled": yesNo(value == "1"), "persistent": "yes"})
	}
	if group := strings.TrimPrefix(key, "prop.group."); group != key {
		d.publishLocked(visionapi.KindGroupChanged, map[string]string{"group": group, "enabled": yesNo(value == "1"), "persistent": "no"})
	}
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
		if strings.HasPrefix(key, "persist.") {
			d.state.Persistent[key] = value
			continue
		}
		if strings.HasPrefix(key, "prop.") {
			d.state.Runtime[key] = value
		}
	}
	return scanner.Err()
}

func (d *daemon) saveStateLocked() error {
	lines := make([]string, 0, len(d.state.Persistent)+len(d.state.Runtime))
	for _, key := range sortedMapKeys(d.state.Persistent) {
		lines = append(lines, key+"="+d.state.Persistent[key])
	}
	for _, key := range sortedMapKeys(d.state.Runtime) {
		lines = append(lines, key+"="+d.state.Runtime[key])
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(d.statePath, []byte(content), 0644)
}

func (d *daemon) reloadDefinitions() error {
	groups := map[string]groupDefinition{}
	targets := map[string]string{}
	for _, dir := range d.groupDirs {
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
				groups[def.Name] = def
				for _, target := range def.Targets {
					targets[target] = def.Name
				}
			}
		}
	}
	for _, dir := range d.targetDirs {
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
				targets[target] = group
			}
		}
	}
	d.groups = groups
	d.targets = targets
	return nil
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
		current.Units = uniqueSortedStrings(current.Units)
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

func (d *daemon) resolveVirtualLocked(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "group:") {
		group := strings.TrimSpace(strings.TrimPrefix(input, "group:"))
		_, ok := d.groups[group]
		return group, ok
	}
	group, ok := d.targets[input]
	return group, ok
}

func (d *daemon) effectiveValueLocked(key string) (string, bool) {
	if value, ok := d.state.Runtime[key]; ok {
		return value, false
	}
	if value, ok := d.state.Persistent[key]; ok {
		return value, true
	}
	return "", false
}

func (d *daemon) groupStateLocked(name string) visionapi.GroupState {
	def := d.groups[name]
	value := strings.TrimSpace(d.state.Runtime["prop.group."+name])
	persistent := false
	if value == "" {
		value = strings.TrimSpace(d.state.Persistent["persist.group."+name])
		persistent = value != ""
	}
	return visionapi.GroupState{
		Name:       name,
		Units:      append([]string{}, def.Units...),
		Enabled:    value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes"),
		Persistent: persistent,
		Targets:    append([]string{}, def.Targets...),
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func (d *daemon) publishLocked(kind string, payload map[string]string) {
	envelope := visionapi.NewEvent(d.mode, visionapi.SourceSysPropertyd, kind, "", payload)
	data, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	addr := &net.UnixAddr{Name: visionapi.SysvisionIngressSocketPathForMode(d.mode), Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write(data)
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

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(value)
}
