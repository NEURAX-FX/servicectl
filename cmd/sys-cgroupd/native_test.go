package main

import (
	"context"
	"path/filepath"
	"servicectl/internal/cgrouptrack"
	"testing"
)

func TestNativeReservationSurvivesStoppedAndReconcile(t *testing.T) {
	groups := newSchedulerGroups()
	s := newScheduler(schedulerOptions{groups: groups, proc: newSchedulerProc(), bootID: "boot-a"})
	key := cgrouptrack.UnitKey{Mode: cgrouptrack.ModeSystem, Unit: "native.service"}
	s.registryPath = filepath.Join(t.TempDir(), "registry.json")
	if _, err := s.Prepare(context.Background(), cgrouptrack.Scope{Mode: key.Mode}, key.Unit); err != nil {
		t.Fatal(err)
	}
	registry, err := cgrouptrack.ReadRegistry(s.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	s = newScheduler(schedulerOptions{groups: groups, proc: newSchedulerProc(), bootID: "boot-a"})
	s.LoadRegistry(registry)
	s.units[key] = &unitWork{identity: cgrouptrack.InstanceIdentity{UnitKey: key}, state: cgrouptrack.StateStopped}
	s.Stopped(key, "epoch", 1)
	if err := s.ReconcileGroups(context.Background()); err != nil {
		t.Fatal(err)
	}
	groupsFound, err := groups.Scan()
	if err != nil || len(groupsFound) != 1 || groupsFound[0].Key != key {
		t.Fatalf("declared group was reclaimed: %+v, %v", groupsFound, err)
	}
	if err := s.Release(context.Background(), cgrouptrack.Scope{Mode: key.Mode}, key.Unit); err != nil {
		t.Fatal(err)
	}
	remaining, err := groups.Scan()
	if err != nil || len(remaining) != 0 {
		t.Fatalf("released group not reclaimed: %+v %v", remaining, err)
	}
}

func TestNativeVerificationDoesNotMoveEscapedProcess(t *testing.T) {
	key := cgrouptrack.UnitKey{Mode: cgrouptrack.ModeSystem, Unit: "native.service"}
	groups := newSchedulerGroups()
	groups.Ensure(key)
	proc := newSchedulerProc(cgrouptrack.Process{PID: 42, StartTime: 100, State: 'S'})
	s := newScheduler(schedulerOptions{groups: groups, proc: proc, bootID: "boot-a"})
	identity := cgrouptrack.InstanceIdentity{UnitKey: key, MainPID: 42, MainPIDStartTime: 100}
	result := s.verifyNative(identity)
	if result.State != cgrouptrack.StateDegraded || len(groups.moves) != 0 {
		t.Fatalf("escaped native process silently migrated: %+v moves=%v", result, groups.moves)
	}
	groups.members[key][42] = true
	result = s.verifyNative(identity)
	if result.State != cgrouptrack.StateTracked {
		t.Fatalf("native member not verified: %+v", result)
	}
}
