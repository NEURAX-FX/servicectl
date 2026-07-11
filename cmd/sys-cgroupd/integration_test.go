package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"servicectl/internal/cgrouptrack"
)

func TestCgroupV2Integration(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("SERVICECTL_CGROUP_TEST_ROOT"))
	if root == "" {
		t.Skip("SERVICECTL_CGROUP_TEST_ROOT is not set")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("test root is not a real directory: %s", root)
	}
	groups, err := cgrouptrack.OpenCgroupFS(root)
	if err != nil {
		t.Fatalf("test root is not a writable cgroup v2 subtree: %v", err)
	}
	var command *exec.Cmd
	var extra *exec.Cmd
	var key cgrouptrack.UnitKey
	t.Cleanup(func() {
		stopFixtureCommand(extra, false)
		stopFixtureCommand(command, true)
		if key.Unit != "" {
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				members, membersErr := groups.PIDs(key)
				if membersErr == nil && len(members) == 0 {
					_, _ = groups.RemoveIfEmpty(key)
					_ = os.Remove(filepath.Join(root, "system"))
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		_ = groups.Close()
	})

	command = exec.Command("/bin/sh", "-c", "sleep 30 & wait")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	mainPID := command.Process.Pid

	proc := cgrouptrack.NewLinuxProcFS("/proc")
	mainProcess := waitForProcess(t, proc, mainPID)
	childPID := waitForChild(t, proc, mainPID)
	unit := "integration-" + strconv.Itoa(mainPID) + ".service"
	key = cgrouptrack.UnitKey{Mode: cgrouptrack.ModeSystem, Unit: unit}
	identity := cgrouptrack.InstanceIdentity{
		UnitKey: key,
		BootID:  "integration-boot", MainPID: mainPID,
		MainPIDStartTime: mainProcess.StartTime, VisionEpoch: "integration-epoch", Generation: 1,
	}
	result := (cgrouptrack.Migrator{
		Proc: proc, Groups: groups, MaxRounds: 8, Deadline: 2 * time.Second,
	}).Migrate(context.Background(), identity)
	if result.Err != nil || result.State != cgrouptrack.StateTracked {
		t.Fatalf("migration result = %#v", result)
	}
	if len(result.Moved) < 2 || result.Moved[0] != mainPID {
		t.Fatalf("MainPID was not migrated first: %#v", result.Moved)
	}
	members, err := groups.PIDs(identity.UnitKey)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPID(members, mainPID) || !containsPID(members, childPID) {
		t.Fatalf("members = %#v, want MainPID %d and child %d", members, mainPID, childPID)
	}

	extra = exec.Command("/bin/sleep", "30")
	if err := extra.Start(); err != nil {
		t.Fatal(err)
	}
	extraProcess := waitForProcess(t, proc, extra.Process.Pid)
	if err := groups.MovePID(identity.UnitKey, extraProcess.PID); err != nil {
		t.Fatal(err)
	}
	members, err = groups.PIDs(identity.UnitKey)
	if err != nil || !containsPID(members, extraProcess.PID) {
		t.Fatalf("explicit attach membership = %#v, %v", members, err)
	}

	if removed, err := groups.RemoveIfEmpty(identity.UnitKey); err != nil || removed {
		t.Fatalf("populated cgroup was removed: removed=%v err=%v", removed, err)
	}
}

func stopFixtureCommand(command *exec.Cmd, processGroup bool) {
	if command == nil || command.Process == nil {
		return
	}
	pid := command.Process.Pid
	if processGroup {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	} else {
		_ = command.Process.Signal(syscall.SIGTERM)
	}
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		if processGroup {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		} else {
			_ = command.Process.Signal(syscall.SIGKILL)
		}
		<-done
	}
}

func waitForProcess(t *testing.T, proc cgrouptrack.ProcFS, pid int) cgrouptrack.Process {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		process, err := proc.Inspect(pid)
		if err == nil {
			if process.PIDFD >= 0 {
				_ = syscall.Close(process.PIDFD)
				process.PIDFD = -1
			}
			return process
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d did not become inspectable", pid)
	return cgrouptrack.Process{}
}

func waitForChild(t *testing.T, proc cgrouptrack.ProcFS, parent int) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pids, err := proc.ListPIDs()
		if err != nil {
			t.Fatal(err)
		}
		for _, pid := range pids {
			process, err := proc.Inspect(pid)
			if err != nil {
				continue
			}
			if process.PIDFD >= 0 {
				_ = syscall.Close(process.PIDFD)
			}
			if process.PPID == parent {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(errors.New("fixture child did not appear"))
	return 0
}
