package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"servicectl/internal/visionapi"
)

func TestParseSystemdUnitDBusBusName(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()

	tempDir := t.TempDir()
	config = Config{SystemdPaths: []string{tempDir}}
	unitText := "[Unit]\nDescription=Hostname Service\n[Service]\nType=dbus\nBusName=org.freedesktop.hostname1\nExecStart=/usr/lib/systemd/systemd-hostnamed\n"
	if err := os.WriteFile(filepath.Join(tempDir, "systemd-hostnamed.service"), []byte(unitText), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}

	unit, err := parseSystemdUnit("systemd-hostnamed")
	if err != nil {
		t.Fatalf("parseSystemdUnit returned error: %v", err)
	}
	if unit.Type != "dbus" {
		t.Fatalf("Type = %q, want dbus", unit.Type)
	}
	if unit.BusName != "org.freedesktop.hostname1" {
		t.Fatalf("BusName = %q, want org.freedesktop.hostname1", unit.BusName)
	}
}

func TestManagedServiceModeForDBusUnit(t *testing.T) {
	unit := &Unit{Name: "systemd-hostnamed", Type: "dbus", BusName: "org.freedesktop.hostname1"}

	if got := managedServiceModeForUnit(unit, nil); got != managedDbusd {
		t.Fatalf("managedServiceModeForUnit() = %q, want %q", got, managedDbusd)
	}
	if got := managedServiceName(unit.Name, managedDbusd); got != "systemd-hostnamed-dbusd" {
		t.Fatalf("managedServiceName() = %q, want systemd-hostnamed-dbusd", got)
	}
}

func TestManagedServiceModeForNotifyUnitWithBusName(t *testing.T) {
	unit := &Unit{Name: "systemd-localed", Type: "notify", BusName: "org.freedesktop.locale1"}

	if got := managedServiceModeForUnit(unit, nil); got != managedDbusd {
		t.Fatalf("managedServiceModeForUnit() = %q, want %q", got, managedDbusd)
	}
	if got := managedServiceName(unit.Name, managedDbusd); got != "systemd-localed-dbusd" {
		t.Fatalf("managedServiceName() = %q, want systemd-localed-dbusd", got)
	}
}

func TestManagedServiceModeForDBusUnitWithSocket(t *testing.T) {
	unit := &Unit{Name: "systemd-hostnamed", Type: "notify", BusName: "org.freedesktop.hostname1"}
	socket := &SocketUnit{Name: "systemd-hostnamed", ListenStreams: []string{"/run/systemd/io.systemd.Hostname"}}

	if got := managedServiceModeForUnit(unit, socket); got != managedDbusd {
		t.Fatalf("managedServiceModeForUnit() = %q, want %q", got, managedDbusd)
	}
}

func TestGenerateDbusdDinitUsesBusName(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()

	tempDir := t.TempDir()
	config = Config{Mode: "system", ManagedRuntimeDir: filepath.Join(tempDir, "managed")}
	unit := &Unit{Name: "systemd-hostnamed", Type: "dbus", BusName: "org.freedesktop.hostname1", ExecStart: []string{"/usr/lib/systemd/systemd-hostnamed"}}

	content := unit.GenerateNotifydDinit(managedDbusd, nil)

	if !strings.Contains(content, "sys-notifyd") {
		t.Fatalf("generated dbus dinit should use sys-notifyd: %q", content)
	}
	if strings.Contains(content, "sys-dbusd") {
		t.Fatalf("generated dbus dinit should not use standalone sys-dbusd: %q", content)
	}
	if !strings.Contains(content, "-bus-name org.freedesktop.hostname1") {
		t.Fatalf("generated dbus dinit missing bus name: %q", content)
	}
	if !strings.Contains(content, "-control-path "+filepath.Join(config.ManagedRuntimeDir, "systemd-hostnamed-dbusd", "control.sock")) {
		t.Fatalf("generated dbus dinit missing control path: %q", content)
	}
	if strings.Contains(content, "-notify-path") {
		t.Fatalf("generated dbus dinit should not include notify socket args: %q", content)
	}
	if !strings.Contains(content, filepath.Join(config.ManagedRuntimeDir, "systemd-hostnamed-dbusd", "state")) {
		t.Fatalf("generated dbus dinit missing dbusd state path: %q", content)
	}
}

func TestGenerateDbusdDinitForNotifyUnitWithBusNameUsesDbusd(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()

	tempDir := t.TempDir()
	config = Config{Mode: "system", ManagedRuntimeDir: filepath.Join(tempDir, "managed")}
	unit := &Unit{Name: "systemd-localed", Type: "notify", BusName: "org.freedesktop.locale1", ExecStart: []string{"/usr/lib/systemd/systemd-localed"}}

	content := unit.GenerateNotifydDinit(managedDbusd, nil)

	for _, want := range []string{
		"sys-notifyd",
		"-bus-name org.freedesktop.locale1",
		"-service-type notify",
		"-notify-path " + filepath.Join(config.ManagedRuntimeDir, "systemd-localed-dbusd", "notify.sock"),
		"-control-path " + filepath.Join(config.ManagedRuntimeDir, "systemd-localed-dbusd", "control.sock"),
		"-state-file " + filepath.Join(config.ManagedRuntimeDir, "systemd-localed-dbusd", "state"),
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("generated notify+bus dbusd dinit missing %q: %q", want, content)
		}
	}
	if strings.Contains(content, "sys-dbusd") || strings.Contains(content, "-start-now") {
		t.Fatalf("notify+bus dbus dinit should be lazy sys-notifyd mode: %q", content)
	}
	if strings.Contains(content, "command = /usr/lib/systemd/systemd-localed") {
		t.Fatalf("Dinit must supervise sys-notifyd rather than the notify backend directly: %q", content)
	}
}

func TestGenerateDbusdDinitIncludesSocketActivation(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()

	tempDir := t.TempDir()
	config = Config{Mode: "system", ManagedRuntimeDir: filepath.Join(tempDir, "managed")}
	unit := &Unit{Name: "systemd-hostnamed", Type: "notify", BusName: "org.freedesktop.hostname1", ExecStart: []string{"/usr/lib/systemd/systemd-hostnamed"}}
	socket := &SocketUnit{Name: "systemd-hostnamed", ListenStreams: []string{"/run/systemd/io.systemd.Hostname"}, FDNames: []string{"varlink"}, SocketMode: "0666"}

	content := unit.GenerateNotifydDinit(managedDbusd, socket)

	for _, want := range []string{
		"-bus-name org.freedesktop.hostname1",
		"-control-path " + filepath.Join(config.ManagedRuntimeDir, "systemd-hostnamed-dbusd", "control.sock"),
		"-listen unix:/run/systemd/io.systemd.Hostname",
		"-fdname varlink",
		"-socket-mode 0666",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("generated dbusd service missing %q: %q", want, content)
		}
	}
}

func TestNormalizeTimeoutValueConvertsSystemdMinutes(t *testing.T) {
	if got := normalizeTimeoutValue("3min"); got != "3m" {
		t.Fatalf("normalizeTimeoutValue(3min) = %q, want 3m", got)
	}
}

func TestRecursiveInstallWritesDBusActivationFileForNotifyUnit(t *testing.T) {
	oldConfig := config
	oldRunDinitctl := runDinitctlFunc
	oldIsKnown := isDinitServiceKnownFunc
	defer func() {
		config = oldConfig
		runDinitctlFunc = oldRunDinitctl
		isDinitServiceKnownFunc = oldIsKnown
	}()

	tempDir := t.TempDir()
	config = Config{
		Mode:              "system",
		SystemdPaths:      []string{tempDir},
		DinitServiceDir:   filepath.Join(tempDir, "dinit-service"),
		DinitGenDir:       filepath.Join(tempDir, "dinit-gen"),
		ManagedRuntimeDir: filepath.Join(tempDir, "managed"),
		DBusServiceDir:    filepath.Join(tempDir, "dbus-services"),
	}
	unitText := "[Service]\nType=notify\nBusName=org.freedesktop.locale1\nExecStart=/usr/lib/systemd/systemd-localed\n"
	if err := os.WriteFile(filepath.Join(tempDir, "systemd-localed.service"), []byte(unitText), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	obsoleteLink := filepath.Join(config.DinitServiceDir, "systemd-localed-notifyd")
	if err := os.MkdirAll(config.DinitServiceDir, 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(config.DinitGenDir, "systemd-localed-notifyd"), obsoleteLink); err != nil {
		t.Fatalf("create obsolete link: %v", err)
	}
	var calls []string
	runDinitctlFunc = func(args ...string) bool {
		calls = append(calls, strings.Join(args, " "))
		return true
	}
	isDinitServiceKnownFunc = func(name string) bool {
		return name == "systemd-localed-notifyd"
	}

	recursiveInstall("systemd-localed", map[string]bool{}, installOptions{})

	generated, err := os.ReadFile(filepath.Join(config.DinitGenDir, "systemd-localed-dbusd"))
	if err != nil {
		t.Fatalf("read generated dbusd service: %v", err)
	}
	if !strings.Contains(string(generated), "sys-notifyd") || strings.Contains(string(generated), "sys-dbusd") {
		t.Fatalf("generated notify+bus service should launch sys-notifyd dbus mode: %q", string(generated))
	}
	activation, err := os.ReadFile(filepath.Join(config.DBusServiceDir, "org.freedesktop.locale1.service"))
	if err != nil {
		t.Fatalf("read dbus activation file: %v", err)
	}
	activationText := string(activation)
	if !strings.Contains(activationText, "Name=org.freedesktop.locale1") {
		t.Fatalf("activation file missing bus name: %q", activationText)
	}
	if !strings.Contains(activationText, "activate-dbus systemd-localed") {
		t.Fatalf("activation file missing servicectl activate command: %q", activationText)
	}
	if _, err := os.Lstat(obsoleteLink); !os.IsNotExist(err) {
		t.Fatalf("obsolete notifyd link still exists or stat failed: %v", err)
	}
	if !containsExactString(calls, "stop systemd-localed-notifyd") || !containsExactString(calls, "unload --ignore-unstarted systemd-localed-notifyd") {
		t.Fatalf("obsolete service cleanup calls = %v", calls)
	}
}

func TestRecursiveInstallWritesDBusActivationFile(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()

	tempDir := t.TempDir()
	config = Config{
		Mode:              "system",
		SystemdPaths:      []string{tempDir},
		DinitServiceDir:   filepath.Join(tempDir, "dinit-service"),
		DinitGenDir:       filepath.Join(tempDir, "dinit-gen"),
		ManagedRuntimeDir: filepath.Join(tempDir, "managed"),
		DBusServiceDir:    filepath.Join(tempDir, "dbus-services"),
	}
	unitText := "[Service]\nType=dbus\nBusName=org.freedesktop.hostname1\nExecStart=/usr/lib/systemd/systemd-hostnamed\n"
	if err := os.WriteFile(filepath.Join(tempDir, "systemd-hostnamed.service"), []byte(unitText), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}

	recursiveInstall("systemd-hostnamed", map[string]bool{}, installOptions{})

	generated, err := os.ReadFile(filepath.Join(config.DinitGenDir, "systemd-hostnamed-dbusd"))
	if err != nil {
		t.Fatalf("read generated dbusd service: %v", err)
	}
	if !strings.Contains(string(generated), "sys-notifyd") || strings.Contains(string(generated), "sys-dbusd") {
		t.Fatalf("generated service should launch sys-notifyd dbus mode: %q", string(generated))
	}
	activation, err := os.ReadFile(filepath.Join(config.DBusServiceDir, "org.freedesktop.hostname1.service"))
	if err != nil {
		t.Fatalf("read dbus activation file: %v", err)
	}
	activationText := string(activation)
	if !strings.Contains(activationText, "Name=org.freedesktop.hostname1") {
		t.Fatalf("activation file missing bus name: %q", activationText)
	}
	if !strings.Contains(activationText, "activate-dbus systemd-hostnamed") {
		t.Fatalf("activation file missing servicectl activate command: %q", activationText)
	}
	if strings.Contains(activationText, "SystemdService=") {
		t.Fatalf("activation file should not delegate back to systemd: %q", activationText)
	}
}

func TestRecursiveInstallMigratesDBusSocketUnitToDbusd(t *testing.T) {
	oldConfig := config
	oldRunDinitctl := runDinitctlFunc
	oldIsKnown := isDinitServiceKnownFunc
	defer func() {
		config = oldConfig
		runDinitctlFunc = oldRunDinitctl
		isDinitServiceKnownFunc = oldIsKnown
	}()

	tempDir := t.TempDir()
	config = Config{
		Mode:              "system",
		SystemdPaths:      []string{tempDir},
		DinitServiceDir:   filepath.Join(tempDir, "dinit-service"),
		DinitGenDir:       filepath.Join(tempDir, "dinit-gen"),
		ManagedRuntimeDir: filepath.Join(tempDir, "managed"),
		DBusServiceDir:    filepath.Join(tempDir, "dbus-services"),
	}
	serviceText := "[Service]\nType=notify\nBusName=org.freedesktop.hostname1\nExecStart=/usr/lib/systemd/systemd-hostnamed\n"
	socketText := "[Socket]\nListenStream=/run/systemd/io.systemd.Hostname\nFileDescriptorName=varlink\nSocketMode=0666\n"
	if err := os.WriteFile(filepath.Join(tempDir, "systemd-hostnamed.service"), []byte(serviceText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "systemd-hostnamed.socket"), []byte(socketText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.DinitServiceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	obsoleteLink := filepath.Join(config.DinitServiceDir, "systemd-hostnamed-socketd")
	if err := os.Symlink(filepath.Join(config.DinitGenDir, "systemd-hostnamed-socketd"), obsoleteLink); err != nil {
		t.Fatal(err)
	}
	var calls []string
	runDinitctlFunc = func(args ...string) bool {
		calls = append(calls, strings.Join(args, " "))
		return true
	}
	isDinitServiceKnownFunc = func(name string) bool { return name == "systemd-hostnamed-socketd" }

	recursiveInstall("systemd-hostnamed", map[string]bool{}, installOptions{})

	generated, err := os.ReadFile(filepath.Join(config.DinitGenDir, "systemd-hostnamed-dbusd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-bus-name org.freedesktop.hostname1", "-listen unix:/run/systemd/io.systemd.Hostname"} {
		if !strings.Contains(string(generated), want) {
			t.Fatalf("generated dbusd service missing %q: %q", want, generated)
		}
	}
	if !containsExactString(calls, "stop systemd-hostnamed-socketd") || !containsExactString(calls, "unload --ignore-unstarted systemd-hostnamed-socketd") {
		t.Fatalf("obsolete service cleanup calls = %v", calls)
	}
	if _, err := os.Lstat(obsoleteLink); !os.IsNotExist(err) {
		t.Fatalf("obsolete socketd link still exists or stat failed: %v", err)
	}
}

func TestBuildUnitSnapshotForDBusManagedUnit(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()

	tempDir := t.TempDir()
	cfg := Config{
		Mode:              "system",
		SystemdPaths:      []string{tempDir},
		DinitGenDir:       filepath.Join(tempDir, "dinit-gen"),
		ManagedRuntimeDir: filepath.Join(tempDir, "managed"),
	}
	unitText := "[Service]\nType=dbus\nBusName=org.freedesktop.hostname1\nExecStart=/usr/lib/systemd/systemd-hostnamed\n"
	if err := os.WriteFile(filepath.Join(tempDir, "systemd-hostnamed.service"), []byte(unitText), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	config = cfg
	statePath := notifydStatePath("systemd-hostnamed", managedDbusd)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	stateText := "phase=ready\nchild_state=running\nmain_pid=123\nbus_name=org.freedesktop.hostname1\n"
	if err := os.WriteFile(statePath, []byte(stateText), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	snapshot, err := buildUnitSnapshot(cfg, "systemd-hostnamed")
	if err != nil {
		t.Fatalf("buildUnitSnapshot returned error: %v", err)
	}

	if snapshot.ManagedBy != "sys-notifyd" {
		t.Fatalf("ManagedBy = %q, want sys-notifyd", snapshot.ManagedBy)
	}
	if snapshot.BusName != "org.freedesktop.hostname1" {
		t.Fatalf("BusName = %q, want org.freedesktop.hostname1", snapshot.BusName)
	}
	if snapshot.NotifySocket != "" {
		t.Fatalf("NotifySocket = %q, want empty for dbus units", snapshot.NotifySocket)
	}
	if !strings.Contains(snapshot.StateFile, "systemd-hostnamed-dbusd") {
		t.Fatalf("StateFile = %q, want dbusd runtime state path", snapshot.StateFile)
	}
}

func TestActivateDBusUnitStartsManagedDbusdService(t *testing.T) {
	oldConfig := config
	oldRunDinitctl := runDinitctlFunc
	oldControl := controlDBusActivationFunc
	oldTrigger := triggerDBusActivationFunc
	oldWait := waitForDBusOwnerFunc
	defer func() {
		config = oldConfig
		runDinitctlFunc = oldRunDinitctl
		controlDBusActivationFunc = oldControl
		triggerDBusActivationFunc = oldTrigger
		waitForDBusOwnerFunc = oldWait
	}()

	tempDir := t.TempDir()
	config = Config{
		Mode:              "system",
		SystemdPaths:      []string{tempDir},
		DinitGenDir:       filepath.Join(tempDir, "dinit-gen"),
		DinitServiceDir:   filepath.Join(tempDir, "dinit-service"),
		ManagedRuntimeDir: filepath.Join(tempDir, "managed"),
		DBusServiceDir:    filepath.Join(tempDir, "dbus-services"),
	}
	unitText := "[Service]\nType=dbus\nBusName=org.freedesktop.hostname1\nExecStart=/usr/lib/systemd/systemd-hostnamed\n"
	if err := os.WriteFile(filepath.Join(tempDir, "systemd-hostnamed.service"), []byte(unitText), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	var calls []string
	runDinitctlFunc = func(args ...string) bool {
		calls = append(calls, strings.Join(args, " "))
		return true
	}
	var controlPath string
	controlDBusActivationFunc = func(path string, _ time.Duration) error {
		controlPath = path
		return nil
	}
	var waitedBus string
	waitForDBusOwnerFunc = func(busName string, _ time.Duration) error {
		waitedBus = busName
		return nil
	}

	if code := activateDBusUnit("systemd-hostnamed"); code != 0 {
		t.Fatalf("activateDBusUnit exit = %d, want 0", code)
	}
	if !containsExactString(calls, "start systemd-hostnamed-dbusd") {
		t.Fatalf("dinit calls = %v, want start systemd-hostnamed-dbusd", calls)
	}
	if _, err := os.Stat(filepath.Join(config.DinitGenDir, "systemd-hostnamed-dbusd")); err != nil {
		t.Fatalf("activation should install generated dbusd service: %v", err)
	}
	if controlPath != filepath.Join(config.ManagedRuntimeDir, "systemd-hostnamed-dbusd", "control.sock") {
		t.Fatalf("control path = %q, want control socket path", controlPath)
	}
	if waitedBus != "org.freedesktop.hostname1" {
		t.Fatalf("waited bus = %q, want org.freedesktop.hostname1", waitedBus)
	}
}

func TestActivateDBusUnitStartsDbusdForNotifyBusUnit(t *testing.T) {
	oldConfig := config
	oldRunDinitctl := runDinitctlFunc
	oldControl := controlDBusActivationFunc
	oldTrigger := triggerDBusActivationFunc
	oldWait := waitForDBusOwnerFunc
	defer func() {
		config = oldConfig
		runDinitctlFunc = oldRunDinitctl
		controlDBusActivationFunc = oldControl
		triggerDBusActivationFunc = oldTrigger
		waitForDBusOwnerFunc = oldWait
	}()

	tempDir := t.TempDir()
	config = Config{
		Mode:              "system",
		SystemdPaths:      []string{tempDir},
		DinitGenDir:       filepath.Join(tempDir, "dinit-gen"),
		DinitServiceDir:   filepath.Join(tempDir, "dinit-service"),
		ManagedRuntimeDir: filepath.Join(tempDir, "managed"),
		DBusServiceDir:    filepath.Join(tempDir, "dbus-services"),
	}
	unitText := "[Service]\nType=notify\nBusName=org.freedesktop.locale1\nExecStart=/usr/lib/systemd/systemd-localed\n"
	if err := os.WriteFile(filepath.Join(tempDir, "systemd-localed.service"), []byte(unitText), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	var calls []string
	runDinitctlFunc = func(args ...string) bool {
		calls = append(calls, strings.Join(args, " "))
		return true
	}
	var controlPath string
	controlDBusActivationFunc = func(path string, _ time.Duration) error {
		controlPath = path
		return nil
	}
	var waitedBus string
	waitForDBusOwnerFunc = func(busName string, _ time.Duration) error {
		waitedBus = busName
		return nil
	}

	if code := activateDBusUnit("systemd-localed"); code != 0 {
		t.Fatalf("activateDBusUnit exit = %d, want 0", code)
	}
	if !containsExactString(calls, "start systemd-localed-dbusd") {
		t.Fatalf("dinit calls = %v, want start systemd-localed-dbusd", calls)
	}
	if _, err := os.Stat(filepath.Join(config.DinitGenDir, "systemd-localed-dbusd")); err != nil {
		t.Fatalf("activation should install generated dbusd service: %v", err)
	}
	if controlPath != filepath.Join(config.ManagedRuntimeDir, "systemd-localed-dbusd", "control.sock") {
		t.Fatalf("control path = %q, want control socket path", controlPath)
	}
	if waitedBus != "org.freedesktop.locale1" {
		t.Fatalf("waited bus = %q, want org.freedesktop.locale1", waitedBus)
	}
}

func TestStartDBusUnitAutoGeneratesAndStartsManagedDbusdService(t *testing.T) {
	oldConfig := config
	oldRunDinitctl := runDinitctlFunc
	oldIsUnitStarted := isUnitStartedFunc
	defer func() {
		config = oldConfig
		runDinitctlFunc = oldRunDinitctl
		isUnitStartedFunc = oldIsUnitStarted
	}()

	tempDir := t.TempDir()
	config = Config{
		Mode:              "system",
		SystemdPaths:      []string{tempDir},
		DinitGenDir:       filepath.Join(tempDir, "dinit-gen"),
		DinitServiceDir:   filepath.Join(tempDir, "dinit-service"),
		ManagedRuntimeDir: filepath.Join(tempDir, "managed"),
		DBusServiceDir:    filepath.Join(tempDir, "dbus-services"),
	}
	unitText := "[Service]\nType=dbus\nBusName=org.freedesktop.hostname1\nExecStart=/usr/lib/systemd/systemd-hostnamed\n"
	if err := os.WriteFile(filepath.Join(tempDir, "systemd-hostnamed.service"), []byte(unitText), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	var calls []string
	runDinitctlFunc = func(args ...string) bool {
		calls = append(calls, strings.Join(args, " "))
		return true
	}
	isUnitStartedFunc = func(string) bool { return false }

	if !startUnit("systemd-hostnamed", startOptions{}) {
		t.Fatal("startUnit returned false")
	}
	if !containsExactString(calls, "start systemd-hostnamed-dbusd") {
		t.Fatalf("dinit calls = %v, want start systemd-hostnamed-dbusd", calls)
	}
	if _, err := os.Stat(filepath.Join(config.DinitGenDir, "systemd-hostnamed-dbusd")); err != nil {
		t.Fatalf("start should generate dbusd dinit service: %v", err)
	}
	activation, err := os.ReadFile(filepath.Join(config.DBusServiceDir, "org.freedesktop.hostname1.service"))
	if err != nil {
		t.Fatalf("start should generate dbus activation file: %v", err)
	}
	if !strings.Contains(string(activation), "activate-dbus systemd-hostnamed") {
		t.Fatalf("activation file missing activate-dbus command: %q", string(activation))
	}
}

func TestActivateDBusUnitRejectsNonDBusUnit(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()

	tempDir := t.TempDir()
	config = Config{Mode: "system", SystemdPaths: []string{tempDir}}
	unitText := "[Service]\nType=simple\nExecStart=/bin/true\n"
	if err := os.WriteFile(filepath.Join(tempDir, "plain.service"), []byte(unitText), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}

	if code := activateDBusUnit("plain"); code == 0 {
		t.Fatal("activateDBusUnit should reject units without BusName")
	}
}

func TestPrintStatusFromSnapshotShowsDBusMetadata(t *testing.T) {
	output := captureStdout(t, func() {
		printStatusFromSnapshot("systemd-hostnamed", visionapi.UnitSnapshot{
			Name:       "systemd-hostnamed",
			DinitName:  "systemd-hostnamed-dbusd",
			ManagedBy:  "sys-notifyd",
			State:      "STARTED",
			Phase:      "ready",
			ChildState: "running",
			BusName:    "org.freedesktop.hostname1",
			BusOwner:   ":1.42",
		})
	})

	for _, want := range []string{"active (dbus manager running, backend running)", "Bus Name", "org.freedesktop.hostname1"} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q: %q", want, output)
		}
	}
}

func TestShowUnitFromSnapshotShowsDBusMetadata(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()

	tempDir := t.TempDir()
	config = Config{Mode: "system", SystemdPaths: []string{tempDir}}
	unitText := "[Service]\nType=dbus\nBusName=org.freedesktop.hostname1\nExecStart=/usr/lib/systemd/systemd-hostnamed\n"
	if err := os.WriteFile(filepath.Join(tempDir, "systemd-hostnamed.service"), []byte(unitText), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}

	output := captureStdout(t, func() {
		if code := showUnitFromSnapshot("systemd-hostnamed", visionapi.UnitSnapshot{
			Name:       "systemd-hostnamed",
			DinitName:  "systemd-hostnamed-dbusd",
			ManagedBy:  "sys-notifyd",
			BusName:    "org.freedesktop.hostname1",
			BusOwner:   ":1.42",
			StateFile:  filepath.Join(tempDir, "managed", "systemd-hostnamed-dbusd", "state"),
			State:      "STARTED",
			Phase:      "ready",
			ChildState: "running",
		}); code != 0 {
			t.Fatalf("showUnitFromSnapshot exit = %d, want 0", code)
		}
	})

	for _, want := range []string{"Managed By", "sys-notifyd", "Bus Name", "org.freedesktop.hostname1"} {
		if !strings.Contains(output, want) {
			t.Fatalf("show output missing %q: %q", want, output)
		}
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = oldStdout
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(out)
}

func containsExactString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
