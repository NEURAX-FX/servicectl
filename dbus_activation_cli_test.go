package main

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDaemonActivationEnableAndDisableRestoresExistingConfig(t *testing.T) {
	paths := testDBusActivationPaths(t)
	oldContent := []byte("<busconfig>\n  <!-- administrator config -->\n</busconfig>\n")
	if err := os.MkdirAll(filepath.Dir(paths.DaemonConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.DaemonConfig, oldContent, 0o640); err != nil {
		t.Fatal(err)
	}
	checker := func(context.Context, dbusActivationPaths) error { return nil }

	if err := enableDaemonActivation(context.Background(), paths, checker); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(paths.DaemonConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "<servicehelper>"+paths.HelperPath+"</servicehelper>") {
		t.Fatalf("daemon config = %q", content)
	}
	manifest, err := readDBusActivationManifest(paths.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Backend != "daemon" || !manifest.PreviousExists || string(manifest.PreviousContent) != string(oldContent) || manifest.CurrentHash == "" {
		t.Fatalf("manifest = %#v", manifest)
	}

	if err := disableDBusActivation(paths); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(paths.DaemonConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(oldContent) {
		t.Fatalf("restored content = %q", restored)
	}
	if _, err := os.Stat(paths.ManifestPath); !os.IsNotExist(err) {
		t.Fatalf("manifest still exists: %v", err)
	}
}

func TestDaemonActivationEnableRemovesNewConfigOnDisable(t *testing.T) {
	paths := testDBusActivationPaths(t)
	if err := enableDaemonActivation(context.Background(), paths, func(context.Context, dbusActivationPaths) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := disableDBusActivation(paths); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.DaemonConfig); !os.IsNotExist(err) {
		t.Fatalf("new config was not removed: %v", err)
	}
}

func TestDaemonActivationCheckDoesNotWrite(t *testing.T) {
	paths := testDBusActivationPaths(t)
	called := false
	if err := checkDaemonActivation(context.Background(), paths, func(context.Context, dbusActivationPaths) error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("preflight was not called")
	}
	for _, path := range []string{paths.DaemonConfig, paths.ManifestPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("check wrote %s: %v", path, err)
		}
	}
}

func TestDBusActivationCommandRejectsMissingSubcommand(t *testing.T) {
	if status := dbusActivationCommand([]string{"--backend=daemon"}); status != 1 {
		t.Fatalf("status = %d, want 1", status)
	}
}

func TestDefaultDBusActivationPathsUseConfiguredHelper(t *testing.T) {
	oldPath := dbusActivationHelperPath
	t.Cleanup(func() { dbusActivationHelperPath = oldPath })
	dbusActivationHelperPath = "/usr/local/libexec/servicectl/sys-dbusd-daemon-helper"
	if got := defaultDBusActivationPaths().HelperPath; got != dbusActivationHelperPath {
		t.Fatalf("helper path = %q, want %q", got, dbusActivationHelperPath)
	}
}

func TestDisableRefusesChangedManagedConfig(t *testing.T) {
	paths := testDBusActivationPaths(t)
	if err := enableDaemonActivation(context.Background(), paths, func(context.Context, dbusActivationPaths) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.DaemonConfig, []byte("changed by administrator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := disableDBusActivation(paths); !errors.Is(err, errDBusActivationChanged) {
		t.Fatalf("disable error = %v, want changed-file refusal", err)
	}
	if _, err := os.Stat(paths.ManifestPath); err != nil {
		t.Fatalf("manifest was removed after refusal: %v", err)
	}
}

func TestDisableRemainsRetryableWhenManifestRemovalFails(t *testing.T) {
	paths := testDBusActivationPaths(t)
	if err := enableDaemonActivation(context.Background(), paths, func(context.Context, dbusActivationPaths) error { return nil }); err != nil {
		t.Fatal(err)
	}
	oldRemove := removeDBusActivationFile
	t.Cleanup(func() { removeDBusActivationFile = oldRemove })
	removeDBusActivationFile = func(path string) error {
		if path == paths.ManifestPath {
			return errors.New("injected manifest removal failure")
		}
		return os.Remove(path)
	}
	if err := disableDBusActivation(paths); err == nil {
		t.Fatal("disable unexpectedly succeeded")
	}
	content, err := os.ReadFile(paths.DaemonConfig)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readDBusActivationManifest(paths.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if hashBytes(content) != manifest.CurrentHash {
		t.Fatal("failed disable did not restore retryable managed state")
	}
	removeDBusActivationFile = os.Remove
	if err := disableDBusActivation(paths); err != nil {
		t.Fatalf("retry disable: %v", err)
	}
}

func TestEnableRejectsSymlinkConfig(t *testing.T) {
	paths := testDBusActivationPaths(t)
	if err := os.MkdirAll(filepath.Dir(paths.DaemonConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, paths.DaemonConfig); err != nil {
		t.Fatal(err)
	}
	if err := enableDaemonActivation(context.Background(), paths, func(context.Context, dbusActivationPaths) error { return nil }); err == nil {
		t.Fatal("enable accepted symlink target")
	}
}

func TestStatusRefusesChangedManagedConfig(t *testing.T) {
	paths := testDBusActivationPaths(t)
	if err := enableDaemonActivation(context.Background(), paths, func(context.Context, dbusActivationPaths) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.DaemonConfig, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPing := pingDBusActivationCore
	t.Cleanup(func() { pingDBusActivationCore = oldPing })
	pingDBusActivationCore = func(context.Context, string) error { return nil }
	if err := printDBusActivationStatus(context.Background(), paths); !errors.Is(err, errDBusActivationChanged) {
		t.Fatalf("status error = %v, want changed-file refusal", err)
	}
}

func TestStatusRefusesUnmanagedConfigWithoutManifest(t *testing.T) {
	paths := testDBusActivationPaths(t)
	if err := os.MkdirAll(filepath.Dir(paths.DaemonConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.DaemonConfig, daemonActivationConfig(paths.HelperPath), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := printDBusActivationStatus(context.Background(), paths); err == nil {
		t.Fatal("status reported disabled with an unmanaged override present")
	}
}

func TestDefaultDaemonPreflightRejectsSymlinkHelper(t *testing.T) {
	paths := testDBusActivationPaths(t)
	if err := os.MkdirAll(filepath.Dir(paths.HelperPath), 0o755); err != nil {
		t.Fatal(err)
	}
	target := paths.HelperPath + ".target"
	if err := os.WriteFile(target, []byte("helper"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, os.ModeSetuid|0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, paths.HelperPath); err != nil {
		t.Fatal(err)
	}
	oldEUID := effectiveUID
	t.Cleanup(func() { effectiveUID = oldEUID })
	effectiveUID = func() int { return 0 }
	if err := defaultDaemonPreflight(context.Background(), paths); err == nil {
		t.Fatal("preflight accepted symlink helper")
	}
}

func TestDefaultDaemonPreflightChecksCore(t *testing.T) {
	paths := testDBusActivationPaths(t)
	if err := os.MkdirAll(filepath.Dir(paths.HelperPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.HelperPath, []byte("helper"), 0o4750); err != nil {
		t.Fatal(err)
	}
	dbusGroup, err := user.LookupGroup("dbus")
	if err != nil {
		t.Fatal(err)
	}
	dbusGID, err := strconv.Atoi(dbusGroup.Gid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(paths.HelperPath, 0, dbusGID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.HelperPath, os.ModeSetuid|0o750); err != nil {
		t.Fatal(err)
	}
	oldEUID := effectiveUID
	oldPing := pingDBusActivationCore
	t.Cleanup(func() {
		effectiveUID = oldEUID
		pingDBusActivationCore = oldPing
	})
	effectiveUID = func() int { return 0 }
	pinged := false
	pingDBusActivationCore = func(context.Context, string) error {
		pinged = true
		return nil
	}
	if err := defaultDaemonPreflight(context.Background(), paths); err != nil {
		t.Fatal(err)
	}
	if !pinged {
		t.Fatal("core was not pinged")
	}
}

func TestReadManifestRejectsSymlinkAndLooseMode(t *testing.T) {
	paths := testDBusActivationPaths(t)
	if err := enableDaemonActivation(context.Background(), paths, func(context.Context, dbusActivationPaths) error { return nil }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.ManifestPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDBusActivationManifest(paths.ManifestPath); err == nil {
		t.Fatal("loose manifest mode was accepted")
	}
	if err := os.Remove(paths.ManifestPath); err != nil {
		t.Fatal(err)
	}
	target := paths.ManifestPath + ".target"
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, paths.ManifestPath); err != nil {
		t.Fatal(err)
	}
	if _, err := readDBusActivationManifest(paths.ManifestPath); err == nil {
		t.Fatal("symlink manifest was accepted")
	}
}

func testDBusActivationPaths(t *testing.T) dbusActivationPaths {
	t.Helper()
	root := t.TempDir()
	return dbusActivationPaths{
		ControlPath:  filepath.Join(root, "run", "control.sock"),
		HelperPath:   filepath.Join(root, "usr", "libexec", "helper"),
		DaemonConfig: filepath.Join(root, "etc", "dbus-1", "system.d", "50-servicectl-activation.conf"),
		ManifestPath: filepath.Join(root, "var", "lib", "servicectl", "dbus-activation", "manifest.json"),
	}
}
