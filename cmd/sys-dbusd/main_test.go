package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"servicectl/internal/dbusactivation"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.controlPath != "/run/servicectl/sys-dbusd/control.sock" {
		t.Fatalf("control path = %q", cfg.controlPath)
	}
	if cfg.activationTimeout != 30*time.Second {
		t.Fatalf("activation timeout = %s", cfg.activationTimeout)
	}
	if len(cfg.serviceDirs) == 0 || len(cfg.systemdPaths) == 0 {
		t.Fatalf("paths service=%#v systemd=%#v", cfg.serviceDirs, cfg.systemdPaths)
	}
}

func TestParseConfigOverrides(t *testing.T) {
	cfg, err := parseConfig([]string{
		"-control-path", "/tmp/control.sock",
		"-service-dir", "/tmp/services-one",
		"-service-dir", "/tmp/services-two",
		"-systemd-path", "/tmp/units",
		"-activation-timeout", "5s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.controlPath != "/tmp/control.sock" || cfg.activationTimeout != 5*time.Second {
		t.Fatalf("config = %#v", cfg)
	}
	if !reflect.DeepEqual([]string(cfg.serviceDirs), []string{"/tmp/services-one", "/tmp/services-two"}) {
		t.Fatalf("service dirs = %#v", cfg.serviceDirs)
	}
	if !reflect.DeepEqual([]string(cfg.systemdPaths), []string{"/tmp/units"}) {
		t.Fatalf("systemd paths = %#v", cfg.systemdPaths)
	}
}

func TestParseConfigRejectsInvalidValues(t *testing.T) {
	if _, err := parseConfig([]string{"-activation-timeout", "0s"}); err == nil {
		t.Fatal("zero activation timeout accepted")
	}
	if _, err := parseConfig([]string{"unexpected"}); err == nil {
		t.Fatal("positional argument accepted")
	}
}

func TestAuthorizePeerByFrontendAndExecutable(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(helper, []byte("first"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := daemonConfig{
		helperPath: helper,
		adminPath:  "/usr/bin/servicectl",
		daemonPID:  4242,
	}
	authorize, err := peerAuthorizer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		frontend dbusactivation.Frontend
		peer     dbusactivation.Peer
		wantErr  bool
	}{
		{dbusactivation.FrontendDaemonHelper, dbusactivation.Peer{UID: 0, ParentPID: 4300, Ancestors: []int32{4300, 4242}, Executable: cfg.helperPath}, false},
		{dbusactivation.FrontendAdmin, dbusactivation.Peer{UID: 0, Executable: cfg.adminPath}, false},
		{dbusactivation.FrontendDaemonHelper, dbusactivation.Peer{UID: 1000, ParentPID: 4300, Ancestors: []int32{4300, 4242}, Executable: cfg.helperPath}, true},
		{dbusactivation.FrontendDaemonHelper, dbusactivation.Peer{UID: 0, ParentPID: 4300, Ancestors: []int32{4300, 4242}, Executable: "/tmp/helper"}, true},
		{dbusactivation.FrontendDaemonHelper, dbusactivation.Peer{UID: 0, ParentPID: 9999, Ancestors: []int32{9999, 1}, Executable: cfg.helperPath}, true},
		{dbusactivation.FrontendAdmin, dbusactivation.Peer{UID: 0, Executable: cfg.helperPath}, true},
	}
	for _, tt := range tests {
		err := authorize(tt.frontend, tt.peer)
		if (err != nil) != tt.wantErr {
			t.Fatalf("authorize(%d, %#v) = %v", tt.frontend, tt.peer, err)
		}
	}
}

func TestAuthorizePeerRejectsReplacedExecutableAtExpectedPath(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(helper, []byte("first"), 0o755); err != nil {
		t.Fatal(err)
	}
	authorize, err := peerAuthorizer(daemonConfig{helperPath: helper, daemonPID: 4242})
	if err != nil {
		t.Fatal(err)
	}
	replacement := helper + ".new"
	if err := os.WriteFile(replacement, []byte("second"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, helper); err != nil {
		t.Fatal(err)
	}
	if err := authorize(dbusactivation.FrontendDaemonHelper, dbusactivation.Peer{UID: 0, ParentPID: 4300, Ancestors: []int32{4300, 4242}, Executable: helper}); err == nil {
		t.Fatal("replaced helper executable was authorized")
	}
}

func TestDefaultManagedControlPath(t *testing.T) {
	resolver := dbusactivation.NewSystemdUnitResolver([]string{t.TempDir()}, "/run/servicectl/managed")
	_ = resolver
	want := filepath.Join("/run/servicectl/managed", "unit-dbusd", "control.sock")
	if got := managedControlPath("/run/servicectl/managed", "unit-dbusd"); got != want {
		t.Fatalf("managedControlPath = %q, want %q", got, want)
	}
}

func TestPrepareManagedUnitUsesCurrentServicectlStartAPI(t *testing.T) {
	directory := t.TempDir()
	arguments := filepath.Join(directory, "arguments")
	servicectl := filepath.Join(directory, "servicectl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >" + arguments + "\n"
	if err := os.WriteFile(servicectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := prepareManagedUnit(context.Background(), servicectl, "systemd-localed"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "start\nsystemd-localed\n" {
		t.Fatalf("arguments = %q", content)
	}
}

func TestIndexStoreKeepsHealthyIndexWhenReloadIsInvalid(t *testing.T) {
	directory := t.TempDir()
	validPath := filepath.Join(directory, "org.example.Service.service")
	valid := "[D-BUS Service]\nName=org.example.Service\nExec=/bin/true\nUser=root\n"
	if err := os.WriteFile(validPath, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &indexStore{}
	if err := store.load([]string{directory}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.get().Lookup("org.example.Service"); err != nil {
		t.Fatal(err)
	}
	invalid := "[D-BUS Service]\nName=org.example.Service\nExec=/bin/true\n"
	if err := os.WriteFile(validPath, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.load([]string{directory}); err == nil {
		t.Fatal("invalid reload unexpectedly succeeded")
	}
	if _, err := store.get().Lookup("org.example.Service"); err != nil {
		t.Fatalf("healthy index was replaced: %v", err)
	}
}

func TestIndexStoreInitialLoadFailsOnInvalidService(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "org.example.Service.service")
	if err := os.WriteFile(path, []byte("[D-BUS Service]\nName=org.example.Service\nExec=/bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &indexStore{}
	if err := store.load([]string{directory}); err == nil {
		t.Fatal("invalid initial index unexpectedly succeeded")
	}
	if store.get() != nil {
		t.Fatal("invalid initial index was installed")
	}
	if !errors.Is(store.load(nil), nil) {
		t.Fatal("empty index should be valid")
	}
}

func TestIndexStoreStatusHandlesMissingIndex(t *testing.T) {
	store := &indexStore{}
	status := string(store.status())
	if status == "" || !reflect.DeepEqual(status, `{"errors":[],"generation":0,"healthy":false,"services":0}`) {
		t.Fatalf("status = %s", status)
	}
}
