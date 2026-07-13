package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"servicectl/internal/cgrouptrack"
	"servicectl/internal/visionapi"
)

const (
	defaultCgroupRoot  = "/sys/fs/cgroup/servicectl.slice"
	defaultSocketPath  = "/run/servicectl/sys-cgroupd.sock"
	defaultRegistry    = "/run/servicectl/sys-cgroupd/registry.json"
	defaultRuntimeRoot = "/run/user"
)

type config struct {
	cgroupRoot        string
	autoMount         bool
	socketPath        string
	registryPath      string
	runtimeRoot       string
	settleDelay       time.Duration
	reconcileInterval time.Duration
	migrationDeadline time.Duration
	maxRounds         int
}

func parseConfig(args []string) (config, error) {
	fs := flag.NewFlagSet("sys-cgroupd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := config{}
	noAutoMount := false
	fs.StringVar(&cfg.cgroupRoot, "cgroup-root", defaultCgroupRoot, "managed cgroup v2 root")
	fs.StringVar(&cfg.socketPath, "socket", defaultSocketPath, "daemon API socket")
	fs.StringVar(&cfg.registryPath, "registry", defaultRegistry, "runtime registry path")
	fs.StringVar(&cfg.runtimeRoot, "runtime-root", defaultRuntimeRoot, "user runtime directory root")
	fs.BoolVar(&noAutoMount, "no-auto-mount", false, "do not mount cgroup2 when it is unavailable")
	fs.DurationVar(&cfg.settleDelay, "settle-delay", 100*time.Millisecond, "ready-state settle delay")
	fs.DurationVar(&cfg.reconcileInterval, "reconcile-interval", 30*time.Second, "full reconciliation interval")
	fs.DurationVar(&cfg.migrationDeadline, "migration-deadline", 250*time.Millisecond, "per-unit migration deadline")
	fs.IntVar(&cfg.maxRounds, "migration-max-rounds", 8, "maximum migration scan rounds")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	cfg.autoMount = !noAutoMount
	if cfg.settleDelay < 10*time.Millisecond || cfg.settleDelay > 10*time.Second {
		return config{}, errors.New("settle delay must be between 10ms and 10s")
	}
	if cfg.reconcileInterval < time.Second || cfg.reconcileInterval > time.Hour {
		return config{}, errors.New("reconcile interval must be between 1s and 1h")
	}
	if cfg.migrationDeadline < 10*time.Millisecond || cfg.migrationDeadline > 30*time.Second {
		return config{}, errors.New("migration deadline must be between 10ms and 30s")
	}
	if cfg.maxRounds < 1 || cfg.maxRounds > 64 {
		return config{}, errors.New("migration max rounds must be between 1 and 64")
	}
	for name, path := range map[string]string{"cgroup root": cfg.cgroupRoot, "socket": cfg.socketPath, "registry": cfg.registryPath, "runtime root": cfg.runtimeRoot} {
		if !filepath.IsAbs(filepath.Clean(path)) {
			return config{}, fmt.Errorf("%s must be absolute", name)
		}
	}
	return cfg, nil
}

type timer interface{ Stop() bool }
type afterFunc func(time.Duration, func()) timer

type schedulerOptions struct {
	bootID       string
	settleDelay  time.Duration
	afterFunc    afterFunc
	proc         cgrouptrack.ProcFS
	groups       cgrouptrack.CgroupFS
	migrator     cgrouptrack.Migrator
	registryPath string
}

type unitWork struct {
	identity      cgrouptrack.InstanceIdentity
	state         cgrouptrack.TrackingState
	source        VisionSource
	timer         timer
	lastMigration time.Time
	lastError     string
	retryCount    int
}

type scheduler struct {
	mu            sync.Mutex
	bootID        string
	settleDelay   time.Duration
	afterFunc     afterFunc
	proc          cgrouptrack.ProcFS
	groups        cgrouptrack.CgroupFS
	migrator      cgrouptrack.Migrator
	registryPath  string
	units         map[cgrouptrack.UnitKey]*unitWork
	lastReconcile time.Time
	rootError     string
}

func newScheduler(options schedulerOptions) *scheduler {
	delay := options.settleDelay
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	after := options.afterFunc
	if after == nil {
		after = func(delay time.Duration, fn func()) timer { return time.AfterFunc(delay, fn) }
	}
	migrator := options.migrator
	if migrator.Proc == nil {
		migrator.Proc = options.proc
	}
	if migrator.Groups == nil {
		migrator.Groups = options.groups
	}
	return &scheduler{
		bootID: options.bootID, settleDelay: delay, afterFunc: after,
		proc: options.proc, groups: options.groups, migrator: migrator,
		registryPath: options.registryPath, units: make(map[cgrouptrack.UnitKey]*unitWork),
	}
}

func (s *scheduler) currentGroups() cgrouptrack.CgroupFS {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.groups
}

func (s *scheduler) currentMigrator() cgrouptrack.Migrator {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.migrator
}

func (s *scheduler) ReplaceGroups(groups cgrouptrack.CgroupFS) {
	if groups == nil {
		return
	}
	s.mu.Lock()
	s.groups = groups
	s.migrator.Groups = groups
	s.rootError = ""
	s.mu.Unlock()
}

func (s *scheduler) LoadRegistry(registry cgrouptrack.Registry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range registry.Units {
		if record.Identity.BootID != s.bootID || record.Identity.Validate() != nil {
			continue
		}
		work := &unitWork{
			identity: record.Identity, state: record.State,
			lastError: record.LastError, retryCount: record.RetryCount,
		}
		if record.LastMigration != "" {
			work.lastMigration, _ = time.Parse(time.RFC3339Nano, record.LastMigration)
		}
		s.units[record.Identity.UnitKey] = work
	}
}

func keyFromSnapshot(snapshot visionapi.UnitSnapshot) (cgrouptrack.UnitKey, error) {
	mode := cgrouptrack.Mode(strings.ToLower(strings.TrimSpace(snapshot.Mode)))
	uid := snapshot.UID
	if mode == cgrouptrack.ModeSystem {
		uid = 0
	}
	key := cgrouptrack.UnitKey{Mode: mode, UID: uid, Unit: canonicalUnit(snapshot.Name)}
	return key, key.Validate()
}

func (s *scheduler) identityFromSnapshot(snapshot visionapi.UnitSnapshot) (cgrouptrack.InstanceIdentity, error) {
	key, err := keyFromSnapshot(snapshot)
	if err != nil {
		return cgrouptrack.InstanceIdentity{}, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(snapshot.MainPID))
	if err != nil {
		return cgrouptrack.InstanceIdentity{}, errors.New("snapshot has invalid MainPID")
	}
	identity := cgrouptrack.InstanceIdentity{
		UnitKey: key, BootID: s.bootID, MainPID: pid,
		MainPIDStartTime: snapshot.MainPIDStartTime,
		VisionEpoch:      snapshot.VisionEpoch, Generation: snapshot.Generation,
	}
	return identity, identity.Validate()
}

func (s *scheduler) Ready(source VisionSource, snapshot visionapi.UnitSnapshot) {
	if snapshot.Lifecycle != visionapi.LifecycleReady {
		return
	}
	identity, err := s.identityFromSnapshot(snapshot)
	if err != nil {
		return
	}
	s.mu.Lock()
	if previous := s.units[identity.UnitKey]; previous != nil {
		if previous.identity == identity {
			hadSource := previous.source != nil
			previous.source = source
			if hadSource && previous.state != cgrouptrack.StateEventSourceOffline && previous.state != cgrouptrack.StateDegraded && previous.state != cgrouptrack.StatePartial {
				s.mu.Unlock()
				return
			}
			if previous.timer != nil {
				previous.timer.Stop()
			}
		}
		if previous.timer != nil {
			previous.timer.Stop()
		}
	}
	work := &unitWork{identity: identity, state: cgrouptrack.StatePending, source: source}
	s.units[identity.UnitKey] = work
	work.timer = s.afterFunc(s.settleDelay, func() { s.migrateCurrent(identity) })
	s.mu.Unlock()
}

func (s *scheduler) migrateCurrent(identity cgrouptrack.InstanceIdentity) {
	s.mu.Lock()
	work := s.units[identity.UnitKey]
	if work == nil || work.identity != identity || work.state != cgrouptrack.StatePending {
		s.mu.Unlock()
		return
	}
	source := work.source
	s.mu.Unlock()
	if source == nil {
		s.updateMigration(identity, cgrouptrack.MigrationResult{State: cgrouptrack.StateDegraded, Err: errors.New("vision source is unavailable")})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	snapshot, err := source.Unit(ctx, identity.Unit)
	cancel()
	if err != nil {
		s.updateMigration(identity, cgrouptrack.MigrationResult{State: cgrouptrack.StateDegraded, Err: err})
		return
	}
	fresh, err := s.identityFromSnapshot(snapshot)
	if err != nil || snapshot.Lifecycle != visionapi.LifecycleReady || fresh != identity {
		if err == nil {
			err = errors.New("service identity changed before migration")
		}
		s.updateMigration(identity, cgrouptrack.MigrationResult{State: cgrouptrack.StateDegraded, Err: err})
		return
	}
	result := s.currentMigrator().Migrate(context.Background(), identity)
	s.updateMigration(identity, result)
}

func (s *scheduler) updateMigration(identity cgrouptrack.InstanceIdentity, result cgrouptrack.MigrationResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	work := s.units[identity.UnitKey]
	if work == nil || work.identity != identity {
		return
	}
	work.state = result.State
	work.lastMigration = time.Now().UTC()
	if result.Err != nil {
		work.lastError = result.Err.Error()
		work.retryCount++
	} else {
		work.lastError = ""
		work.retryCount = 0
	}
}

func (s *scheduler) Stopped(key cgrouptrack.UnitKey, epoch string, generation uint64) {
	s.mu.Lock()
	work := s.units[key]
	if work == nil {
		s.mu.Unlock()
		return
	}
	if work.timer != nil {
		work.timer.Stop()
		work.timer = nil
	}
	s.mu.Unlock()
	groups := s.currentGroups()
	pids, err := groups.PIDs(key)
	state := cgrouptrack.StateStopped
	errorText := ""
	removeRecord := false
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ENOENT) {
		state = cgrouptrack.StateDegraded
		errorText = err.Error()
	} else if len(pids) != 0 {
		state = cgrouptrack.StateOrphanedPopulated
	} else {
		_, _ = groups.RemoveIfEmpty(key)
		removeRecord = true
	}
	s.mu.Lock()
	if current := s.units[key]; current == work {
		if removeRecord {
			delete(s.units, key)
		} else {
			current.state = state
			current.lastError = errorText
		}
	}
	s.mu.Unlock()
}

func (s *scheduler) ReconcileSource(ctx context.Context, source VisionSource) error {
	meta, err := source.Meta(ctx)
	if err != nil {
		return err
	}
	snapshots, err := source.Units(ctx)
	if err != nil {
		return err
	}
	seen := make(map[cgrouptrack.UnitKey]bool)
	for _, snapshot := range snapshots {
		if snapshot.VisionEpoch == "" {
			snapshot.VisionEpoch = meta.VisionEpoch
		}
		if snapshot.Mode == "" {
			snapshot.Mode = meta.Mode
		}
		if snapshot.UID == 0 {
			snapshot.UID = meta.UID
		}
		key, keyErr := keyFromSnapshot(snapshot)
		if keyErr != nil {
			continue
		}
		if err := s.migrateLegacyGroup(key); err != nil {
			return err
		}
		seen[key] = true
		if snapshot.Lifecycle == visionapi.LifecycleReady {
			s.Ready(source, snapshot)
		} else {
			s.Stopped(key, snapshot.VisionEpoch, snapshot.Generation)
		}
	}
	s.mu.Lock()
	keys := make([]cgrouptrack.UnitKey, 0)
	mode := cgrouptrack.Mode(meta.Mode)
	uid := meta.UID
	if mode == cgrouptrack.ModeSystem {
		uid = 0
	}
	for key := range s.units {
		if key.Mode == mode && key.UID == uid && !seen[key] {
			keys = append(keys, key)
		}
	}
	s.mu.Unlock()
	for _, key := range keys {
		s.Missing(key)
	}
	return s.ReconcileGroups(ctx)
}

func (s *scheduler) Missing(key cgrouptrack.UnitKey) {
	s.mu.Lock()
	work := s.units[key]
	if work == nil {
		s.mu.Unlock()
		return
	}
	if work.timer != nil {
		work.timer.Stop()
		work.timer = nil
	}
	s.mu.Unlock()
	groups := s.currentGroups()
	pids, err := groups.PIDs(key)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ENOENT) {
		s.mu.Lock()
		if current := s.units[key]; current == work {
			current.state = cgrouptrack.StateDegraded
			current.lastError = err.Error()
		}
		s.mu.Unlock()
		return
	}
	if len(pids) != 0 {
		s.mu.Lock()
		if current := s.units[key]; current == work {
			current.state = cgrouptrack.StateOrphanedPopulated
			current.lastError = ""
		}
		s.mu.Unlock()
		return
	}
	_, _ = groups.RemoveIfEmpty(key)
	s.mu.Lock()
	if s.units[key] == work {
		delete(s.units, key)
	}
	s.mu.Unlock()
}

func (s *scheduler) ReconcileGroups(context.Context) error {
	backend := s.currentGroups()
	s.mu.Lock()
	known := make([]cgrouptrack.UnitKey, 0, len(s.units))
	for key := range s.units {
		known = append(known, key)
	}
	s.mu.Unlock()
	sort.Slice(known, func(i, j int) bool {
		if known[i].Mode != known[j].Mode {
			return known[i].Mode < known[j].Mode
		}
		if known[i].UID != known[j].UID {
			return known[i].UID < known[j].UID
		}
		return known[i].Unit < known[j].Unit
	})
	for _, key := range known {
		if err := s.migrateLegacyGroup(key); err != nil {
			return err
		}
	}
	groups, err := backend.Scan()
	if err != nil {
		return err
	}
	observed := make(map[cgrouptrack.UnitKey]bool, len(groups))
	for _, group := range groups {
		observed[group.Key] = true
		s.mu.Lock()
		work := s.units[group.Key]
		s.mu.Unlock()
		if work == nil {
			if len(group.PIDs) == 0 {
				_, _ = backend.RemoveIfEmpty(group.Key)
				continue
			}
			s.mu.Lock()
			s.units[group.Key] = &unitWork{identity: cgrouptrack.InstanceIdentity{UnitKey: group.Key, BootID: s.bootID}, state: cgrouptrack.StateUnknownUnit}
			s.mu.Unlock()
			continue
		}
		if work.state == cgrouptrack.StateStopped && len(group.PIDs) != 0 {
			s.mu.Lock()
			work.state = cgrouptrack.StateOrphanedPopulated
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	for key, work := range s.units {
		if observed[key] || work.source == nil || work.identity.Validate() != nil || work.state != cgrouptrack.StateTracked {
			continue
		}
		if work.timer != nil {
			work.timer.Stop()
		}
		identity := work.identity
		work.state = cgrouptrack.StatePending
		work.timer = s.afterFunc(s.settleDelay, func() { s.migrateCurrent(identity) })
	}
	s.lastReconcile = time.Now().UTC()
	s.mu.Unlock()
	return nil
}

func (s *scheduler) migrateLegacyGroup(key cgrouptrack.UnitKey) error {
	migrator, ok := s.currentGroups().(interface {
		MigrateLegacy(cgrouptrack.UnitKey) error
	})
	if !ok {
		return nil
	}
	if err := migrator.MigrateLegacy(key); err != nil {
		return fmt.Errorf("migrate legacy cgroup for %s: %w", key.Unit, err)
	}
	return nil
}

func (s *scheduler) MarkSourceOffline(source VisionSource, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, work := range s.units {
		if work.source == source && work.state != cgrouptrack.StateStopped && work.state != cgrouptrack.StateOrphanedPopulated {
			work.state = cgrouptrack.StateEventSourceOffline
			work.lastError = err.Error()
		}
	}
}

func (s *scheduler) Status(_ context.Context, scope cgrouptrack.Scope) (cgrouptrack.DaemonStatus, error) {
	root := s.groupsRoot()
	s.mu.Lock()
	defer s.mu.Unlock()
	status := cgrouptrack.DaemonStatus{Healthy: s.rootError == "", CgroupRoot: root, Pending: 0, Abnormal: 0}
	if !s.lastReconcile.IsZero() {
		status.LastReconcile = s.lastReconcile.Format(time.RFC3339Nano)
	}
	for key, work := range s.units {
		if !scope.Global && (key.Mode != scope.Mode || key.UID != scope.UID) {
			continue
		}
		if work.state == cgrouptrack.StatePending {
			status.Pending++
		}
		if work.state != cgrouptrack.StateTracked && work.state != cgrouptrack.StateStopped && work.state != cgrouptrack.StatePending {
			status.Abnormal++
		}
	}
	return status, nil
}

func (s *scheduler) ListUnits(_ context.Context, scope cgrouptrack.Scope) ([]cgrouptrack.UnitStatus, error) {
	s.mu.Lock()
	keys := make([]cgrouptrack.UnitKey, 0, len(s.units))
	for key := range s.units {
		if scope.Global || (key.Mode == scope.Mode && key.UID == scope.UID) {
			keys = append(keys, key)
		}
	}
	s.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Mode != keys[j].Mode {
			return keys[i].Mode < keys[j].Mode
		}
		if keys[i].UID != keys[j].UID {
			return keys[i].UID < keys[j].UID
		}
		return keys[i].Unit < keys[j].Unit
	})
	result := make([]cgrouptrack.UnitStatus, 0, len(keys))
	for _, key := range keys {
		status, err := s.statusForKey(key)
		if err == nil {
			result = append(result, status)
		}
	}
	return result, nil
}

func (s *scheduler) GetUnit(_ context.Context, scope cgrouptrack.Scope, unit string) (cgrouptrack.UnitStatus, error) {
	key := cgrouptrack.UnitKey{Mode: scope.Mode, UID: scope.UID, Unit: canonicalUnit(unit)}
	if err := key.Validate(); err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	return s.statusForKey(key)
}

func (s *scheduler) statusForKey(key cgrouptrack.UnitKey) (cgrouptrack.UnitStatus, error) {
	s.mu.Lock()
	work := s.units[key]
	if work == nil {
		s.mu.Unlock()
		return cgrouptrack.UnitStatus{}, errors.New("unit is not tracked")
	}
	identity, state, lastError, lastMigration := work.identity, work.state, work.lastError, work.lastMigration
	s.mu.Unlock()
	groups := s.currentGroups()
	pids, err := groups.PIDs(key)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ENOENT) {
		return cgrouptrack.UnitStatus{}, err
	}
	status := cgrouptrack.UnitStatus{Identity: identity, State: state, Path: groups.Path(key), MemberCount: len(pids), LastError: lastError}
	if !lastMigration.IsZero() {
		status.LastMigration = lastMigration.Format(time.RFC3339Nano)
	}
	return status, nil
}

func (s *scheduler) ListPIDs(_ context.Context, scope cgrouptrack.Scope, unit string) ([]cgrouptrack.ProcessStatus, error) {
	key := cgrouptrack.UnitKey{Mode: scope.Mode, UID: scope.UID, Unit: canonicalUnit(unit)}
	if err := key.Validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	work := s.units[key]
	s.mu.Unlock()
	if work == nil {
		return nil, errors.New("unit is not tracked")
	}
	groups := s.currentGroups()
	pids, err := groups.PIDs(key)
	if err != nil {
		return nil, err
	}
	result := make([]cgrouptrack.ProcessStatus, 0, len(pids))
	for _, pid := range pids {
		process, inspectErr := s.proc.Inspect(pid)
		if inspectErr != nil {
			continue
		}
		if process.PIDFD >= 0 {
			_ = unix.Close(process.PIDFD)
		}
		result = append(result, cgrouptrack.ProcessStatus{PID: pid, StartTime: process.StartTime, UID: process.UID, Comm: process.Comm, MainPID: pid == work.identity.MainPID})
	}
	return result, nil
}

func (s *scheduler) Attach(ctx context.Context, scope cgrouptrack.Scope, unit string, pid int) (cgrouptrack.UnitStatus, error) {
	key := cgrouptrack.UnitKey{Mode: scope.Mode, UID: scope.UID, Unit: canonicalUnit(unit)}
	if err := key.Validate(); err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	s.mu.Lock()
	work := s.units[key]
	s.mu.Unlock()
	if work == nil || work.source == nil {
		return cgrouptrack.UnitStatus{}, errors.New("target unit is unavailable")
	}
	snapshot, err := work.source.Unit(ctx, key.Unit)
	if err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	identity, err := s.identityFromSnapshot(snapshot)
	if err != nil || snapshot.Lifecycle != visionapi.LifecycleReady || identity != work.identity {
		return cgrouptrack.UnitStatus{}, errors.New("target unit changed or is not ready")
	}
	before, err := s.proc.Inspect(pid)
	if err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	if before.PIDFD >= 0 {
		defer unix.Close(before.PIDFD)
	}
	if key.Mode == cgrouptrack.ModeUser && before.UID != key.UID {
		return cgrouptrack.UnitStatus{}, errors.New("process UID does not match target user")
	}
	groups := s.currentGroups()
	if err := groups.Ensure(key); err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	if err := groups.MovePID(key, pid); err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	after, err := s.proc.Inspect(pid)
	if err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	if after.PIDFD >= 0 {
		_ = unix.Close(after.PIDFD)
	}
	if after.StartTime != before.StartTime || after.UID != before.UID {
		return cgrouptrack.UnitStatus{}, cgrouptrack.ErrProcessIdentityChanged
	}
	members, err := groups.PIDs(key)
	if err != nil || !containsPID(members, pid) {
		return cgrouptrack.UnitStatus{}, cgrouptrack.ErrMembershipNotObserved
	}
	fresh, err := work.source.Unit(ctx, key.Unit)
	if err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	freshIdentity, err := s.identityFromSnapshot(fresh)
	if err != nil || freshIdentity != identity {
		return cgrouptrack.UnitStatus{}, errors.New("target unit changed during attach")
	}
	return s.statusForKey(key)
}

func (s *scheduler) groupsRoot() string {
	if rooter, ok := s.currentGroups().(interface{ Root() string }); ok {
		return rooter.Root()
	}
	return ""
}

func (s *scheduler) saveRegistry() error {
	if s.registryPath == "" {
		return nil
	}
	s.mu.Lock()
	records := make([]cgrouptrack.UnitRecord, 0, len(s.units))
	for _, work := range s.units {
		record := cgrouptrack.UnitRecord{Identity: work.identity, State: work.state, RetryCount: work.retryCount, LastError: work.lastError}
		if !work.lastMigration.IsZero() {
			record.LastMigration = work.lastMigration.Format(time.RFC3339Nano)
		}
		if record.Identity.Validate() == nil {
			records = append(records, record)
		}
	}
	s.mu.Unlock()
	return cgrouptrack.WriteRegistry(s.registryPath, cgrouptrack.Registry{Version: 1, Units: records})
}

type VisionSource interface {
	Meta(context.Context) (visionapi.MetaResponse, error)
	Units(context.Context) ([]visionapi.UnitSnapshot, error)
	Unit(context.Context, string) (visionapi.UnitSnapshot, error)
	Watch(context.Context) (<-chan visionapi.EventEnvelope, error)
}

type unixVisionSource struct {
	path        string
	trustedMode string
	trustedUID  uint32
}

func (s *unixVisionSource) request(ctx context.Context, path string, target any) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", s.path)
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("sysvision returned %s", response.Status)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(target)
}

func (s *unixVisionSource) Meta(ctx context.Context) (visionapi.MetaResponse, error) {
	var meta visionapi.MetaResponse
	err := s.request(ctx, "/v1/meta", &meta)
	if err == nil {
		meta.Mode = s.trustedMode
		meta.UID = s.trustedUID
	}
	return meta, err
}
func (s *unixVisionSource) Units(ctx context.Context) ([]visionapi.UnitSnapshot, error) {
	var response visionapi.UnitsResponse
	err := s.request(ctx, "/v1/query/units", &response)
	for i := range response.Units {
		response.Units[i].Mode = s.trustedMode
		response.Units[i].UID = s.trustedUID
	}
	return response.Units, err
}
func (s *unixVisionSource) Unit(ctx context.Context, unit string) (visionapi.UnitSnapshot, error) {
	var snapshot visionapi.UnitSnapshot
	err := s.request(ctx, "/v1/query/unit/"+url.PathEscape(visionQueryUnitName(unit)), &snapshot)
	snapshot.Mode = s.trustedMode
	snapshot.UID = s.trustedUID
	return snapshot, err
}

func visionQueryUnitName(unit string) string {
	return strings.TrimSuffix(unit, ".service")
}
func (s *unixVisionSource) Watch(ctx context.Context) (<-chan visionapi.EventEnvelope, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", s.path)
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/watch", nil)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("sysvision watch returned %s", response.Status)
	}
	ch := make(chan visionapi.EventEnvelope, 64)
	go func() {
		defer close(ch)
		defer response.Body.Close()
		defer transport.CloseIdleConnections()
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			var event visionapi.EventEnvelope
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				return
			}
			event.Mode = s.trustedMode
			event.UID = s.trustedUID
			select {
			case ch <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func runSource(ctx context.Context, scheduler *scheduler, source VisionSource, logger *log.Logger) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := scheduler.ReconcileSource(ctx, source); err != nil {
			scheduler.MarkSourceOffline(source, err)
			logger.Printf("source reconciliation failed: %v", err)
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		}
		events, err := source.Watch(ctx)
		if err != nil {
			scheduler.MarkSourceOffline(source, err)
			if !sleepContext(ctx, backoff) {
				return
			}
			continue
		}
		if err := scheduler.ReconcileSource(ctx, source); err != nil {
			scheduler.MarkSourceOffline(source, err)
			if !sleepContext(ctx, backoff) {
				return
			}
			continue
		}
		backoff = time.Second
		for event := range events {
			if event.Kind != visionapi.KindUnitReady && event.Kind != visionapi.KindUnitMainPIDChanged && event.Kind != visionapi.KindUnitStopped {
				continue
			}
			queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			snapshot, queryErr := source.Unit(queryCtx, canonicalUnit(event.Unit))
			cancel()
			if queryErr != nil && event.Kind == visionapi.KindUnitStopped {
				key := cgrouptrack.UnitKey{Mode: cgrouptrack.Mode(event.Mode), UID: event.UID, Unit: canonicalUnit(event.Unit)}
				if key.Mode == cgrouptrack.ModeSystem {
					key.UID = 0
				}
				if key.Validate() == nil {
					scheduler.Stopped(key, event.VisionEpoch, event.Generation)
				}
				continue
			}
			if queryErr != nil {
				continue
			}
			if snapshot.Lifecycle == visionapi.LifecycleReady {
				scheduler.Ready(source, snapshot)
			} else if key, keyErr := keyFromSnapshot(snapshot); keyErr == nil {
				scheduler.Stopped(key, snapshot.VisionEpoch, snapshot.Generation)
			}
		}
		scheduler.MarkSourceOffline(source, errors.New("event stream closed"))
	}
}

func reconcileVisionSources(ctx context.Context, scheduler *scheduler, sources []VisionSource, logger *log.Logger) {
	for _, source := range sources {
		if source == nil {
			continue
		}
		reconcileCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := scheduler.ReconcileSource(reconcileCtx, source)
		cancel()
		if err == nil {
			continue
		}
		scheduler.MarkSourceOffline(source, err)
		if logger != nil {
			logger.Printf("periodic source reconciliation failed: %v", err)
		}
	}
}

type runningVisionSource struct {
	source VisionSource
	stop   context.CancelFunc
}

func discoverUserSources(runtimeRoot string) map[uint32]*unixVisionSource {
	result := make(map[uint32]*unixVisionSource)
	entries, err := os.ReadDir(runtimeRoot)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		uidValue, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil || strconv.FormatUint(uidValue, 10) != entry.Name() {
			continue
		}
		uid := uint32(uidValue)
		directory := filepath.Join(runtimeRoot, entry.Name())
		info, err := os.Stat(directory)
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uid {
			continue
		}
		path := filepath.Join(directory, "servicectl", visionapi.SysvisionDirName, visionapi.SystemSysvisionSockName)
		socket, err := os.Lstat(path)
		if err != nil || socket.Mode()&os.ModeSocket == 0 {
			continue
		}
		socketStat, ok := socket.Sys().(*syscall.Stat_t)
		if !ok || socketStat.Uid != uid {
			continue
		}
		result[uid] = &unixVisionSource{path: path, trustedMode: visionapi.ModeUser, trustedUID: uid}
	}
	return result
}

func unifiedCgroupMount(mountinfo, selfCgroup string) (string, error) {
	unified := false
	for _, line := range strings.Split(selfCgroup, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "0::") {
			unified = true
			break
		}
	}
	if !unified {
		return "", errors.New("process has no unified cgroup v2 entry")
	}
	for _, line := range strings.Split(mountinfo, "\n") {
		parts := strings.Split(line, " - ")
		if len(parts) != 2 {
			continue
		}
		right := strings.Fields(parts[1])
		left := strings.Fields(parts[0])
		if len(right) >= 1 && right[0] == "cgroup2" && len(left) >= 5 {
			return unescapeMountPath(left[4]), nil
		}
	}
	return "", errors.New("cgroup v2 mount was not found")
}

func managedHierarchyPath(mountPoint, filesystemRoot string) (string, error) {
	mountPoint = filepath.Clean(mountPoint)
	filesystemRoot = filepath.Clean(filesystemRoot)
	relative, err := filepath.Rel(mountPoint, filesystemRoot)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("managed root is outside the cgroup v2 mount")
	}
	if relative == "." {
		return "/", nil
	}
	return "/" + filepath.ToSlash(relative), nil
}

func managedHierarchyPathFromMountInfo(mountinfo, filesystemRoot string) (string, error) {
	filesystemRoot = filepath.Clean(filesystemRoot)
	bestPoint := ""
	bestRoot := ""
	for _, line := range strings.Split(mountinfo, "\n") {
		parts := strings.Split(line, " - ")
		if len(parts) != 2 {
			continue
		}
		right := strings.Fields(parts[1])
		left := strings.Fields(parts[0])
		if len(right) < 1 || right[0] != "cgroup2" || len(left) < 5 {
			continue
		}
		mountRoot := filepath.Clean(unescapeMountPath(left[3]))
		mountPoint := filepath.Clean(unescapeMountPath(left[4]))
		relative, err := filepath.Rel(mountPoint, filesystemRoot)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if len(mountPoint) > len(bestPoint) {
			bestPoint = mountPoint
			bestRoot = mountRoot
		}
	}
	if bestPoint == "" {
		return "", errors.New("managed root is outside every cgroup v2 mount")
	}
	relative, err := filepath.Rel(bestPoint, filesystemRoot)
	if err != nil {
		return "", err
	}
	path := filepath.Join(bestRoot, relative)
	if !filepath.IsAbs(path) {
		path = "/" + path
	}
	return filepath.Clean(path), nil
}

func prepareCgroupRoot(path string) (string, error) {
	return prepareCgroupRootWith(linuxCgroupMountSystem{}, path, true)
}

func main() {
	logger := log.New(os.Stdout, "sys-cgroupd: ", log.LstdFlags)
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		logger.Fatal(err)
	}
	if os.Geteuid() != 0 {
		logger.Fatal("sys-cgroupd must run as root")
	}
	bootIDBytes, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		logger.Fatal(err)
	}
	proc := cgrouptrack.NewLinuxProcFS("/proc")
	mountSystem := linuxCgroupMountSystem{}
	root, rootErr := prepareCgroupRootWith(mountSystem, cfg.cgroupRoot, cfg.autoMount)
	managedPath := "/"
	mountinfo, mountErr := os.ReadFile("/proc/self/mountinfo")
	managedRoot := root
	if managedRoot == "" {
		managedRoot = cfg.cgroupRoot
	}
	if mountErr == nil {
		managedPath, mountErr = managedHierarchyPathFromMountInfo(string(mountinfo), managedRoot)
	}
	if mountErr != nil {
		rootErr = errors.Join(rootErr, fmt.Errorf("map managed cgroup hierarchy: %w", mountErr))
	}
	var groups cgrouptrack.CgroupFS
	if rootErr == nil {
		groups, rootErr = cgrouptrack.OpenCgroupFS(root)
	}
	if rootErr != nil {
		logger.Printf("cgroup root is degraded: %v", rootErr)
		groups = &unavailableGroups{root: cfg.cgroupRoot, err: rootErr}
	}
	scheduler := newScheduler(schedulerOptions{
		bootID: strings.TrimSpace(string(bootIDBytes)), settleDelay: cfg.settleDelay, proc: proc, groups: groups,
		migrator: cgrouptrack.Migrator{Proc: proc, Groups: groups, MaxRounds: cfg.maxRounds, Deadline: cfg.migrationDeadline}, registryPath: cfg.registryPath,
	})
	if rootErr != nil {
		scheduler.rootError = rootErr.Error()
	}
	registry, registryErr := cgrouptrack.ReadOrQuarantine(cfg.registryPath, time.Now())
	if registryErr != nil {
		logger.Printf("registry was ignored: %v", registryErr)
	}
	scheduler.LoadRegistry(registry)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	server := cgrouptrack.NewServer(cgrouptrack.ServerOptions{Path: cfg.socketPath, Mode: 0o666, Service: scheduler, Proc: proc, ManagedCgroupPath: managedPath})
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ctx) }()
	systemSource := &unixVisionSource{path: visionapi.SysvisionSocketPathForMode(visionapi.ModeSystem), trustedMode: visionapi.ModeSystem}
	go runSource(ctx, scheduler, systemSource, logger)
	startedUsers := make(map[uint32]runningVisionSource)
	reconcile := time.NewTicker(cfg.reconcileInterval)
	defer reconcile.Stop()
	discover := time.NewTicker(2 * time.Second)
	defer discover.Stop()
	for {
		select {
		case <-ctx.Done():
			for _, running := range startedUsers {
				running.stop()
			}
			_ = scheduler.saveRegistry()
			<-errCh
			return
		case err := <-errCh:
			if err != nil {
				logger.Fatal(err)
			}
			return
		case <-hup:
			tryRecoverGroups(cfg, mountSystem, scheduler, server, logger)
			_ = scheduler.ReconcileGroups(ctx)
		case <-reconcile.C:
			tryRecoverGroups(cfg, mountSystem, scheduler, server, logger)
			sources := make([]VisionSource, 0, len(startedUsers)+1)
			sources = append(sources, systemSource)
			for _, running := range startedUsers {
				sources = append(sources, running.source)
			}
			reconcileVisionSources(ctx, scheduler, sources, logger)
			_ = scheduler.ReconcileGroups(ctx)
			if err := scheduler.saveRegistry(); err != nil {
				logger.Printf("save registry after reconciliation: %v", err)
			}
		case <-discover.C:
			found := discoverUserSources(cfg.runtimeRoot)
			for uid, source := range found {
				if _, ok := startedUsers[uid]; !ok {
					sourceCtx, stop := context.WithCancel(ctx)
					startedUsers[uid] = runningVisionSource{source: source, stop: stop}
					go runSource(sourceCtx, scheduler, source, logger)
				}
			}
			for uid, running := range startedUsers {
				if _, ok := found[uid]; !ok {
					running.stop()
					delete(startedUsers, uid)
				}
			}
		}
	}
}

func tryRecoverGroups(cfg config, mountSystem cgroupMountSystem, scheduler *scheduler, server *cgrouptrack.Server, logger *log.Logger) {
	scheduler.mu.Lock()
	degraded := scheduler.rootError != ""
	scheduler.mu.Unlock()
	if !degraded {
		return
	}
	root, err := prepareCgroupRootWith(mountSystem, cfg.cgroupRoot, cfg.autoMount)
	if err != nil {
		return
	}
	groups, err := cgrouptrack.OpenCgroupFS(root)
	if err != nil {
		return
	}
	mountinfo, err := os.ReadFile(procSelfMountInfo)
	if err != nil {
		groups.Close()
		return
	}
	managedPath, err := managedHierarchyPathFromMountInfo(string(mountinfo), root)
	if err != nil {
		groups.Close()
		return
	}
	scheduler.ReplaceGroups(groups)
	server.SetManagedCgroupPath(managedPath)
	logger.Printf("cgroup root recovered at %s", root)
}

type unavailableGroups struct {
	root string
	err  error
}

func (g *unavailableGroups) Ensure(cgrouptrack.UnitKey) error                { return g.err }
func (g *unavailableGroups) MovePID(cgrouptrack.UnitKey, int) error          { return g.err }
func (g *unavailableGroups) PIDs(cgrouptrack.UnitKey) ([]int, error)         { return nil, g.err }
func (g *unavailableGroups) RemoveIfEmpty(cgrouptrack.UnitKey) (bool, error) { return false, g.err }
func (g *unavailableGroups) Scan() ([]cgrouptrack.GroupSnapshot, error)      { return nil, g.err }
func (g *unavailableGroups) Path(cgrouptrack.UnitKey) string                 { return "" }
func (g *unavailableGroups) Root() string                                    { return g.root }

func canonicalUnit(unit string) string {
	unit = strings.TrimSpace(unit)
	if unit != "" && !strings.HasSuffix(unit, ".service") {
		unit += ".service"
	}
	return unit
}
func containsPID(pids []int, pid int) bool {
	index := sort.SearchInts(pids, pid)
	return index < len(pids) && pids[index] == pid
}
func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
func unescapeMountPath(value string) string {
	replacer := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	return replacer.Replace(value)
}
