package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"servicectl/internal/visionapi"
)

func TestS6PathsAreUnifiedAcrossModes(t *testing.T) {
	systemPaths := s6PathsForMode("system")
	userPaths := s6PathsForMode("user")

	if systemPaths.SourceRoot != "/s6/rc" {
		t.Fatalf("system source root = %q, want /s6/rc", systemPaths.SourceRoot)
	}
	if userPaths.SourceRoot != "/s6/rc" {
		t.Fatalf("user source root = %q, want /s6/rc", userPaths.SourceRoot)
	}
	if systemPaths.BundleContents != "/s6/rc/servicectl-enabled/contents" {
		t.Fatalf("unexpected system bundle path: %q", systemPaths.BundleContents)
	}
	if userPaths.BundleContents != "/s6/rc/servicectl-enabled/contents" {
		t.Fatalf("unexpected user bundle path: %q", userPaths.BundleContents)
	}
	if systemPaths.LiveDir != "/run/s6/state" {
		t.Fatalf("unexpected system live dir: %q", systemPaths.LiveDir)
	}
	if userPaths.LiveDir != "/run/s6/state" {
		t.Fatalf("unexpected user live dir: %q", userPaths.LiveDir)
	}
	if systemPaths.CompiledDir != "/run/s6/compiled.servicectl" {
		t.Fatalf("unexpected system compiled dir: %q", systemPaths.CompiledDir)
	}
	if userPaths.CompiledDir != "/run/s6/compiled.servicectl" {
		t.Fatalf("unexpected user compiled dir: %q", userPaths.CompiledDir)
	}
	if strings.Contains(userPaths.SourceRoot, "servicectl/s6") {
		t.Fatalf("user source root should not be runtime-based: %q", userPaths.SourceRoot)
	}
}

func TestDBusActivationDirectoriesAreModeSpecific(t *testing.T) {
	systemConfig := buildConfig(false)
	if systemConfig.DBusServiceDir != "/etc/dbus-1/system-services" {
		t.Fatalf("system DBusServiceDir = %q, want /etc/dbus-1/system-services", systemConfig.DBusServiceDir)
	}

	userConfig := buildConfig(true)
	if !strings.HasSuffix(userConfig.DBusServiceDir, "/.local/share/dbus-1/services") {
		t.Fatalf("user DBusServiceDir = %q, want ~/.local/share/dbus-1/services", userConfig.DBusServiceDir)
	}
}

func TestEnableGroupWithS6WritesUserRunScriptInUnifiedPlane(t *testing.T) {
	tmp := t.TempDir()
	prevConfig := config
	defer func() { config = prevConfig }()
	config = buildConfig(true)
	prevPaths := testS6PathsOverride
	testS6PathsOverride = &s6PlanePaths{
		SourceRoot:      filepath.Join(tmp, "s6", "rc"),
		CompiledDir:     filepath.Join(tmp, "run", "s6", "compiled.servicectl"),
		LiveDir:         filepath.Join(tmp, "run", "s6", "state"),
		BundleDir:       filepath.Join(tmp, "s6", "rc", s6BundleName()),
		BundleContents:  filepath.Join(tmp, "s6", "rc", s6BundleName(), "contents"),
		DefaultContents: filepath.Join(tmp, "s6", "rc", "default", "contents"),
	}
	defer func() { testS6PathsOverride = prevPaths }()
	prevAvailable := s6AvailableFunc
	prevCommandOutput := commandOutputFunc
	s6AvailableFunc = func() bool { return true }
	commandOutputFunc = func(name string, args ...string) (string, int, error) { return "", 0, nil }
	defer func() {
		s6AvailableFunc = prevAvailable
		commandOutputFunc = prevCommandOutput
	}()

	if err := enableGroupWithS6("pipewire"); err != nil {
		t.Fatalf("enableGroupWithS6 returned error: %v", err)
	}

	runScript, err := os.ReadFile(filepath.Join(tmp, "s6", "rc", "group-pipewire-orchestrd", "run"))
	if err != nil {
		t.Fatalf("read run script: %v", err)
	}
	if !strings.Contains(string(runScript), "--user --group pipewire") {
		t.Fatalf("expected unified plane user run script, got %q", string(runScript))
	}
	contents, err := os.ReadFile(filepath.Join(tmp, "s6", "rc", "servicectl-enabled", "contents"))
	if err != nil {
		t.Fatalf("read bundle contents: %v", err)
	}
	if !strings.Contains(string(contents), "group-pipewire-orchestrd") {
		t.Fatalf("expected group service in unified bundle, got %q", string(contents))
	}
}

func TestIsGroupEnabledWithS6UsesUnifiedBundleInUserMode(t *testing.T) {
	tmp := t.TempDir()
	prevConfig := config
	prevPaths := testS6PathsOverride
	defer func() {
		config = prevConfig
		testS6PathsOverride = prevPaths
	}()
	config = buildConfig(true)
	testS6PathsOverride = &s6PlanePaths{
		SourceRoot:      filepath.Join(tmp, "s6", "rc"),
		CompiledDir:     filepath.Join(tmp, "run", "s6", "compiled.servicectl"),
		LiveDir:         filepath.Join(tmp, "run", "s6", "state"),
		BundleDir:       filepath.Join(tmp, "s6", "rc", s6BundleName()),
		BundleContents:  filepath.Join(tmp, "s6", "rc", s6BundleName(), "contents"),
		DefaultContents: filepath.Join(tmp, "s6", "rc", "default", "contents"),
	}
	if err := os.MkdirAll(filepath.Dir(testS6PathsOverride.BundleContents), 0755); err != nil {
		t.Fatalf("mkdir bundle dir: %v", err)
	}
	if err := os.WriteFile(testS6PathsOverride.BundleContents, []byte("group-pipewire-orchestrd\n"), 0644); err != nil {
		t.Fatalf("write bundle contents: %v", err)
	}

	if !isGroupEnabledWithS6("pipewire") {
		t.Fatal("expected unified bundle lookup to report user group enabled")
	}
}

func TestIsEnabledWithS6UsesUnifiedBundleInUserMode(t *testing.T) {
	tmp := t.TempDir()
	prevConfig := config
	prevPaths := testS6PathsOverride
	defer func() {
		config = prevConfig
		testS6PathsOverride = prevPaths
	}()
	config = buildConfig(true)
	testS6PathsOverride = &s6PlanePaths{
		SourceRoot:      filepath.Join(tmp, "s6", "rc"),
		CompiledDir:     filepath.Join(tmp, "run", "s6", "compiled.servicectl"),
		LiveDir:         filepath.Join(tmp, "run", "s6", "state"),
		BundleDir:       filepath.Join(tmp, "s6", "rc", s6BundleName()),
		BundleContents:  filepath.Join(tmp, "s6", "rc", s6BundleName(), "contents"),
		DefaultContents: filepath.Join(tmp, "s6", "rc", "default", "contents"),
	}
	if err := os.MkdirAll(filepath.Dir(testS6PathsOverride.BundleContents), 0755); err != nil {
		t.Fatalf("mkdir bundle dir: %v", err)
	}
	if err := os.WriteFile(testS6PathsOverride.BundleContents, []byte("cliproxyapi-orchestrd\n"), 0644); err != nil {
		t.Fatalf("write bundle contents: %v", err)
	}

	if !isEnabledWithS6("cliproxyapi.service") {
		t.Fatal("expected unified bundle lookup to report user unit enabled")
	}
}

func TestLiveStartS6UsesUnifiedLiveDirInUserMode(t *testing.T) {
	prevConfig := config
	prevCommandOutput := commandOutputFunc
	defer func() {
		config = prevConfig
		commandOutputFunc = prevCommandOutput
	}()
	config = buildConfig(true)
	var gotArgs []string
	commandOutputFunc = func(name string, args ...string) (string, int, error) {
		gotArgs = append([]string{name}, args...)
		return "", 0, nil
	}

	if err := os.MkdirAll("/run/s6/state", 0755); err != nil {
		t.Fatalf("mkdir live dir: %v", err)
	}
	if err := liveStartS6("group-pipewire-orchestrd"); err != nil {
		t.Fatalf("liveStartS6 returned error: %v", err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "/run/s6/state") {
		t.Fatalf("expected unified live dir in command args, got %q", joined)
	}
}

func TestLiveUpdateS6UsesUnifiedCompiledDirInUserMode(t *testing.T) {
	prevConfig := config
	prevCommandOutput := commandOutputFunc
	defer func() {
		config = prevConfig
		commandOutputFunc = prevCommandOutput
	}()
	config = buildConfig(true)
	var gotArgs []string
	commandOutputFunc = func(name string, args ...string) (string, int, error) {
		gotArgs = append([]string{name}, args...)
		return "", 0, nil
	}

	if err := os.MkdirAll("/run/s6/state", 0755); err != nil {
		t.Fatalf("mkdir live dir: %v", err)
	}
	if err := liveUpdateS6(); err != nil {
		t.Fatalf("liveUpdateS6 returned error: %v", err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "/run/s6/compiled.servicectl") {
		t.Fatalf("expected unified compiled dir in command args, got %q", joined)
	}
}

func TestBuildEnabledServiceDAGSkipsMissingTransitiveDependency(t *testing.T) {
	roots := enabledRootSet{Standalone: []string{"app.service"}, Groups: map[string][]string{}}

	lookup := func(name string) (*Unit, error) {
		switch strings.TrimSpace(name) {
		case "app.service":
			return &Unit{Name: "app", Wants: []string{"optional-missing.service"}}, nil
		case "optional-missing.service":
			return nil, fmt.Errorf("unit %s not found", name)
		default:
			return nil, nil
		}
	}

	graph, err := buildEnabledServiceDAG(roots, lookup)
	if err != nil {
		t.Fatalf("buildEnabledServiceDAG returned unexpected error: %v", err)
	}

	order, err := graph.TopologicalServices()
	if err != nil {
		t.Fatalf("TopologicalServices returned unexpected error: %v", err)
	}

	if indexOf(order, "app.service") < 0 {
		t.Fatalf("order missing root unit: %v", order)
	}
}

func TestBuildEnabledServiceDAGSkipsMissingExplicitRoot(t *testing.T) {
	roots := enabledRootSet{Standalone: []string{"missing-root.service"}, Groups: map[string][]string{}}

	lookup := func(name string) (*Unit, error) {
		if strings.TrimSpace(name) == "missing-root.service" {
			return nil, fmt.Errorf("unit %s not found", name)
		}
		return nil, nil
	}

	graph, err := buildEnabledServiceDAG(roots, lookup)
	if err != nil {
		t.Fatalf("buildEnabledServiceDAG returned unexpected error: %v", err)
	}
	order, err := graph.TopologicalServices()
	if err != nil {
		t.Fatalf("TopologicalServices returned unexpected error: %v", err)
	}
	if len(order) != 1 || order[0] != "missing-root.service" {
		t.Fatalf("unexpected topological order for missing root: %v", order)
	}
}

func TestUserActivationGateReady(t *testing.T) {
	metaReady := sysvisionMetaResponse{MetaResponse: visionapi.MetaResponse{ServicectlEventsConnected: true}}
	if !userActivationGateReady(true, metaReady, false, false) {
		t.Fatal("expected gate to be ready when sysvision meta says system bus connected")
	}

	metaNotReady := sysvisionMetaResponse{MetaResponse: visionapi.MetaResponse{ServicectlEventsConnected: false}}
	if userActivationGateReady(true, metaNotReady, true, true) {
		t.Fatal("expected gate to be blocked when sysvision meta says system bus disconnected")
	}

	if !userActivationGateReady(false, sysvisionMetaResponse{}, true, true) {
		t.Fatal("expected fallback socket check to allow activation when sockets exist")
	}

	if userActivationGateReady(false, sysvisionMetaResponse{}, true, false) {
		t.Fatal("expected fallback socket check to block activation when socket missing")
	}
}

func TestEnsureS6BundleUsesOnePlaneInUserMode(t *testing.T) {
	tmp := t.TempDir()
	previousConfig := config
	previousPaths := testS6PathsOverride
	t.Cleanup(func() {
		config = previousConfig
		testS6PathsOverride = previousPaths
	})
	config = buildConfig(true)
	testS6PathsOverride = &s6PlanePaths{
		SourceRoot:      filepath.Join(tmp, "s6", "rc"),
		CompiledDir:     filepath.Join(tmp, "run", "s6", "compiled.servicectl"),
		LiveDir:         filepath.Join(tmp, "run", "s6", "state"),
		BundleDir:       filepath.Join(tmp, "s6", "rc", s6BundleName()),
		BundleContents:  filepath.Join(tmp, "s6", "rc", s6BundleName(), "contents"),
		DefaultContents: filepath.Join(tmp, "s6", "rc", "default", "contents"),
	}
	if err := ensureS6Bundle(); err != nil {
		t.Fatal(err)
	}
	apiRun, err := os.ReadFile(filepath.Join(s6ServicectlAPISourceDir(), "run"))
	if err != nil {
		t.Fatal(err)
	}
	visionRun, err := os.ReadFile(filepath.Join(s6SysvisiondSourceDir(), "run"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(apiRun), "servicectl --user serve-api") {
		t.Fatalf("API run script = %q", apiRun)
	}
	if !strings.Contains(string(visionRun), "sysvisiond --mode=user") {
		t.Fatalf("sysvisiond run script = %q", visionRun)
	}
	if s6SysvisiondServiceName() != "sysvisiond-user-0" {
		t.Fatalf("user sysvisiond service name = %q", s6SysvisiondServiceName())
	}
	if s6ServicectlAPIServiceName() != "servicectl-api-user-0" {
		t.Fatalf("user API service name = %q", s6ServicectlAPIServiceName())
	}
	defaultContents, err := os.ReadFile(s6DefaultContentsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(defaultContents), "sysvisiond-user-0") || !strings.Contains(string(defaultContents), "servicectl-api-user-0") {
		t.Fatalf("default contents = %q", defaultContents)
	}
	if _, err := os.Stat(s6SysPropertydSourceDir()); !os.IsNotExist(err) {
		t.Fatalf("user mode created sys-propertyd source: %v", err)
	}
}

func TestS6OwnerModeDetection(t *testing.T) {
	tmp := t.TempDir()
	systemDir := filepath.Join(tmp, "system")
	userDir := filepath.Join(tmp, "user")
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemDir, "run"), []byte("sys-orchestrd --unit demo.service\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "run"), []byte("sys-orchestrd --user --unit demo.service\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := config
	t.Cleanup(func() { config = previous })
	config = buildConfig(false)
	if !s6OwnerMatchesCurrentMode(systemDir) || s6OwnerMatchesCurrentMode(userDir) {
		t.Fatal("system mode owner detection failed")
	}
	config = buildConfig(true)
	if s6OwnerMatchesCurrentMode(systemDir) || !s6OwnerMatchesCurrentMode(userDir) {
		t.Fatal("user mode owner detection failed")
	}
}

func TestEnabledStandaloneServicesFiltersCurrentMode(t *testing.T) {
	tmp := t.TempDir()
	previousConfig := config
	previousPaths := testS6PathsOverride
	t.Cleanup(func() {
		config = previousConfig
		testS6PathsOverride = previousPaths
	})
	testS6PathsOverride = &s6PlanePaths{
		SourceRoot:      filepath.Join(tmp, "s6", "rc"),
		CompiledDir:     filepath.Join(tmp, "run", "s6", "compiled.servicectl"),
		LiveDir:         filepath.Join(tmp, "run", "s6", "state"),
		BundleDir:       filepath.Join(tmp, "s6", "rc", s6BundleName()),
		BundleContents:  filepath.Join(tmp, "s6", "rc", s6BundleName(), "contents"),
		DefaultContents: filepath.Join(tmp, "s6", "rc", "default", "contents"),
	}
	if err := os.MkdirAll(testS6PathsOverride.BundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testS6PathsOverride.BundleContents, []byte("system-demo-orchestrd\nuser-demo-orchestrd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]string{
		"system-demo-orchestrd": "sys-orchestrd --unit system-demo.service\n",
		"user-demo-orchestrd":   "sys-orchestrd --user --unit user-demo.service\n",
	} {
		directory := filepath.Join(testS6PathsOverride.SourceRoot, name)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "run"), []byte(run), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config = buildConfig(false)
	if got := enabledStandaloneServicesFromS6Bundle(); len(got) != 1 || got[0] != "system-demo.service" {
		t.Fatalf("system services = %#v", got)
	}
	config = buildConfig(true)
	if got := enabledStandaloneServicesFromS6Bundle(); len(got) != 1 || got[0] != "user-demo.service" {
		t.Fatalf("user services = %#v", got)
	}
}
