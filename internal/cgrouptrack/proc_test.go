package cgrouptrack

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDescendantsAreParentFirst(t *testing.T) {
	processes := []Process{
		{PID: 12, PPID: 11},
		{PID: 10, PPID: 1},
		{PID: 13, PPID: 10},
		{PID: 11, PPID: 10},
	}
	if got := Descendants(10, processes); !reflect.DeepEqual(got, []int{11, 13, 12}) {
		t.Fatalf("descendants = %#v", got)
	}
}

func TestDescendantsIgnoreCycles(t *testing.T) {
	processes := []Process{{PID: 10, PPID: 11}, {PID: 11, PPID: 10}}
	if got := Descendants(10, processes); !reflect.DeepEqual(got, []int{11}) {
		t.Fatalf("descendants = %#v", got)
	}
}

func TestLinuxProcFSInspectStableProcess(t *testing.T) {
	root := t.TempDir()
	writeFakeProcess(t, root, 42, 7, 1000, 'S', 12345, "worker", "0::/old\n", true)
	proc := NewLinuxProcFS(root)
	proc.openPIDFD = func(pid int) (int, error) {
		if pid != 42 {
			t.Fatalf("pidfd pid = %d", pid)
		}
		return 99, nil
	}

	process, err := proc.Inspect(42)
	if err != nil {
		t.Fatal(err)
	}
	if process.PID != 42 || process.PPID != 7 || process.UID != 1000 || process.StartTime != 12345 || process.Cgroup != "/old" || process.PIDFD != 99 {
		t.Fatalf("process = %#v", process)
	}
}

func TestLinuxProcFSRejectsZombieAndKernelThread(t *testing.T) {
	root := t.TempDir()
	writeFakeProcess(t, root, 42, 1, 0, 'Z', 100, "zombie", "0::/\n", true)
	writeFakeProcess(t, root, 43, 1, 0, 'S', 101, "kthread", "0::/\n", false)
	proc := NewLinuxProcFS(root)
	proc.openPIDFD = func(pid int) (int, error) { return pid, nil }

	if _, err := proc.Inspect(42); !errors.Is(err, ErrZombieProcess) {
		t.Fatalf("zombie error = %v", err)
	}
	if _, err := proc.Inspect(43); !errors.Is(err, ErrKernelThread) {
		t.Fatalf("kernel thread error = %v", err)
	}
}

func TestLinuxProcFSReadsOnlyUnifiedCgroup(t *testing.T) {
	root := t.TempDir()
	writeFakeProcess(t, root, 42, 1, 1000, 'S', 100, "worker", "5:memory:/legacy\n0::/unified/path\n", true)
	proc := NewLinuxProcFS(root)
	if got, err := proc.ReadCgroup(42); err != nil || got != "/unified/path" {
		t.Fatalf("cgroup = %q, %v", got, err)
	}
}

func TestLinuxProcFSPIDNamespaceIdentity(t *testing.T) {
	root := t.TempDir()
	writeFakeProcess(t, root, 42, 1, 1000, 'S', 100, "worker", "0::/\n", true)
	nsTarget := filepath.Join(root, "namespace")
	if err := os.WriteFile(nsTarget, []byte("ns"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(nsTarget, filepath.Join(root, "42", "ns", "pid")); err != nil {
		t.Fatal(err)
	}
	identity, err := NewLinuxProcFS(root).PIDNamespace(42)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Device == 0 && identity.Inode == 0 {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestLinuxProcFSListPIDsIsNumericAndSorted(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"20", "3", "self", "abc"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pids, err := NewLinuxProcFS(root).ListPIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pids, []int{3, 20}) {
		t.Fatalf("pids = %#v", pids)
	}
}

func writeFakeProcess(t *testing.T, root string, pid int, ppid int, uid uint32, state byte, startTime uint64, comm string, cgroup string, executable bool) {
	t.Helper()
	dir := filepath.Join(root, integerString(pid))
	if err := os.MkdirAll(filepath.Join(dir, "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	fields := make([]string, 20)
	fields[0] = string(state)
	fields[1] = integerString(ppid)
	for i := 2; i < 19; i++ {
		fields[i] = "0"
	}
	fields[19] = uint64String(startTime)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(integerString(pid)+" ("+comm+") "+strings.Join(fields, " ")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte("Name:\t"+comm+"\nUid:\t"+uint64String(uint64(uid))+"\t"+uint64String(uint64(uid))+"\t"+uint64String(uint64(uid))+"\t"+uint64String(uint64(uid))+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte(cgroup), 0o644); err != nil {
		t.Fatal(err)
	}
	if executable {
		target := filepath.Join(root, "bin-"+integerString(pid))
		if err := os.WriteFile(target, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "exe")); err != nil {
			t.Fatal(err)
		}
	}
}
