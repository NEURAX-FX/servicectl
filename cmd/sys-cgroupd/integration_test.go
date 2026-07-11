package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"servicectl/internal/cgrouptrack"
	"servicectl/internal/visionapi"
)

const integrationSettleDelay = 100 * time.Millisecond

func TestCgroupV2Integration(t *testing.T) {
	root := integrationCgroupRoot(t)
	linuxGroups, err := cgrouptrack.OpenCgroupFS(root)
	if err != nil {
		t.Fatalf("test root is not a writable cgroup v2 subtree: %v", err)
	}
	audit := newCgroupAudit(linuxGroups)
	proc := cgrouptrack.NewLinuxProcFS("/proc")
	fixtures := make([]*fixtureCommand, 0, 6)
	keys := make([]cgrouptrack.UnitKey, 0, 4)
	t.Cleanup(func() {
		for _, fixture := range fixtures {
			fixture.Stop()
		}
		cleanupIntegrationGroups(root, audit, keys)
		_ = linuxGroups.Close()
	})

	t.Run("lifecycle migration restart and cleanup", func(t *testing.T) {
		fixture := startFixture(t, 0, 0, true)
		fixtures = append(fixtures, fixture)
		mainProcess := waitForProcess(t, proc, fixture.PID())
		childPID := waitForChild(t, proc, fixture.PID())
		unit := "integration-system-" + strconv.Itoa(fixture.PID()) + ".service"
		key := cgrouptrack.UnitKey{Mode: cgrouptrack.ModeSystem, Unit: unit}
		keys = append(keys, key)
		source := newIntegrationVisionSource(visionapi.ModeSystem, 0, "epoch-system")
		snapshot := integrationSnapshot(key, mainProcess, visionapi.LifecycleUnknown, "epoch-system", 1)
		source.Set(snapshot)
		scheduler := newIntegrationScheduler(proc, audit, t.TempDir())

		if err := scheduler.ReconcileSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
		time.Sleep(integrationSettleDelay + 25*time.Millisecond)
		if moves := audit.Moves(key); len(moves) != 0 {
			t.Fatalf("notify service migrated before ready: %#v", moves)
		}

		snapshot.Lifecycle = visionapi.LifecycleReady
		snapshot.Generation = 2
		source.Set(snapshot)
		readyAt := time.Now()
		if err := scheduler.ReconcileSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
		waitForTracked(t, scheduler, key)
		moves := audit.Moves(key)
		if len(moves) < 2 || moves[0].PID != fixture.PID() {
			t.Fatalf("MainPID was not migrated first: %#v", moves)
		}
		if moves[0].At.Sub(readyAt) < integrationSettleDelay-10*time.Millisecond {
			t.Fatalf("migration started before settle delay: %s", moves[0].At.Sub(readyAt))
		}
		members := waitForMembers(t, audit, key, fixture.PID(), childPID)
		if len(members) < 2 {
			t.Fatalf("members = %#v", members)
		}

		if err := scheduler.saveRegistry(); err != nil {
			t.Fatal(err)
		}
		registry, err := cgrouptrack.ReadRegistry(scheduler.registryPath)
		if err != nil {
			t.Fatal(err)
		}
		restarted := newIntegrationScheduler(proc, audit, t.TempDir())
		restarted.LoadRegistry(registry)
		if err := restarted.ReconcileSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
		waitForTracked(t, restarted, key)
		waitForMembers(t, audit, key, fixture.PID(), childPID)

		restarted.Stopped(key, snapshot.VisionEpoch, snapshot.Generation+1)
		status := waitForState(t, restarted, key, cgrouptrack.StateOrphanedPopulated)
		if status.MemberCount == 0 {
			t.Fatalf("populated stopped group was not preserved: %#v", status)
		}
		fixture.Stop()
		waitForNoMembers(t, audit, key)
		restarted.Stopped(key, snapshot.VisionEpoch, snapshot.Generation+1)
		waitForState(t, restarted, key, cgrouptrack.StateStopped)
		waitForMissingPath(t, audit.Path(key))
	})

	t.Run("epoch replacement cancels stale timer", func(t *testing.T) {
		fixture := startFixture(t, 0, 0, false)
		fixtures = append(fixtures, fixture)
		process := waitForProcess(t, proc, fixture.PID())
		unit := "integration-epoch-" + strconv.Itoa(fixture.PID()) + ".service"
		key := cgrouptrack.UnitKey{Mode: cgrouptrack.ModeSystem, Unit: unit}
		keys = append(keys, key)
		source := newIntegrationVisionSource(visionapi.ModeSystem, 0, "epoch-old")
		oldSnapshot := integrationSnapshot(key, process, visionapi.LifecycleReady, "epoch-old", 1)
		newSnapshot := integrationSnapshot(key, process, visionapi.LifecycleReady, "epoch-new", 1)
		source.Set(oldSnapshot)
		scheduler := newIntegrationScheduler(proc, audit, t.TempDir())
		scheduler.Ready(source, oldSnapshot)
		time.Sleep(20 * time.Millisecond)
		source.Set(newSnapshot)
		scheduler.Ready(source, newSnapshot)
		waitForTracked(t, scheduler, key)
		moves := audit.Moves(key)
		if len(moves) != 1 || moves[0].PID != fixture.PID() {
			t.Fatalf("stale epoch timer migrated a process: %#v", moves)
		}
		status := waitForState(t, scheduler, key, cgrouptrack.StateTracked)
		if status.Identity.VisionEpoch != "epoch-new" {
			t.Fatalf("tracked stale epoch: %#v", status.Identity)
		}
		fixture.Stop()
		waitForNoMembers(t, audit, key)
		scheduler.Stopped(key, "epoch-new", 2)
		waitForMissingPath(t, audit.Path(key))
	})

	t.Run("authenticated same UID attach", func(t *testing.T) {
		primary, alternate, ok := integrationUserCredentials(t)
		if !ok {
			t.Skip("two non-root passwd entries are required")
		}
		mainFixture := startFixture(t, primary.uid, primary.gid, false)
		extraFixture := startFixture(t, primary.uid, primary.gid, false)
		crossFixture := startFixture(t, alternate.uid, alternate.gid, false)
		fixtures = append(fixtures, mainFixture, extraFixture, crossFixture)
		mainProcess := waitForProcess(t, proc, mainFixture.PID())
		waitForProcess(t, proc, extraFixture.PID())
		waitForProcess(t, proc, crossFixture.PID())
		unit := "integration-user-" + strconv.Itoa(mainFixture.PID()) + ".service"
		key := cgrouptrack.UnitKey{Mode: cgrouptrack.ModeUser, UID: primary.uid, Unit: unit}
		keys = append(keys, key)
		source := newIntegrationVisionSource(visionapi.ModeUser, primary.uid, "epoch-user")
		snapshot := integrationSnapshot(key, mainProcess, visionapi.LifecycleReady, "epoch-user", 1)
		source.Set(snapshot)
		scheduler := newIntegrationScheduler(proc, audit, t.TempDir())
		scheduler.Ready(source, snapshot)
		waitForTracked(t, scheduler, key)

		client, stopServer := startIntegrationServer(t, scheduler, proc, root, primary.uid, primary.gid)
		defer stopServer()
		request := cgrouptrack.Request{
			Operation: cgrouptrack.OpAttach, Mode: cgrouptrack.ModeUser,
			UID: primary.uid, Unit: unit, PID: extraFixture.PID(),
		}
		response, err := client.Do(context.Background(), request)
		if err != nil || response.Unit == nil {
			t.Fatalf("same-UID attach failed: response=%#v err=%v", response, err)
		}
		waitForMembers(t, audit, key, mainFixture.PID(), extraFixture.PID())

		request.PID = crossFixture.PID()
		if _, err := client.Do(context.Background(), request); err == nil {
			t.Fatal("cross-UID attach was accepted")
		} else {
			var apiErr *cgrouptrack.APIError
			if !errors.As(err, &apiErr) || apiErr.Code != "access-denied" {
				t.Fatalf("cross-UID attach error = %#v", err)
			}
		}

		mainFixture.Stop()
		extraFixture.Stop()
		crossFixture.Stop()
		waitForNoMembers(t, audit, key)
		scheduler.Stopped(key, "epoch-user", 2)
		waitForMissingPath(t, audit.Path(key))
	})

	audit.AssertMembershipOnly(t)
}

func integrationCgroupRoot(t *testing.T) string {
	t.Helper()
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
	return root
}

func newIntegrationScheduler(proc cgrouptrack.ProcFS, groups cgrouptrack.CgroupFS, registryDir string) *scheduler {
	registryPath := filepath.Join(registryDir, "registry.json")
	return newScheduler(schedulerOptions{
		bootID: "integration-boot", settleDelay: integrationSettleDelay,
		proc: proc, groups: groups, registryPath: registryPath,
		migrator: cgrouptrack.Migrator{Proc: proc, Groups: groups, MaxRounds: 8, Deadline: 2 * time.Second},
	})
}

func integrationSnapshot(key cgrouptrack.UnitKey, process cgrouptrack.Process, lifecycle, epoch string, generation uint64) visionapi.UnitSnapshot {
	return visionapi.UnitSnapshot{
		Name: key.Unit, Mode: string(key.Mode), UID: key.UID, State: "STARTED",
		MainPID: strconv.Itoa(process.PID), MainPIDStartTime: process.StartTime,
		VisionEpoch: epoch, Generation: generation, Lifecycle: lifecycle,
	}
}

type integrationVisionSource struct {
	mu        sync.Mutex
	meta      visionapi.MetaResponse
	snapshots map[string]visionapi.UnitSnapshot
}

func newIntegrationVisionSource(mode string, uid uint32, epoch string) *integrationVisionSource {
	return &integrationVisionSource{
		meta:      visionapi.MetaResponse{VisionEpoch: epoch, Mode: mode, UID: uid, SnapshotReady: true},
		snapshots: make(map[string]visionapi.UnitSnapshot),
	}
}

func (s *integrationVisionSource) Set(snapshot visionapi.UnitSnapshot) {
	s.mu.Lock()
	s.meta.VisionEpoch = snapshot.VisionEpoch
	s.snapshots[snapshot.Name] = snapshot
	s.mu.Unlock()
}

func (s *integrationVisionSource) Meta(context.Context) (visionapi.MetaResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta, nil
}

func (s *integrationVisionSource) Units(context.Context) ([]visionapi.UnitSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]visionapi.UnitSnapshot, 0, len(s.snapshots))
	for _, snapshot := range s.snapshots {
		result = append(result, snapshot)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s *integrationVisionSource) Unit(_ context.Context, unit string) (visionapi.UnitSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.snapshots[unit]
	if !ok {
		return visionapi.UnitSnapshot{}, os.ErrNotExist
	}
	return snapshot, nil
}

func (s *integrationVisionSource) Watch(context.Context) (<-chan visionapi.EventEnvelope, error) {
	return make(chan visionapi.EventEnvelope), nil
}

type auditOperation struct {
	Kind string
	Key  cgrouptrack.UnitKey
	PID  int
	At   time.Time
}

type cgroupAudit struct {
	backend cgrouptrack.CgroupFS
	mu      sync.Mutex
	ops     []auditOperation
}

func newCgroupAudit(backend cgrouptrack.CgroupFS) *cgroupAudit {
	return &cgroupAudit{backend: backend}
}

func (a *cgroupAudit) record(kind string, key cgrouptrack.UnitKey, pid int) {
	a.mu.Lock()
	a.ops = append(a.ops, auditOperation{Kind: kind, Key: key, PID: pid, At: time.Now()})
	a.mu.Unlock()
}

func (a *cgroupAudit) Ensure(key cgrouptrack.UnitKey) error {
	a.record("ensure", key, 0)
	return a.backend.Ensure(key)
}

func (a *cgroupAudit) MovePID(key cgrouptrack.UnitKey, pid int) error {
	a.record("write:cgroup.procs", key, pid)
	return a.backend.MovePID(key, pid)
}

func (a *cgroupAudit) PIDs(key cgrouptrack.UnitKey) ([]int, error) {
	a.record("read:cgroup.procs", key, 0)
	return a.backend.PIDs(key)
}

func (a *cgroupAudit) RemoveIfEmpty(key cgrouptrack.UnitKey) (bool, error) {
	a.record("remove-empty", key, 0)
	return a.backend.RemoveIfEmpty(key)
}

func (a *cgroupAudit) Scan() ([]cgrouptrack.GroupSnapshot, error) {
	a.record("scan", cgrouptrack.UnitKey{}, 0)
	return a.backend.Scan()
}

func (a *cgroupAudit) Path(key cgrouptrack.UnitKey) string { return a.backend.Path(key) }

func (a *cgroupAudit) Root() string {
	if rooter, ok := a.backend.(interface{ Root() string }); ok {
		return rooter.Root()
	}
	return ""
}

func (a *cgroupAudit) Moves(key cgrouptrack.UnitKey) []auditOperation {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]auditOperation, 0)
	for _, operation := range a.ops {
		if operation.Kind == "write:cgroup.procs" && operation.Key == key {
			result = append(result, operation)
		}
	}
	return result
}

func (a *cgroupAudit) AssertMembershipOnly(t *testing.T) {
	t.Helper()
	allowed := map[string]bool{
		"ensure": true, "write:cgroup.procs": true, "read:cgroup.procs": true,
		"remove-empty": true, "scan": true,
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, operation := range a.ops {
		if !allowed[operation.Kind] || strings.Contains(operation.Kind, "kill") || strings.Contains(operation.Kind, "freeze") || strings.Contains(operation.Kind, "controller") {
			t.Fatalf("forbidden cgroup operation: %#v", operation)
		}
	}
}

type fixtureCommand struct {
	command *exec.Cmd
	group   bool
	done    chan struct{}
	once    sync.Once
}

func startFixture(t *testing.T, uid, gid uint32, processGroup bool) *fixtureCommand {
	t.Helper()
	var command *exec.Cmd
	if processGroup {
		command = exec.Command("/bin/sh", "-c", "sleep 30 & wait")
	} else {
		command = exec.Command("/bin/sleep", "30")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: processGroup}
	if uid != 0 {
		command.SysProcAttr.Credential = &syscall.Credential{Uid: uid, Gid: gid, NoSetGroups: true}
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	fixture := &fixtureCommand{command: command, group: processGroup, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		close(fixture.done)
	}()
	return fixture
}

func (f *fixtureCommand) PID() int { return f.command.Process.Pid }

func (f *fixtureCommand) Stop() {
	if f == nil || f.command == nil || f.command.Process == nil {
		return
	}
	f.once.Do(func() {
		pid := f.PID()
		if f.group {
			_ = syscall.Kill(-pid, syscall.SIGTERM)
		} else {
			_ = f.command.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-f.done:
		case <-time.After(2 * time.Second):
			if f.group {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			} else {
				_ = f.command.Process.Signal(syscall.SIGKILL)
			}
			<-f.done
		}
	})
}

type testCredential struct {
	uid uint32
	gid uint32
}

func integrationUserCredentials(t *testing.T) (testCredential, testCredential, bool) {
	t.Helper()
	file, err := os.Open("/etc/passwd")
	if err != nil {
		t.Logf("cannot read passwd entries: %v", err)
		return testCredential{}, testCredential{}, false
	}
	defer file.Close()
	credentials := make([]testCredential, 0, 2)
	seen := make(map[uint32]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) < 4 {
			continue
		}
		uid64, uidErr := strconv.ParseUint(fields[2], 10, 32)
		gid64, gidErr := strconv.ParseUint(fields[3], 10, 32)
		uid := uint32(uid64)
		if uidErr != nil || gidErr != nil || uid == 0 || seen[uid] {
			continue
		}
		seen[uid] = true
		credentials = append(credentials, testCredential{uid: uid, gid: uint32(gid64)})
		if len(credentials) == 2 {
			return credentials[0], credentials[1], true
		}
	}
	return testCredential{}, testCredential{}, false
}

func startIntegrationServer(t *testing.T, service cgrouptrack.Service, proc cgrouptrack.ProcFS, root string, uid, gid uint32) (*cgrouptrack.Client, func()) {
	t.Helper()
	namespace := cgrouptrack.FileIdentity{Device: 1, Inode: 2}
	serverProc := &fixedNamespaceProc{ProcFS: proc, namespace: namespace}
	path := filepath.Join(t.TempDir(), "sys-cgroupd.sock")
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	managedRoot := root
	if strings.HasPrefix(managedRoot, "/proc/1/root/") {
		managedRoot = strings.TrimPrefix(managedRoot, "/proc/1/root")
	}
	managedPath, err := managedHierarchyPathFromMountInfo(string(mountinfo), managedRoot)
	if err != nil && strings.HasPrefix(root, "/proc/1/root/sys/fs/cgroup/") {
		managedPath, err = managedHierarchyPath("/sys/fs/cgroup", managedRoot)
	}
	if err != nil {
		t.Fatal(err)
	}
	server := cgrouptrack.NewServer(cgrouptrack.ServerOptions{
		Path: path, Service: service, Proc: serverProc, ManagedCgroupPath: managedPath,
		RequestTimeout: 2 * time.Second,
		ResolvePeer: func(*net.UnixConn, cgrouptrack.ProcFS) (cgrouptrack.Peer, error) {
			return cgrouptrack.Peer{PID: os.Getpid(), UID: uid, GID: gid, PIDNamespace: namespace}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ctx) }()
	waitForSocket(t, path)
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Errorf("server stop: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Error("server did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return cgrouptrack.NewClient(path), stop
}

type fixedNamespaceProc struct {
	cgrouptrack.ProcFS
	namespace cgrouptrack.FileIdentity
}

func (p *fixedNamespaceProc) PIDNamespace(int) (cgrouptrack.FileIdentity, error) {
	return p.namespace, nil
}

func (p *fixedNamespaceProc) SelfPIDNamespace() (cgrouptrack.FileIdentity, error) {
	return p.namespace, nil
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	waitForCondition(t, "server socket", func() bool {
		info, err := os.Lstat(path)
		return err == nil && info.Mode()&os.ModeSocket != 0
	})
}

func waitForTracked(t *testing.T, scheduler *scheduler, key cgrouptrack.UnitKey) cgrouptrack.UnitStatus {
	t.Helper()
	return waitForState(t, scheduler, key, cgrouptrack.StateTracked)
}

func waitForState(t *testing.T, scheduler *scheduler, key cgrouptrack.UnitKey, state cgrouptrack.TrackingState) cgrouptrack.UnitStatus {
	t.Helper()
	var result cgrouptrack.UnitStatus
	waitForCondition(t, "unit state "+string(state), func() bool {
		status, err := scheduler.GetUnit(context.Background(), cgrouptrack.Scope{Mode: key.Mode, UID: key.UID}, key.Unit)
		if err != nil || status.State != state {
			return false
		}
		result = status
		return true
	})
	return result
}

func waitForMembers(t *testing.T, groups cgrouptrack.CgroupFS, key cgrouptrack.UnitKey, wanted ...int) []int {
	t.Helper()
	var result []int
	waitForCondition(t, "cgroup membership", func() bool {
		pids, err := groups.PIDs(key)
		if err != nil {
			return false
		}
		for _, pid := range wanted {
			if !containsPID(pids, pid) {
				return false
			}
		}
		result = pids
		return true
	})
	return result
}

func waitForNoMembers(t *testing.T, groups cgrouptrack.CgroupFS, key cgrouptrack.UnitKey) {
	t.Helper()
	waitForCondition(t, "empty cgroup", func() bool {
		pids, err := groups.PIDs(key)
		return err == nil && len(pids) == 0
	})
}

func waitForMissingPath(t *testing.T, path string) {
	t.Helper()
	waitForCondition(t, "removed cgroup", func() bool {
		_, err := os.Stat(path)
		return os.IsNotExist(err)
	})
}

func waitForCondition(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func waitForProcess(t *testing.T, proc cgrouptrack.ProcFS, pid int) cgrouptrack.Process {
	t.Helper()
	var result cgrouptrack.Process
	waitForCondition(t, "inspectable process", func() bool {
		process, err := proc.Inspect(pid)
		if err != nil {
			return false
		}
		if process.PIDFD >= 0 {
			_ = syscall.Close(process.PIDFD)
			process.PIDFD = -1
		}
		result = process
		return true
	})
	return result
}

func waitForChild(t *testing.T, proc cgrouptrack.ProcFS, parent int) int {
	t.Helper()
	child := 0
	waitForCondition(t, "fixture child", func() bool {
		pids, err := proc.ListPIDs()
		if err != nil {
			return false
		}
		for _, pid := range pids {
			process, inspectErr := proc.Inspect(pid)
			if inspectErr != nil {
				continue
			}
			if process.PIDFD >= 0 {
				_ = syscall.Close(process.PIDFD)
			}
			if process.PPID == parent {
				child = pid
				return true
			}
		}
		return false
	})
	return child
}

func cleanupIntegrationGroups(root string, groups cgrouptrack.CgroupFS, keys []cgrouptrack.UnitKey) {
	for _, key := range keys {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			pids, err := groups.PIDs(key)
			if (err == nil && len(pids) == 0) || os.IsNotExist(err) || errors.Is(err, syscall.ENOENT) {
				_, _ = groups.RemoveIfEmpty(key)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	_ = os.Remove(filepath.Join(root, "system"))
	userRoot := filepath.Join(root, "user")
	entries, _ := os.ReadDir(userRoot)
	for _, entry := range entries {
		if entry.IsDir() {
			_ = os.Remove(filepath.Join(userRoot, entry.Name()))
		}
	}
	_ = os.Remove(userRoot)
}
