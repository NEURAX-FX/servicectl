package cgrouptrack

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"servicectl/internal/procinfo"
)

func TestMigratorMovesMainPIDBeforeDescendants(t *testing.T) {
	proc := newFakeMigrationProc(
		Process{PID: 10, PPID: 1, UID: 0, StartTime: 100, State: 'S'},
		Process{PID: 11, PPID: 10, UID: 1000, StartTime: 101, State: 'S'},
		Process{PID: 12, PPID: 11, UID: 1000, StartTime: 102, State: 'S'},
	)
	groups := newRecordingGroups()
	m := Migrator{Proc: proc, Groups: groups, MaxRounds: 8, Deadline: time.Second}
	result := m.Migrate(context.Background(), systemInstance("demo.service", 10, 100))
	if result.State != StateTracked || result.Err != nil {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(groups.moves, []int{10, 11, 12}) {
		t.Fatalf("moves = %#v", groups.moves)
	}
}

func TestMigratorSkipsCrossUIDUserDescendant(t *testing.T) {
	proc := newFakeMigrationProc(
		Process{PID: 10, PPID: 1, UID: 1000, StartTime: 100, State: 'S'},
		Process{PID: 11, PPID: 10, UID: 1001, StartTime: 101, State: 'S'},
	)
	groups := newRecordingGroups()
	m := Migrator{Proc: proc, Groups: groups, MaxRounds: 2, Deadline: time.Second}
	result := m.Migrate(context.Background(), userInstance("demo.service", 1000, 10, 100))
	if result.State != StateTracked || !reflect.DeepEqual(groups.moves, []int{10}) {
		t.Fatalf("result=%#v moves=%#v", result, groups.moves)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].PID != 11 {
		t.Fatalf("skipped = %#v", result.Skipped)
	}
}

func TestMigratorRejectsChangedMainPIDIdentity(t *testing.T) {
	proc := newFakeMigrationProc(Process{PID: 10, PPID: 1, UID: 0, StartTime: 101, State: 'S'})
	result := (Migrator{Proc: proc, Groups: newRecordingGroups(), MaxRounds: 2, Deadline: time.Second}).Migrate(
		context.Background(), systemInstance("demo.service", 10, 100),
	)
	if result.State != StateDegraded || !errors.Is(result.Err, ErrProcessIdentityChanged) {
		t.Fatalf("result = %#v", result)
	}
}

func TestMigratorReturnsPartialAtRoundBound(t *testing.T) {
	proc := newFakeMigrationProc(Process{PID: 10, PPID: 1, UID: 0, StartTime: 100, State: 'S'})
	proc.afterList = func(call int, processes map[int]Process) {
		pid := 10 + call
		processes[pid] = Process{PID: pid, PPID: 10, UID: 0, StartTime: uint64(100 + pid), State: 'S'}
	}
	groups := newRecordingGroups()
	result := (Migrator{Proc: proc, Groups: groups, MaxRounds: 2, Deadline: time.Second}).Migrate(
		context.Background(), systemInstance("demo.service", 10, 100),
	)
	if result.State != StatePartial {
		t.Fatalf("result = %#v", result)
	}
}

func TestMigratorDetectsPostWriteIdentityChange(t *testing.T) {
	proc := newFakeMigrationProc(Process{PID: 10, PPID: 1, UID: 0, StartTime: 100, State: 'S'})
	groups := newRecordingGroups()
	groups.afterMove = func(pid int) { proc.processes[pid] = Process{PID: pid, PPID: 1, UID: 0, StartTime: 999, State: 'S'} }
	result := (Migrator{Proc: proc, Groups: groups, MaxRounds: 2, Deadline: time.Second}).Migrate(
		context.Background(), systemInstance("demo.service", 10, 100),
	)
	if result.State != StateDegraded || !errors.Is(result.Err, ErrProcessIdentityChanged) {
		t.Fatalf("result = %#v", result)
	}
}

func TestMigratorDetectsPostWriteMembershipFailure(t *testing.T) {
	proc := newFakeMigrationProc(Process{PID: 10, PPID: 1, UID: 0, StartTime: 100, State: 'S'})
	groups := newRecordingGroups()
	groups.ignoreMoves = true
	result := (Migrator{Proc: proc, Groups: groups, MaxRounds: 2, Deadline: time.Second}).Migrate(
		context.Background(), systemInstance("demo.service", 10, 100),
	)
	if result.State != StateDegraded || !errors.Is(result.Err, ErrMembershipNotObserved) {
		t.Fatalf("result = %#v", result)
	}
}

type fakeMigrationProc struct {
	processes map[int]Process
	listCalls int
	afterList func(int, map[int]Process)
}

func newFakeMigrationProc(processes ...Process) *fakeMigrationProc {
	result := &fakeMigrationProc{processes: make(map[int]Process)}
	for _, process := range processes {
		process.PIDFD = -1
		result.processes[process.PID] = process
	}
	return result
}

func (p *fakeMigrationProc) ListPIDs() ([]int, error) {
	p.listCalls++
	if p.afterList != nil {
		p.afterList(p.listCalls, p.processes)
	}
	pids := make([]int, 0, len(p.processes))
	for pid := range p.processes {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}

func (p *fakeMigrationProc) Inspect(pid int) (Process, error) {
	process, ok := p.processes[pid]
	if !ok {
		return Process{}, errors.New("process disappeared")
	}
	return process, nil
}

func (p *fakeMigrationProc) ReadStat(pid int) (procinfo.Stat, error)    { panic("not used") }
func (p *fakeMigrationProc) ReadStatus(pid int) (ProcStatus, error)     { panic("not used") }
func (p *fakeMigrationProc) ReadCgroup(pid int) (string, error)         { panic("not used") }
func (p *fakeMigrationProc) ReadExecutable(pid int) (string, error)     { panic("not used") }
func (p *fakeMigrationProc) OpenPIDFD(pid int) (int, error)             { panic("not used") }
func (p *fakeMigrationProc) PIDNamespace(pid int) (FileIdentity, error) { panic("not used") }

type recordingGroups struct {
	moves       []int
	members     map[UnitKey]map[int]bool
	ignoreMoves bool
	afterMove   func(int)
}

func newRecordingGroups() *recordingGroups {
	return &recordingGroups{members: make(map[UnitKey]map[int]bool)}
}

func (g *recordingGroups) Ensure(UnitKey) error { return nil }
func (g *recordingGroups) MovePID(key UnitKey, pid int) error {
	g.moves = append(g.moves, pid)
	if !g.ignoreMoves {
		if g.members[key] == nil {
			g.members[key] = make(map[int]bool)
		}
		g.members[key][pid] = true
	}
	if g.afterMove != nil {
		g.afterMove(pid)
	}
	return nil
}
func (g *recordingGroups) PIDs(key UnitKey) ([]int, error) {
	pids := make([]int, 0, len(g.members[key]))
	for pid := range g.members[key] {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}
func (g *recordingGroups) RemoveIfEmpty(UnitKey) (bool, error) { return false, nil }
func (g *recordingGroups) Scan() ([]GroupSnapshot, error)      { return nil, nil }
func (g *recordingGroups) Path(UnitKey) string                 { return "/fake" }

func systemInstance(unit string, pid int, start uint64) InstanceIdentity {
	return InstanceIdentity{
		UnitKey: UnitKey{Mode: ModeSystem, Unit: unit}, BootID: "boot-a",
		MainPID: pid, MainPIDStartTime: start, VisionEpoch: "epoch-a", Generation: 1,
	}
}

func userInstance(unit string, uid uint32, pid int, start uint64) InstanceIdentity {
	identity := systemInstance(unit, pid, start)
	identity.Mode = ModeUser
	identity.UID = uid
	return identity
}
