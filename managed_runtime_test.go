package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedRuntimePathsUseDedicatedDir(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()

	tempDir := t.TempDir()
	config = Config{
		Mode:              "system",
		DinitGenDir:       filepath.Join(tempDir, "generated"),
		ManagedRuntimeDir: filepath.Join(tempDir, "runtime"),
	}

	if got := notifydStatePath("redis", managedNotifyd); got != filepath.Join(config.ManagedRuntimeDir, "redis-notifyd", "state") {
		t.Fatalf("notifydStatePath() = %q, want runtime path", got)
	}
	if got := managedNotifySocketPath("redis", managedNotifyd); got != filepath.Join(config.ManagedRuntimeDir, "redis-notifyd", "notify.sock") {
		t.Fatalf("managedNotifySocketPath() = %q, want runtime path", got)
	}
	if strings.HasPrefix(notifydStatePath("redis", managedNotifyd), config.DinitGenDir) {
		t.Fatalf("state path should not live under DinitGenDir: %q", notifydStatePath("redis", managedNotifyd))
	}
	if strings.HasPrefix(managedNotifySocketPath("redis", managedNotifyd), config.DinitGenDir) {
		t.Fatalf("notify path should not live under DinitGenDir: %q", managedNotifySocketPath("redis", managedNotifyd))
	}
}

func TestGenerateNotifydDinitUsesManagedRuntimePaths(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()

	tempDir := t.TempDir()
	config = Config{
		Mode:              "system",
		DinitGenDir:       filepath.Join(tempDir, "generated"),
		ManagedRuntimeDir: filepath.Join(tempDir, "runtime"),
	}

	unit := &Unit{Name: "redis", Type: "notify", ExecStart: []string{"/bin/true"}}
	content := unit.GenerateNotifydDinit(managedNotifyd, nil)

	statePath := filepath.Join(config.ManagedRuntimeDir, "redis-notifyd", "state")
	notifyPath := filepath.Join(config.ManagedRuntimeDir, "redis-notifyd", "notify.sock")
	if !strings.Contains(content, "-state-file "+statePath) {
		t.Fatalf("GenerateNotifydDinit() missing runtime state path: %q", content)
	}
	if !strings.Contains(content, "-notify-path "+notifyPath) {
		t.Fatalf("GenerateNotifydDinit() missing runtime notify path: %q", content)
	}
	if strings.Contains(content, config.DinitGenDir) {
		t.Fatalf("GenerateNotifydDinit() should not reference DinitGenDir for runtime artifacts: %q", content)
	}
}

func TestRecursiveInstallCreatesManagedRuntimeDirForServiceUser(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()

	tempDir := t.TempDir()
	config = Config{
		Mode:              "system",
		IsRoot:            true,
		SystemdPaths:      []string{tempDir},
		DinitServiceDir:   filepath.Join(tempDir, "dinit-service"),
		DinitGenDir:       filepath.Join(tempDir, "dinit-gen"),
		ManagedRuntimeDir: filepath.Join(tempDir, "managed-runtime"),
	}
	current, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	group, err := user.LookupGroupId(current.Gid)
	if err != nil {
		t.Fatalf("lookup current group: %v", err)
	}

	unitText := strings.Join([]string{
		"[Service]",
		"Type=notify",
		"User=" + current.Username,
		"Group=" + group.Name,
		"ExecStart=/bin/true",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tempDir, "notifydemo.service"), []byte(unitText), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}

	recursiveInstall("notifydemo", map[string]bool{}, installOptions{})

	runtimeDir := filepath.Join(config.ManagedRuntimeDir, "notifydemo-notifyd")
	info, err := os.Stat(runtimeDir)
	if err != nil {
		t.Fatalf("managed runtime dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("managed runtime path is not a directory: %s", runtimeDir)
	}
	stateDirMode := info.Mode().Perm()
	if stateDirMode != 0o755 {
		t.Fatalf("managed runtime dir mode = %o, want 755", stateDirMode)
	}
	if generated, err := os.ReadFile(filepath.Join(config.DinitGenDir, "notifydemo-notifyd")); err != nil {
		t.Fatalf("read generated dinit: %v", err)
	} else if strings.Contains(string(generated), config.DinitGenDir) {
		t.Fatalf("generated notifyd dinit should not reference DinitGenDir: %q", string(generated))
	}
}
