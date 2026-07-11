package main

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"servicectl/internal/cgrouptrack"
	"servicectl/internal/procinfo"
	"servicectl/internal/visionapi"
)

func TestConfigBounds(t *testing.T) {
	cfg, err := parseConfig([]string{"--cgroup-root=/tmp/cg", "--settle-delay=100ms", "--migration-max-rounds=8"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cgroupRoot != "/tmp/cg" || cfg.settleDelay != 100*time.Millisecond || cfg.maxRounds != 8 {
		t.Fatalf("config = %#v", cfg)
	}
	for _, args := range [][]string{{"--settle-delay=1us"}, {"--reconcile-interval=100ms"}, {"--migration-deadline=1us"}, {"--migration-max-rounds=65"}} {
		if _, err := parseConfig(args); err == nil {
			t.Fatalf("unsafe config accepted: %#v", args)
		}
	}
}

func TestNewGenerationCancelsPendingMigration(t *testing.T) {
	clock := &manualClock{}
	proc := newSchedulerProc(
		cgrouptrack.Process{PID: 10, UID: 0, StartTime: 100, State: 'S'},
		cgrouptrack.Process{PID: 11, UID: 0, StartTime: 200, State: 'S'},
	)
	groups := newSchedulerGroups()
	source := &fakeVisionSource{units: map[string]visionapi.UnitSnapshot{}}
	s := newScheduler(schedulerOptions{
		bootID: "boot-a", settleDelay: 100 * time.Millisecond, afterFunc: clock.AfterFunc,
		proc: proc, groups: groups, migrator: cgrouptrack.Migrator{Proc: proc, Groups: groups, MaxRounds: 2, Deadline: time.Second},
	})
	first := readySnapshot("demo.service", visionapi.ModeSystem, 0, 10, 100, "epoch-a", 1)
	second := readySnapshot("demo.service", visionapi.ModeSystem, 0, 11, 200, "epoch-a", 2)
	source.units[first.Name] = first
	s.Ready(source, first)
	source.units[second.Name] = second
	s.Ready(source, second)
	clock.Advance(100 * time.Millisecond)
	if !reflect.DeepEqual(groups.moves, []int{11}) {
		t.Fatalf("moves = %#v", groups.moves)
	}
}

func TestSameGenerationDoesNotRescheduleTrackedUnit(t *testing.T) {
	clock := &manualClock{}
	proc := newSchedulerProc(cgrouptrack.Process{PID: 10, UID: 0, StartTime: 100, State: 'S'})
	groups := newSchedulerGroups()
	source := &fakeVisionSource{units: map[string]visionapi.UnitSnapshot{}}
	snapshot := readySnapshot("demo.service", visionapi.ModeSystem, 0, 10, 100, "epoch-a", 1)
	source.units[snapshot.Name] = snapshot
	s := newScheduler(schedulerOptions{
		bootID: "boot-a", settleDelay: 100 * time.Millisecond, afterFunc: clock.AfterFunc,
		proc: proc, groups: groups, migrator: cgrouptrack.Migrator{Proc: proc, Groups: groups, MaxRounds: 2, Deadline: time.Second},
	})
	s.Ready(source, snapshot)
	clock.Advance(100 * time.Millisecond)
	s.Ready(source, snapshot)
	clock.Advance(100 * time.Millisecond)
	if !reflect.DeepEqual(groups.moves, []int{10}) {
		t.Fatalf("duplicate snapshot caused migration: %#v", groups.moves)
	}
}

func TestSameGenerationReschedulesOfflineUnit(t *testing.T) {
	clock := &manualClock{}
	proc := newSchedulerProc(cgrouptrack.Process{PID: 10, UID: 0, StartTime: 100, State: 'S'})
	groups := newSchedulerGroups()
	source := &fakeVisionSource{units: map[string]visionapi.UnitSnapshot{}}
	snapshot := readySnapshot("demo.service", visionapi.ModeSystem, 0, 10, 100, "epoch-a", 1)
	source.units[snapshot.Name] = snapshot
	s := newScheduler(schedulerOptions{
		bootID: "boot-a", settleDelay: 100 * time.Millisecond, afterFunc: clock.AfterFunc,
		proc: proc, groups: groups, migrator: cgrouptrack.Migrator{Proc: proc, Groups: groups, MaxRounds: 2, Deadline: time.Second},
	})
	s.Ready(source, snapshot)
	s.MarkSourceOffline(source, errors.New("offline"))
	s.Ready(source, snapshot)
	clock.Advance(100 * time.Millisecond)
	if !reflect.DeepEqual(groups.moves, []int{10}) {
		t.Fatalf("moves = %#v", groups.moves)
	}
}

func TestStoppedCancelsPendingMigrationAndPreservesPopulatedGroup(t *testing.T) {
	clock := &manualClock{}
	proc := newSchedulerProc(cgrouptrack.Process{PID: 10, UID: 0, StartTime: 100, State: 'S'})
	groups := newSchedulerGroups()
	s := newScheduler(schedulerOptions{bootID: "boot-a", settleDelay: time.Second, afterFunc: clock.AfterFunc, proc: proc, groups: groups})
	snapshot := readySnapshot("demo.service", visionapi.ModeSystem, 0, 10, 100, "epoch-a", 1)
	s.Ready(&fakeVisionSource{}, snapshot)
	groups.members[cgrouptrack.UnitKey{Mode: cgrouptrack.ModeSystem, Unit: "demo.service"}] = map[int]bool{10: true}
	s.Stopped(cgrouptrack.UnitKey{Mode: cgrouptrack.ModeSystem, Unit: "demo.service"}, "epoch-a", 2)
	clock.Advance(time.Second)
	status, err := s.GetUnit(context.Background(), cgrouptrack.Scope{Mode: cgrouptrack.ModeSystem}, "demo.service")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != cgrouptrack.StateOrphanedPopulated || len(groups.moves) != 0 {
		t.Fatalf("status=%#v moves=%#v", status, groups.moves)
	}
}

func TestAttachRevalidatesRunningTargetAndPID(t *testing.T) {
	proc := newSchedulerProc(
		cgrouptrack.Process{PID: 10, UID: 1000, StartTime: 100, State: 'S'},
		cgrouptrack.Process{PID: 42, UID: 1000, StartTime: 500, State: 'S'},
	)
	groups := newSchedulerGroups()
	source := &fakeVisionSource{units: map[string]visionapi.UnitSnapshot{}}
	snapshot := readySnapshot("demo.service", visionapi.ModeUser, 1000, 10, 100, "epoch-a", 1)
	source.units[snapshot.Name] = snapshot
	s := newScheduler(schedulerOptions{bootID: "boot-a", settleDelay: time.Second, proc: proc, groups: groups})
	s.Ready(source, snapshot)
	status, err := s.Attach(context.Background(), cgrouptrack.Scope{Mode: cgrouptrack.ModeUser, UID: 1000}, "demo.service", 42)
	if err != nil {
		t.Fatal(err)
	}
	if status.Identity.Unit != "demo.service" || !groups.members[status.Identity.UnitKey][42] {
		t.Fatalf("status=%#v groups=%#v", status, groups.members)
	}
}

func TestReconcileMarksUnknownAndRemovesEmptyStoppedGroup(t *testing.T) {
	groups := newSchedulerGroups()
	unknown := cgrouptrack.UnitKey{Mode: cgrouptrack.ModeSystem, Unit: "unknown.service"}
	empty := cgrouptrack.UnitKey{Mode: cgrouptrack.ModeSystem, Unit: "empty.service"}
	groups.members[unknown] = map[int]bool{44: true}
	groups.members[empty] = map[int]bool{}
	s := newScheduler(schedulerOptions{bootID: "boot-a", proc: newSchedulerProc(), groups: groups})
	if err := s.ReconcileGroups(context.Background()); err != nil {
		t.Fatal(err)
	}
	units, err := s.ListUnits(context.Background(), cgrouptrack.Scope{Global: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].Identity.Unit != "unknown.service" || units[0].State != cgrouptrack.StateUnknownUnit {
		t.Fatalf("units = %#v", units)
	}
	if _, ok := groups.members[empty]; ok {
		t.Fatal("empty unknown group was not removed")
	}
}

func TestStatusCountsOnlyAuthorizedScope(t *testing.T) {
	s := newScheduler(schedulerOptions{bootID: "boot-a", proc: newSchedulerProc(), groups: newSchedulerGroups()})
	s.units[cgrouptrack.UnitKey{Mode: cgrouptrack.ModeUser, UID: 1000, Unit: "a.service"}] = &unitWork{state: cgrouptrack.StatePending}
	s.units[cgrouptrack.UnitKey{Mode: cgrouptrack.ModeUser, UID: 1001, Unit: "b.service"}] = &unitWork{state: cgrouptrack.StateDegraded}
	status, err := s.Status(context.Background(), cgrouptrack.Scope{Mode: cgrouptrack.ModeUser, UID: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if status.Pending != 1 || status.Abnormal != 0 {
		t.Fatalf("status = %#v", status)
	}
}

func TestParseUnifiedMountIgnoresV1Controllers(t *testing.T) {
	mountinfo := "20 1 0:19 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n21 1 0:20 / /sys/fs/cgroup/freezer rw - cgroup cgroup rw,freezer\n"
	self := "5:freezer:/legacy\n0::/daemon\n"
	root, err := unifiedCgroupMount(mountinfo, self)
	if err != nil || root != "/sys/fs/cgroup" {
		t.Fatalf("root=%q err=%v", root, err)
	}
}

func TestSchedulerReplacesDegradedGroups(t *testing.T) {
	bad := &unavailableGroups{root: "/bad", err: errors.New("unavailable")}
	good := newSchedulerGroups()
	proc := newSchedulerProc(cgrouptrack.Process{PID: 10, UID: 0, StartTime: 100, State: 'S'})
	s := newScheduler(schedulerOptions{
		bootID: "boot-a", proc: proc, groups: bad,
		migrator: cgrouptrack.Migrator{Proc: proc, Groups: bad, MaxRounds: 2, Deadline: time.Second},
	})
	s.rootError = "unavailable"
	s.ReplaceGroups(good)
	status, err := s.Status(context.Background(), cgrouptrack.Scope{Global: true})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Healthy {
		t.Fatalf("status = %#v", status)
	}
	if s.currentGroups() != good || s.currentMigrator().Groups != good {
		t.Fatal("group backend was not replaced consistently")
	}
}

func TestSchedulerLoadsRegistryHints(t *testing.T) {
	s := newScheduler(schedulerOptions{bootID: "boot-a", proc: newSchedulerProc(), groups: newSchedulerGroups()})
	record := cgrouptrack.UnitRecord{
		Identity: cgrouptrack.InstanceIdentity{
			UnitKey: cgrouptrack.UnitKey{Mode: cgrouptrack.ModeSystem, Unit: "demo.service"},
			BootID:  "boot-a", MainPID: 10, MainPIDStartTime: 100, VisionEpoch: "epoch-a", Generation: 1,
		},
		State: cgrouptrack.StateTracked, RetryCount: 2, LastError: "old error",
	}
	s.LoadRegistry(cgrouptrack.Registry{Version: 1, Units: []cgrouptrack.UnitRecord{record}})
	status, err := s.GetUnit(context.Background(), cgrouptrack.Scope{Mode: cgrouptrack.ModeSystem}, "demo.service")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != cgrouptrack.StateTracked || status.LastError != "old error" {
		t.Fatalf("status = %#v", status)
	}
}

func readySnapshot(unit, mode string, uid uint32, pid int, start uint64, epoch string, generation uint64) visionapi.UnitSnapshot {
	return visionapi.UnitSnapshot{
		Name: unit, Mode: mode, UID: uid, State: "STARTED", Lifecycle: visionapi.LifecycleReady,
		MainPID: integer(pid), MainPIDStartTime: start, VisionEpoch: epoch, Generation: generation,
	}
}

type manualTimer struct {
	at      time.Duration
	fn      func()
	stopped bool
}

func (t *manualTimer) Stop() bool { was := !t.stopped; t.stopped = true; return was }

type manualClock struct {
	now    time.Duration
	timers []*manualTimer
}

func (c *manualClock) AfterFunc(delay time.Duration, fn func()) timer {
	t := &manualTimer{at: c.now + delay, fn: fn}
	c.timers = append(c.timers, t)
	return t
}

func (c *manualClock) Advance(delay time.Duration) {
	c.now += delay
	for {
		var due *manualTimer
		for _, candidate := range c.timers {
			if !candidate.stopped && candidate.at <= c.now {
				due = candidate
				break
			}
		}
		if due == nil {
			return
		}
		due.stopped = true
		due.fn()
	}
}

type fakeVisionSource struct {
	meta      visionapi.MetaResponse
	units     map[string]visionapi.UnitSnapshot
	unitCalls int
}

func (s *fakeVisionSource) Meta(context.Context) (visionapi.MetaResponse, error) { return s.meta, nil }
func (s *fakeVisionSource) Units(context.Context) ([]visionapi.UnitSnapshot, error) {
	result := make([]visionapi.UnitSnapshot, 0, len(s.units))
	for _, unit := range s.units {
		result = append(result, unit)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
func (s *fakeVisionSource) Unit(_ context.Context, unit string) (visionapi.UnitSnapshot, error) {
	s.unitCalls++
	snapshot, ok := s.units[unit]
	if !ok {
		return visionapi.UnitSnapshot{}, errors.New("unit missing")
	}
	return snapshot, nil
}
func (s *fakeVisionSource) Watch(context.Context) (<-chan visionapi.EventEnvelope, error) {
	return make(chan visionapi.EventEnvelope), nil
}

type schedulerProc struct{ processes map[int]cgrouptrack.Process }

func newSchedulerProc(processes ...cgrouptrack.Process) *schedulerProc {
	p := &schedulerProc{processes: make(map[int]cgrouptrack.Process)}
	for _, process := range processes {
		process.PIDFD = -1
		p.processes[process.PID] = process
	}
	return p
}
func (p *schedulerProc) Inspect(pid int) (cgrouptrack.Process, error) {
	process, ok := p.processes[pid]
	if !ok {
		return cgrouptrack.Process{}, errors.New("missing process")
	}
	return process, nil
}
func (p *schedulerProc) ListPIDs() ([]int, error) {
	pids := make([]int, 0, len(p.processes))
	for pid := range p.processes {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}
func (p *schedulerProc) ReadStat(int) (procinfo.Stat, error)            { panic("unused") }
func (p *schedulerProc) ReadStatus(int) (cgrouptrack.ProcStatus, error) { panic("unused") }
func (p *schedulerProc) ReadCgroup(int) (string, error)                 { panic("unused") }
func (p *schedulerProc) ReadExecutable(int) (string, error)             { panic("unused") }
func (p *schedulerProc) OpenPIDFD(int) (int, error)                     { return -1, nil }
func (p *schedulerProc) PIDNamespace(int) (cgrouptrack.FileIdentity, error) {
	return cgrouptrack.FileIdentity{Device: 1, Inode: 2}, nil
}
func (p *schedulerProc) SelfPIDNamespace() (cgrouptrack.FileIdentity, error) {
	return cgrouptrack.FileIdentity{Device: 1, Inode: 2}, nil
}

type schedulerGroups struct {
	members map[cgrouptrack.UnitKey]map[int]bool
	moves   []int
}

func newSchedulerGroups() *schedulerGroups {
	return &schedulerGroups{members: make(map[cgrouptrack.UnitKey]map[int]bool)}
}
func (g *schedulerGroups) Ensure(key cgrouptrack.UnitKey) error {
	if g.members[key] == nil {
		g.members[key] = make(map[int]bool)
	}
	return nil
}
func (g *schedulerGroups) MovePID(key cgrouptrack.UnitKey, pid int) error {
	_ = g.Ensure(key)
	g.members[key][pid] = true
	g.moves = append(g.moves, pid)
	return nil
}
func (g *schedulerGroups) PIDs(key cgrouptrack.UnitKey) ([]int, error) {
	pids := make([]int, 0, len(g.members[key]))
	for pid := range g.members[key] {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}
func (g *schedulerGroups) RemoveIfEmpty(key cgrouptrack.UnitKey) (bool, error) {
	if len(g.members[key]) != 0 {
		return false, nil
	}
	delete(g.members, key)
	return true, nil
}
func (g *schedulerGroups) Scan() ([]cgrouptrack.GroupSnapshot, error) {
	result := make([]cgrouptrack.GroupSnapshot, 0, len(g.members))
	for key := range g.members {
		pids, _ := g.PIDs(key)
		result = append(result, cgrouptrack.GroupSnapshot{Key: key, PIDs: pids})
	}
	return result, nil
}
func (g *schedulerGroups) Path(key cgrouptrack.UnitKey) string { return "/test/" + key.Unit }

func integer(value int) string { return strconv.Itoa(value) }
