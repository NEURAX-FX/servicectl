package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSystemHelperBinaryPathPrefersSystemDirOverPATH(t *testing.T) {
	t.Setenv("PATH", filepath.Join(string(os.PathSeparator), "nonexistent"))

	tempDir := t.TempDir()
	userBin := filepath.Join(tempDir, "user-bin")
	systemBin := filepath.Join(tempDir, "system-bin")
	if err := os.MkdirAll(userBin, 0o755); err != nil {
		t.Fatalf("mkdir user bin: %v", err)
	}
	if err := os.MkdirAll(systemBin, 0o755); err != nil {
		t.Fatalf("mkdir system bin: %v", err)
	}

	userNotifyd := filepath.Join(userBin, "sys-notifyd")
	if err := os.WriteFile(userNotifyd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write user helper: %v", err)
	}
	systemNotifyd := filepath.Join(systemBin, "sys-notifyd")
	if err := os.WriteFile(systemNotifyd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write system helper: %v", err)
	}

	oldPreferred := systemHelperBinaryDirs
	systemHelperBinaryDirs = []string{systemBin}
	t.Cleanup(func() {
		systemHelperBinaryDirs = oldPreferred
	})

	oldExecutableDir := siblingExecutableDirFunc
	siblingExecutableDirFunc = func() string { return filepath.Join(tempDir, "missing-sibling") }
	t.Cleanup(func() {
		siblingExecutableDirFunc = oldExecutableDir
	})

	oldLookPath := lookPathFunc
	lookPathFunc = func(name string) (string, error) {
		if name != "sys-notifyd" {
			t.Fatalf("unexpected lookup for %q", name)
		}
		return userNotifyd, nil
	}
	t.Cleanup(func() {
		lookPathFunc = oldLookPath
	})

	if got := systemHelperBinaryPath("sys-notifyd"); got != systemNotifyd {
		t.Fatalf("systemHelperBinaryPath returned %q, want %q", got, systemNotifyd)
	}
}

func TestUserBinaryPathPrefersPATHResult(t *testing.T) {
	tempDir := t.TempDir()
	pathBin := filepath.Join(tempDir, "path-bin")
	if err := os.MkdirAll(pathBin, 0o755); err != nil {
		t.Fatalf("mkdir path bin: %v", err)
	}
	pathServicectl := filepath.Join(pathBin, "servicectl")
	if err := os.WriteFile(pathServicectl, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write path servicectl: %v", err)
	}

	oldExecutableDir := siblingExecutableDirFunc
	siblingExecutableDirFunc = func() string { return filepath.Join(tempDir, "missing-sibling") }
	t.Cleanup(func() {
		siblingExecutableDirFunc = oldExecutableDir
	})

	oldLookPath := lookPathFunc
	lookPathFunc = func(name string) (string, error) {
		if name != "servicectl" {
			t.Fatalf("unexpected lookup for %q", name)
		}
		return pathServicectl, nil
	}
	t.Cleanup(func() {
		lookPathFunc = oldLookPath
	})

	oldPreferred := systemHelperBinaryDirs
	systemHelperBinaryDirs = nil
	t.Cleanup(func() {
		systemHelperBinaryDirs = oldPreferred
	})

	if got := userBinaryPath("servicectl"); got != pathServicectl {
		t.Fatalf("userBinaryPath returned %q, want %q", got, pathServicectl)
	}
}
