package cgrouptrack

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRegistryRoundTripAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	want := Registry{Version: 1, Units: []UnitRecord{{
		Identity: systemInstance("demo.service", 10, 100), State: StateTracked,
		LastMigration: "2026-07-11T00:00:00Z",
	}}}
	if err := WriteRegistry(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	got, err := ReadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%#v want=%#v", got, want)
	}
}

func TestRegistryMissingReturnsEmptyVersionOne(t *testing.T) {
	got, err := ReadRegistry(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || len(got.Units) != 0 {
		t.Fatalf("registry = %#v", got)
	}
}

func TestRegistryQuarantinesCorruption(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "registry.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadOrQuarantine(path, time.Unix(100, 0))
	if err == nil || got.Version != 1 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("quarantine files = %#v, %v", matches, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original registry remains: %v", err)
	}
}

func TestRegistryRejectsLooseModeSymlinkAndOversize(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "registry.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegistry(path); err == nil {
		t.Fatal("loose mode accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegistry(path); err == nil {
		t.Fatal("symlink registry accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, MaxRegistryBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegistry(path); err == nil {
		t.Fatal("oversized registry accepted")
	}
}

func TestRegistryRejectsInvalidUnitRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	invalid := Registry{Version: 1, Units: []UnitRecord{{Identity: InstanceIdentity{}, State: StateTracked}}}
	if err := WriteRegistry(path, invalid); err == nil {
		t.Fatal("invalid record was written")
	}
}
