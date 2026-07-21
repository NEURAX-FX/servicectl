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

func TestUserConfigUsesHostRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/session-runtime")
	userConfig := buildConfig(true)
	wantRuntime := "/tmp/session-runtime"
	if userConfig.DinitGenDir != filepath.Join(wantRuntime, "dinit.d/generated") {
		t.Fatalf("DinitGenDir = %q", userConfig.DinitGenDir)
	}
	if userConfig.ManagedRuntimeDir != filepath.Join(wantRuntime, "servicectl/managed") {
		t.Fatalf("ManagedRuntimeDir = %q", userConfig.ManagedRuntimeDir)
	}
}

func TestEnableGroupWithS6WritesUserRunScriptInUnifiedPlane(t *testing.T) {
	tmp := t.TempDir()
	prevConfig := config
	defer func() { config = prevConfig }()
	config = buildConfig(false)
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
	if err := ensureS6Bundle(); err != nil {
		t.Fatal(err)
	}
	config = buildConfig(true)

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
	dependencies, err := os.ReadFile(filepath.Join(tmp, "s6", "rc", "group-pipewire-orchestrd", "dependencies"))
	if err != nil {
		t.Fatalf("read dependencies: %v", err)
	}
	if string(dependencies) != "sysvisiond\nsysvisiond-user-0\n" {
		t.Fatalf("user group dependencies = %q", dependencies)
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
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/session-runtime")
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
	if !strings.Contains(string(apiRun), "--ready-fd=3") {
		t.Fatalf("API run script does not configure readiness: %q", apiRun)
	}
	wantRuntime := "/tmp/session-runtime"
	if !strings.Contains(string(apiRun), "XDG_RUNTIME_DIR="+wantRuntime) {
		t.Fatalf("API run script does not preserve the host runtime dir: %q", apiRun)
	}
	if !strings.Contains(string(visionRun), "sysvisiond --mode=user --ready-fd=3") {
		t.Fatalf("sysvisiond run script = %q", visionRun)
	}
	if !strings.Contains(string(visionRun), "XDG_RUNTIME_DIR="+wantRuntime) {
		t.Fatalf("sysvisiond run script does not preserve the host runtime dir: %q", visionRun)
	}
	readyFD, err := os.ReadFile(filepath.Join(s6SysvisiondSourceDir(), "notification-fd"))
	if err != nil {
		t.Fatal(err)
	}
	if string(readyFD) != "3\n" {
		t.Fatalf("notification fd = %q", readyFD)
	}
	apiReadyFD, err := os.ReadFile(filepath.Join(s6ServicectlAPISourceDir(), "notification-fd"))
	if err != nil {
		t.Fatal(err)
	}
	if string(apiReadyFD) != "3\n" {
		t.Fatalf("API notification fd = %q", apiReadyFD)
	}
	apiDependencies, err := os.ReadFile(filepath.Join(s6ServicectlAPISourceDir(), "dependencies"))
	if err != nil {
		t.Fatal(err)
	}
	if string(apiDependencies) != "sys-propertyd\n" {
		t.Fatalf("user API dependencies = %q", apiDependencies)
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
	if _, err := os.Stat(s6SysCgroupdSourceDir()); !os.IsNotExist(err) {
		t.Fatalf("user mode created sys-cgroupd source: %v", err)
	}
	if _, err := os.Stat(s6SysLogdSourceDir()); !os.IsNotExist(err) {
		t.Fatalf("user mode created sys-logd source: %v", err)
	}
}

func TestEnsureS6BundleOrdersSystemPropertyAndAPIReadiness(t *testing.T) {
	tmp := t.TempDir()
	previousConfig := config
	previousPaths := testS6PathsOverride
	t.Cleanup(func() {
		config = previousConfig
		testS6PathsOverride = previousPaths
	})
	config = buildConfig(false)
	useTempDinitPaths(&config, tmp)
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
	propertyRun, err := os.ReadFile(filepath.Join(s6SysPropertydSourceDir(), "run"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(propertyRun), "--ready-fd=3") {
		t.Fatalf("propertyd run script = %q", propertyRun)
	}
	for _, serviceDir := range []string{s6SysPropertydSourceDir(), s6ServicectlAPISourceDir()} {
		readyFD, err := os.ReadFile(filepath.Join(serviceDir, "notification-fd"))
		if err != nil {
			t.Fatal(err)
		}
		if string(readyFD) != "3\n" {
			t.Fatalf("%s notification fd = %q", serviceDir, readyFD)
		}
	}
	apiDependencies, err := os.ReadFile(filepath.Join(s6ServicectlAPISourceDir(), "dependencies"))
	if err != nil {
		t.Fatal(err)
	}
	if string(apiDependencies) != "sys-propertyd\n" {
		t.Fatalf("system API dependencies = %q", apiDependencies)
	}
}

func TestEnsureS6BundleIncludesSystemCgroupd(t *testing.T) {
	tmp := t.TempDir()
	previousConfig := config
	previousPaths := testS6PathsOverride
	t.Cleanup(func() {
		config = previousConfig
		testS6PathsOverride = previousPaths
	})
	config = buildConfig(false)
	useTempDinitPaths(&config, tmp)
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
	run, err := os.ReadFile(filepath.Join(s6SysCgroupdSourceDir(), "run"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(run), "sys-cgroupd") {
		t.Fatalf("sys-cgroupd run script = %q", run)
	}
	dependencies, err := os.ReadFile(filepath.Join(s6SysCgroupdSourceDir(), "dependencies"))
	if err != nil {
		t.Fatal(err)
	}
	if string(dependencies) != "sysvisiond\n" {
		t.Fatalf("sys-cgroupd dependencies = %q", dependencies)
	}
	defaultContents, err := os.ReadFile(s6DefaultContentsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(uniqueLinesPreserveOrder(string(defaultContents)), s6SysCgroupdServiceName()) {
		t.Fatalf("default contents = %q", defaultContents)
	}
}

func TestEnsureS6BundleIncludesSystemLogd(t *testing.T) {
	tmp := t.TempDir()
	previousConfig := config
	previousPaths := testS6PathsOverride
	t.Cleanup(func() {
		config = previousConfig
		testS6PathsOverride = previousPaths
	})
	config = buildConfig(false)
	useTempDinitPaths(&config, tmp)
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
	run, err := os.ReadFile(filepath.Join(s6SysLogdSourceDir(), "run"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sys-logd", "-system", "-socket /run/servicectl/logd", "-ready-fd 3"} {
		if !strings.Contains(string(run), want) {
			t.Fatalf("sys-logd run script missing %q: %q", want, run)
		}
	}
	readyFD, err := os.ReadFile(filepath.Join(s6SysLogdSourceDir(), "notification-fd"))
	if err != nil {
		t.Fatal(err)
	}
	if string(readyFD) != "3\n" {
		t.Fatalf("sys-logd notification fd = %q", readyFD)
	}
	defaultContents, err := os.ReadFile(s6DefaultContentsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(uniqueLinesPreserveOrder(string(defaultContents)), s6SysLogdServiceName()) {
		t.Fatalf("default contents = %q", defaultContents)
	}
}

func TestEnsureS6CoreServicesOnlyCompilesWithoutLiveGraph(t *testing.T) {
	tmp := t.TempDir()
	previousConfig := config
	previousPaths := testS6PathsOverride
	previousAvailable := s6AvailableFunc
	previousCommandOutput := commandOutputFunc
	t.Cleanup(func() {
		config = previousConfig
		testS6PathsOverride = previousPaths
		s6AvailableFunc = previousAvailable
		commandOutputFunc = previousCommandOutput
	})
	config = buildConfig(false)
	useTempDinitPaths(&config, tmp)
	testS6PathsOverride = &s6PlanePaths{
		SourceRoot:      filepath.Join(tmp, "s6", "rc"),
		CompiledDir:     filepath.Join(tmp, "run", "s6", "compiled.servicectl"),
		LiveDir:         filepath.Join(tmp, "run", "s6", "state"),
		BundleDir:       filepath.Join(tmp, "s6", "rc", s6BundleName()),
		BundleContents:  filepath.Join(tmp, "s6", "rc", s6BundleName(), "contents"),
		DefaultContents: filepath.Join(tmp, "s6", "rc", "default", "contents"),
	}
	s6AvailableFunc = func() bool { return true }
	commands := make([]string, 0, 1)
	commandOutputFunc = func(name string, args ...string) (string, int, error) {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		return "", 0, nil
	}

	if err := ensureS6CoreServices(); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || !strings.Contains(commands[0], "s6-rc-compile") {
		t.Fatalf("commands = %#v", commands)
	}
}

func TestEnsureS6CoreServicesBootstrapsMissingSourceRoot(t *testing.T) {
	tmp := t.TempDir()
	previousConfig := config
	previousPaths := testS6PathsOverride
	previousAvailable := s6AvailableFunc
	previousCommandOutput := commandOutputFunc
	t.Cleanup(func() {
		config = previousConfig
		testS6PathsOverride = previousPaths
		s6AvailableFunc = previousAvailable
		commandOutputFunc = previousCommandOutput
	})
	config = buildConfig(false)
	useTempDinitPaths(&config, tmp)
	testS6PathsOverride = &s6PlanePaths{
		SourceRoot:      filepath.Join(tmp, "missing", "s6", "rc"),
		CompiledDir:     filepath.Join(tmp, "run", "s6", "compiled.servicectl"),
		LiveDir:         filepath.Join(tmp, "run", "s6", "state"),
		BundleDir:       filepath.Join(tmp, "missing", "s6", "rc", s6BundleName()),
		BundleContents:  filepath.Join(tmp, "missing", "s6", "rc", s6BundleName(), "contents"),
		DefaultContents: filepath.Join(tmp, "missing", "s6", "rc", "default", "contents"),
	}
	s6AvailableFunc = func() bool { return false }
	commandOutputFunc = func(name string, args ...string) (string, int, error) {
		return "", 0, nil
	}

	if err := ensureS6CoreServices(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s6SysCgroupdSourceDir()); err != nil {
		t.Fatalf("sys-cgroupd source was not bootstrapped: %v", err)
	}
}

func TestEnsureS6CoreServicesUpdatesThenStartsCgroupd(t *testing.T) {
	tmp := t.TempDir()
	previousConfig := config
	previousPaths := testS6PathsOverride
	previousAvailable := s6AvailableFunc
	previousCommandOutput := commandOutputFunc
	t.Cleanup(func() {
		config = previousConfig
		testS6PathsOverride = previousPaths
		s6AvailableFunc = previousAvailable
		commandOutputFunc = previousCommandOutput
	})
	config = buildConfig(false)
	useTempDinitPaths(&config, tmp)
	testS6PathsOverride = &s6PlanePaths{
		SourceRoot:      filepath.Join(tmp, "s6", "rc"),
		CompiledDir:     filepath.Join(tmp, "run", "s6", "compiled.servicectl"),
		LiveDir:         filepath.Join(tmp, "run", "s6", "state"),
		BundleDir:       filepath.Join(tmp, "s6", "rc", s6BundleName()),
		BundleContents:  filepath.Join(tmp, "s6", "rc", s6BundleName(), "contents"),
		DefaultContents: filepath.Join(tmp, "s6", "rc", "default", "contents"),
	}
	if err := os.MkdirAll(testS6PathsOverride.LiveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s6AvailableFunc = func() bool { return true }
	commands := make([]string, 0, 4)
	commandOutputFunc = func(name string, args ...string) (string, int, error) {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		return "", 0, nil
	}

	if err := ensureS6CoreServices(); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 4 {
		t.Fatalf("commands = %#v", commands)
	}
	if !strings.Contains(commands[0], "s6-rc-compile") || !strings.Contains(commands[1], "s6-rc-update") {
		t.Fatalf("commands = %#v", commands)
	}
	if !strings.Contains(commands[2], "s6-rc") || !strings.HasSuffix(commands[2], "change sys-logd") {
		t.Fatalf("logd start command = %q", commands[2])
	}
	if !strings.Contains(commands[3], "s6-rc") || !strings.HasSuffix(commands[3], "change sys-cgroupd") {
		t.Fatalf("cgroupd start command = %q", commands[3])
	}
}

func TestRestartWantedS6Service(t *testing.T) {
	tmp := t.TempDir()
	previousPaths := testS6PathsOverride
	previousCommandOutput := commandOutputFunc
	t.Cleanup(func() {
		testS6PathsOverride = previousPaths
		commandOutputFunc = previousCommandOutput
	})
	testS6PathsOverride = &s6PlanePaths{LiveDir: filepath.Join(tmp, "run", "s6", "state")}
	serviceDir := filepath.Join(tmp, "run", "s6", "service", "demo")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	commands := make([]string, 0, 3)
	commandOutputFunc = func(name string, args ...string) (string, int, error) {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		if strings.Contains(name, "s6-svstat") {
			return "true\n", 0, nil
		}
		return "", 0, nil
	}

	if err := restartWantedS6Service("demo", true); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 3 {
		t.Fatalf("commands = %#v", commands)
	}
	if !strings.Contains(commands[1], "s6-svc -d -wD -T 10000 "+serviceDir) {
		t.Fatalf("stop command = %q", commands[1])
	}
	if !strings.Contains(commands[2], "s6-svc -u -wR -T 10000 "+serviceDir) {
		t.Fatalf("start command = %q", commands[2])
	}
}

func TestRestartWantedS6ServiceLeavesStoppedServiceDown(t *testing.T) {
	tmp := t.TempDir()
	previousPaths := testS6PathsOverride
	previousCommandOutput := commandOutputFunc
	t.Cleanup(func() {
		testS6PathsOverride = previousPaths
		commandOutputFunc = previousCommandOutput
	})
	testS6PathsOverride = &s6PlanePaths{LiveDir: filepath.Join(tmp, "run", "s6", "state")}
	serviceDir := filepath.Join(tmp, "run", "s6", "service", "demo")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	commands := make([]string, 0, 1)
	commandOutputFunc = func(name string, args ...string) (string, int, error) {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		return "false\n", 0, nil
	}

	if err := restartWantedS6Service("demo", true); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 {
		t.Fatalf("commands = %#v", commands)
	}
}

func TestEnsureS6CoreServicesMigratesExistingUserAPIInUnifiedGraph(t *testing.T) {
	tmp := t.TempDir()
	previousConfig := config
	previousPaths := testS6PathsOverride
	previousCommandOutput := commandOutputFunc
	t.Cleanup(func() {
		config = previousConfig
		testS6PathsOverride = previousPaths
		commandOutputFunc = previousCommandOutput
	})
	config = buildConfig(false)
	useTempDinitPaths(&config, tmp)
	testS6PathsOverride = &s6PlanePaths{
		SourceRoot:      filepath.Join(tmp, "s6", "rc"),
		CompiledDir:     filepath.Join(tmp, "run", "s6", "compiled.servicectl"),
		LiveDir:         filepath.Join(tmp, "run", "s6", "state"),
		BundleDir:       filepath.Join(tmp, "s6", "rc", s6BundleName()),
		BundleContents:  filepath.Join(tmp, "s6", "rc", s6BundleName(), "contents"),
		DefaultContents: filepath.Join(tmp, "s6", "rc", "default", "contents"),
	}
	userAPI := filepath.Join(testS6PathsOverride.SourceRoot, "servicectl-api-user-1000")
	if err := os.MkdirAll(userAPI, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userAPI, "type"), []byte("longrun\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldRun := "#!/usr/bin/execlineb -P\n/usr/bin/env XDG_RUNTIME_DIR=/run/user/1000 /home/demo/.local/bin/servicectl --user serve-api\n"
	if err := os.WriteFile(filepath.Join(userAPI, "run"), []byte(oldRun), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userAPI, "dependencies"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	commandOutputFunc = func(name string, args ...string) (string, int, error) { return "", 0, nil }

	if err := ensureS6CoreServices(); err != nil {
		t.Fatal(err)
	}
	run, err := os.ReadFile(filepath.Join(userAPI, "run"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(run), "serve-api --ready-fd=3") {
		t.Fatalf("migrated run script = %q", run)
	}
	readyFD, err := os.ReadFile(filepath.Join(userAPI, "notification-fd"))
	if err != nil {
		t.Fatal(err)
	}
	if string(readyFD) != "3\n" {
		t.Fatalf("notification fd = %q", readyFD)
	}
	dependencies, err := os.ReadFile(filepath.Join(userAPI, "dependencies"))
	if err != nil {
		t.Fatal(err)
	}
	if string(dependencies) != "sys-propertyd\n" {
		t.Fatalf("dependencies = %q", dependencies)
	}
}

func TestEnsureS6CoreServicesMigratesExistingUserOrchestrdDependencies(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/session-runtime")
	previousConfig := config
	previousPaths := testS6PathsOverride
	previousCommandOutput := commandOutputFunc
	t.Cleanup(func() {
		config = previousConfig
		testS6PathsOverride = previousPaths
		commandOutputFunc = previousCommandOutput
	})
	config = buildConfig(false)
	useTempDinitPaths(&config, tmp)
	testS6PathsOverride = &s6PlanePaths{
		SourceRoot:      filepath.Join(tmp, "s6", "rc"),
		CompiledDir:     filepath.Join(tmp, "run", "s6", "compiled.servicectl"),
		LiveDir:         filepath.Join(tmp, "run", "s6", "state"),
		BundleDir:       filepath.Join(tmp, "s6", "rc", s6BundleName()),
		BundleContents:  filepath.Join(tmp, "s6", "rc", s6BundleName(), "contents"),
		DefaultContents: filepath.Join(tmp, "s6", "rc", "default", "contents"),
	}
	userOrchestrd := filepath.Join(testS6PathsOverride.SourceRoot, "demo-orchestrd")
	if err := os.MkdirAll(userOrchestrd, 0o755); err != nil {
		t.Fatal(err)
	}
	run := "#!/usr/bin/execlineb -P\n/usr/bin/env XDG_RUNTIME_DIR=/run/user/1000 /usr/bin/sys-orchestrd --user --unit demo.service\n"
	if err := os.WriteFile(filepath.Join(userOrchestrd, "run"), []byte(run), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userOrchestrd, "dependencies"), []byte("dependency-orchestrd\nsysvisiond-user-1000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(testS6PathsOverride.LiveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serviceDir := filepath.Join(filepath.Dir(testS6PathsOverride.LiveDir), "service", "demo-orchestrd")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	commands := make([]string, 0, 6)
	commandOutputFunc = func(name string, args ...string) (string, int, error) {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		if strings.Contains(name, "s6-svstat") {
			return "true\n", 0, nil
		}
		return "", 0, nil
	}

	if err := ensureS6CoreServices(); err != nil {
		t.Fatal(err)
	}
	dependencies, err := os.ReadFile(filepath.Join(userOrchestrd, "dependencies"))
	if err != nil {
		t.Fatal(err)
	}
	want := "dependency-orchestrd\nsysvisiond\nsysvisiond-user-1000\n"
	if string(dependencies) != want {
		t.Fatalf("dependencies = %q, want %q", dependencies, want)
	}
	migratedRun, err := os.ReadFile(filepath.Join(userOrchestrd, "run"))
	if err != nil {
		t.Fatal(err)
	}
	wantRuntime := "/tmp/session-runtime"
	if !strings.Contains(string(migratedRun), "XDG_RUNTIME_DIR="+wantRuntime+" ") {
		t.Fatalf("migrated run script = %q", migratedRun)
	}
	if strings.Contains(string(migratedRun), "XDG_RUNTIME_DIR=/run/user/1000 ") {
		t.Fatalf("migrated run script retains stale runtime: %q", migratedRun)
	}
	wantCommands := []string{
		"/bin/s6-svstat -o wantedup " + serviceDir,
		"/bin/s6-svc -d -wD -T 10000 " + serviceDir,
		"/bin/s6-svc -u -wU -T 10000 " + serviceDir,
	}
	for _, want := range wantCommands {
		if !containsString(commands, want) {
			t.Fatalf("commands = %#v, missing %q", commands, want)
		}
	}
}

func TestUserOrchestrdRunPreservesHostRuntimeDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/session-runtime")
	previousConfig := config
	previousPaths := testS6PathsOverride
	previousAvailable := s6AvailableFunc
	t.Cleanup(func() {
		config = previousConfig
		testS6PathsOverride = previousPaths
		s6AvailableFunc = previousAvailable
	})
	config = buildConfig(false)
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
	config = buildConfig(true)
	s6AvailableFunc = func() bool { return true }
	unitDir := filepath.Join(tmp, "units")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config.SystemdPaths = []string{unitDir}
	if err := os.WriteFile(filepath.Join(unitDir, "demo.service"), []byte("[Service]\nType=simple\nExecStart=/bin/sleep 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enableWithS6("demo.service"); err != nil {
		t.Fatal(err)
	}
	run, err := os.ReadFile(filepath.Join(s6OrchestrdSourceDir("demo.service"), "run"))
	if err != nil {
		t.Fatal(err)
	}
	want := "/usr/bin/env XDG_RUNTIME_DIR=/tmp/session-runtime " + sysOrchestrdBinaryPath() + " --user --unit demo.service"
	if !strings.Contains(string(run), want) {
		t.Fatalf("run script = %q, want %q", run, want)
	}
	if _, err := os.Stat(filepath.Join(s6OrchestrdSourceDir("demo.service"), "notification-fd")); !os.IsNotExist(err) {
		t.Fatalf("ordinary orchestrator must not use notification-fd: %v", err)
	}
	dependencies, err := os.ReadFile(filepath.Join(s6OrchestrdSourceDir("demo.service"), "dependencies"))
	if err != nil {
		t.Fatal(err)
	}
	if string(dependencies) != "sysvisiond\nsysvisiond-user-0\n" {
		t.Fatalf("user unit dependencies = %q", dependencies)
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

func useTempDinitPaths(cfg *Config, root string) {
	cfg.DinitGenDir = filepath.Join(root, "dinit", "generated")
	cfg.DinitServiceDir = filepath.Join(root, "dinit", "services")
	cfg.ManagedRuntimeDir = filepath.Join(root, "runtime", "managed")
}
