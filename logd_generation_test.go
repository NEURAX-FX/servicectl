package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateGeneratedLoggerDefinitionsRewritesLegacyWithoutRestart(t *testing.T) {
	oldConfig := config
	oldRunDinitctl := runDinitctlFunc
	oldIsKnown := isDinitServiceKnownFunc
	defer func() {
		config = oldConfig
		runDinitctlFunc = oldRunDinitctl
		isDinitServiceKnownFunc = oldIsKnown
	}()

	tempDir := t.TempDir()
	unitDir := filepath.Join(tempDir, "units")
	config = Config{
		Mode:              "system",
		IsRoot:            true,
		SystemdPaths:      []string{unitDir},
		DinitGenDir:       filepath.Join(tempDir, "generated"),
		DinitServiceDir:   filepath.Join(tempDir, "services"),
		ManagedRuntimeDir: filepath.Join(tempDir, "runtime", "managed"),
	}
	for _, dir := range []string{unitDir, config.DinitGenDir, config.DinitServiceDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(unitDir, "demo.service"), []byte("[Service]\nType=simple\nExecStart=/bin/true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	legacy := "# Log consumer for demo\ntype = process\nconsumer-of = demo\ncommand = /usr/local/bin/sys-logd -service demo\nrestart = false\n"
	loggerPath := filepath.Join(config.DinitGenDir, "demo-log")
	if err := os.WriteFile(loggerPath, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	var calls []string
	isDinitServiceKnownFunc = func(name string) bool { return name == "demo-log" }
	runDinitctlFunc = func(args ...string) bool {
		calls = append(calls, strings.Join(args, " "))
		return true
	}

	if err := migrateGeneratedLoggerDefinitions(); err != nil {
		t.Fatal(err)
	}
	migrated, err := os.ReadFile(loggerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-worker", "-scope system", "-unit demo.service", "-logger-service demo-log"} {
		if !strings.Contains(string(migrated), want) {
			t.Fatalf("migrated definition missing %q: %q", want, migrated)
		}
	}
	if len(calls) != 1 || calls[0] != "reload demo-log" {
		t.Fatalf("dinit calls = %#v, want definition reload only", calls)
	}
}

func TestGenerateLoggerDinitUsesBrokerWorkerAndUnitIdentity(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()
	tempDir := t.TempDir()
	config = Config{Mode: "system", IsRoot: true, ManagedRuntimeDir: filepath.Join(tempDir, "managed")}
	unit := &Unit{Name: "demo", User: "nobody"}

	content := generateLoggerDinit("demo-dbusd", "demo", unit)
	for _, want := range []string{
		"consumer-of = demo-dbusd",
		"-worker",
		"-scope system",
		"-unit demo.service",
		"-logger-service demo-dbusd-log",
		"-service demo",
		"-spill-dir " + loggerSpillDir("demo-dbusd-log"),
		"run-as = nobody",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("logger definition missing %q: %q", want, content)
		}
	}
}

func TestGenerateLoggerDinitUserScopeInheritsUserManagerIdentity(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()
	tempDir := t.TempDir()
	config = Config{Mode: "user", ManagedRuntimeDir: filepath.Join(tempDir, "servicectl", "managed")}

	content := generateLoggerDinit("demo", "demo", &Unit{Name: "demo"})
	if !strings.Contains(content, "-scope user") {
		t.Fatalf("logger definition = %q", content)
	}
	if strings.Contains(content, "run-as =") {
		t.Fatalf("user logger overrides user-manager identity: %q", content)
	}
}

func TestEnsureLoggerSpillDirIsPrivate(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()
	config = Config{Mode: "system", IsRoot: true, ManagedRuntimeDir: filepath.Join(t.TempDir(), "managed")}
	path := loggerSpillDir("demo-log")
	if err := ensureLoggerSpillDir(path, &Unit{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Fatalf("spill directory mode = %o", got)
	}
}
