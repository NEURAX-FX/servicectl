package cgrouptrack

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCgroupFSOnlyWritesProcs(t *testing.T) {
	root := t.TempDir()
	fs, err := openCgroupFS(root, skipMagicCheck)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	key := UnitKey{Mode: ModeSystem, Unit: "demo.service"}
	if err := fs.Ensure(key); err != nil {
		t.Fatal(err)
	}
	procs := filepath.Join(root, "system", mustEncoded(t, key), "cgroup.procs")
	if err := os.WriteFile(procs, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fs.MovePID(key, 42); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(procs)
	if err != nil || string(content) != "42\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
}

func TestCgroupFSRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("/tmp", filepath.Join(root, "system")); err != nil {
		t.Fatal(err)
	}
	if _, err := openCgroupFS(root, skipMagicCheck); err == nil {
		t.Fatal("symlink root accepted")
	}
}

func TestCgroupFSUserPathAndPIDListing(t *testing.T) {
	root := t.TempDir()
	fs, err := openCgroupFS(root, skipMagicCheck)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	key := UnitKey{Mode: ModeUser, UID: 1000, Unit: "demo.service"}
	if err := fs.Ensure(key); err != nil {
		t.Fatal(err)
	}
	procs := filepath.Join(root, "user", "1000", mustEncoded(t, key), "cgroup.procs")
	if err := os.WriteFile(procs, []byte("20\n3\n20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pids, err := fs.PIDs(key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pids, []int{3, 20}) {
		t.Fatalf("pids = %#v", pids)
	}
	if got := fs.Path(key); got != filepath.Join(root, "user", "1000", mustEncoded(t, key)) {
		t.Fatalf("path = %q", got)
	}
}

func TestCgroupFSRemoveOnlyWhenEmpty(t *testing.T) {
	root := t.TempDir()
	fs, err := openCgroupFS(root, skipMagicCheck)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	key := UnitKey{Mode: ModeSystem, Unit: "demo.service"}
	if err := fs.Ensure(key); err != nil {
		t.Fatal(err)
	}
	procs := filepath.Join(fs.Path(key), "cgroup.procs")
	if err := os.WriteFile(procs, []byte("42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if removed, err := fs.RemoveIfEmpty(key); err != nil || removed {
		t.Fatalf("populated remove = %v, %v", removed, err)
	}
	if err := os.WriteFile(procs, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if removed, err := fs.RemoveIfEmpty(key); err != nil || !removed {
		t.Fatalf("empty remove = %v, %v", removed, err)
	}
}

func TestCgroupFSScanIgnoresMalformedDirectories(t *testing.T) {
	root := t.TempDir()
	fs, err := openCgroupFS(root, skipMagicCheck)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	valid := UnitKey{Mode: ModeSystem, Unit: "demo.service"}
	if err := fs.Ensure(valid); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fs.Path(valid), "cgroup.procs"), []byte("5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "system", "invalid!"), 0o755); err != nil {
		t.Fatal(err)
	}
	groups, err := fs.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Key != valid || !reflect.DeepEqual(groups[0].PIDs, []int{5}) {
		t.Fatalf("groups = %#v", groups)
	}
}

func mustEncoded(t *testing.T, key UnitKey) string {
	t.Helper()
	encoded, err := key.EncodedUnit()
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
