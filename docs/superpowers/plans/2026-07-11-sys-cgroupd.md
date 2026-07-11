# sys-cgroupd Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a root `sys-cgroupd` daemon that asynchronously places ready servicectl system and user service process trees into dedicated cgroup v2 leaves and supports authenticated explicit PID attachment without assuming service lifecycle or resource-policy authority.

**Architecture:** Extend `sysvisiond` into a single-plane lifecycle normalizer with epoch/generation-aware snapshots and watch events. Implement cgroup and `/proc` mechanics in a focused `internal/cgrouptrack` package, then expose them through a bounded Unix-socket protocol consumed by the root daemon and `servicectl cgroup` CLI. Use kernel cgroup membership as truth, a runtime registry only as a recovery hint, and periodic full reconciliation to recover from event loss.

**Tech Stack:** Go 1.22, `golang.org/x/sys/unix`, cgroup v2 filesystem, Linux `/proc`, Unix sockets with `SO_PEERCRED`, NDJSON over the existing sysvision HTTP-over-Unix API, Dinit, RPM/tmpfiles packaging, Bash integration tests.

---

## File Map

### Lifecycle Plane

- Modify `internal/visionapi/types.go`: lifecycle metadata, UID, start time, epoch, generation, and event constants.
- Modify `internal/visionapi/types_test.go`: canonical lifecycle identity and runtime path tests.
- Modify `cmd/sysvisiond/main.go`: one-plane mode, lifecycle cache, polling normalization, metadata, snapshot, and watch behavior.
- Create `cmd/sysvisiond/main_test.go`: direct/notify/lazy lifecycle state-machine and stream-overflow tests.
- Create `internal/procinfo/stat.go`: shared Linux proc stat parser and start-time reader.
- Create `internal/procinfo/stat_test.go`: command-name and malformed-stat parser tests.
- Modify `servicectl_api.go`: run only the process-selected system or user query/event plane.
- Create `servicectl_api_test.go`: single-plane API path and mode tests.
- Modify `s6_backend.go`: run user `sysvisiond` with `--mode=user`.

### cgroup Tracking Core

- Create `internal/cgrouptrack/types.go`: mode, unit key, instance identity, states, status structures, and validation.
- Create `internal/cgrouptrack/types_test.go`: unit canonicalization and state tests.
- Create `internal/cgrouptrack/proc.go`: safe `/proc` parsing, pidfd identity, descendant graph, PID namespace checks.
- Create `internal/cgrouptrack/proc_test.go`: parser, reuse, graph, zombie, kernel-thread, and namespace tests.
- Create `internal/cgrouptrack/cgroupfs.go`: validated rooted cgroupfs operations and path codec.
- Create `internal/cgrouptrack/cgroupfs_test.go`: traversal, file allowlist, membership, and cleanup tests using a fake tree.
- Create `internal/cgrouptrack/migrate.go`: bounded MainPID-first converging migration.
- Create `internal/cgrouptrack/migrate_test.go`: stable identity, ordering, convergence, and partial-state tests.
- Create `internal/cgrouptrack/registry.go`: atomic runtime recovery hints and corruption quarantine.
- Create `internal/cgrouptrack/registry_test.go`: atomicity, mode, size, and corruption tests.
- Create `internal/cgrouptrack/protocol.go`: bounded framed request/response types.
- Create `internal/cgrouptrack/protocol_test.go`: framing and validation tests.
- Create `internal/cgrouptrack/client.go`: CLI client.
- Create `internal/cgrouptrack/server.go`: peer authentication, request limits, and scoped dispatch.
- Create `internal/cgrouptrack/server_test.go`: root/user visibility and attach authorization tests.

### Daemon and CLI

- Create `cmd/sys-cgroupd/main.go`: configuration, source discovery, subscriptions, scheduling, reconciliation, and shutdown.
- Create `cmd/sys-cgroupd/main_test.go`: generation cancellation, source reconnect, retries, and cleanup tests.
- Create `cgroup_cli.go`: `servicectl cgroup` command implementation.
- Create `cgroup_cli_test.go`: parsing, human output, redaction, and exit-status tests.
- Modify `main.go`: help and command dispatch.

### Deployment and Acceptance

- Create `packaging/sys-cgroupd`: Dinit service definition.
- Modify `packaging/servicectl.tmpfiles`: runtime directory creation.
- Modify `packaging/servicectl-stack.spec`: build, install, test, and package `sys-cgroupd`.
- Modify `scripts/install.sh`: local install of the daemon and service.
- Modify `scripts/test-install-paths.sh`: install-path assertions.
- Create `scripts/test-cgroupd-integration.sh`: real delegated cgroup v2 acceptance test.
- Modify `README.md`: command and operational documentation.
- Modify `packaging/SRPM-DESIGN.md`: package payload and non-activation behavior.

## Task 1: Lifecycle Types and Identity

**Files:**
- Modify: `internal/visionapi/types.go`
- Modify: `internal/visionapi/types_test.go`

- [ ] **Step 1: Write failing lifecycle type tests**

Add tests that require UID-aware snapshots, epoch/generation fields, lifecycle kinds, and explicit runtime directories:

```go
func TestLifecycleIdentityIsComplete(t *testing.T) {
	snapshot := UnitSnapshot{
		Name: "demo.service", Mode: ModeUser, UID: 1000,
		MainPID: "42", MainPIDStartTime: 1234,
		VisionEpoch: "epoch-a", Generation: 7,
		Lifecycle: LifecycleReady,
	}
	if snapshot.UID != 1000 || snapshot.MainPIDStartTime != 1234 || snapshot.Generation != 7 {
		t.Fatalf("incomplete snapshot: %#v", snapshot)
	}
	if KindUnitReady == KindUnitStopped || KindUnitMainPIDChanged == KindUnitReady {
		t.Fatal("lifecycle event kinds are not distinct")
	}
}

func TestRuntimeDirForUID(t *testing.T) {
	if got := RuntimeDirForUID(1000); got != "/run/user/1000/servicectl" {
		t.Fatalf("RuntimeDirForUID = %q", got)
	}
	if got := SysvisionSocketPathForUID(1000); got != "/run/user/1000/servicectl/sysvision/sysvisiond.sock" {
		t.Fatalf("SysvisionSocketPathForUID = %q", got)
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run: `go test ./internal/visionapi -run 'TestLifecycleIdentityIsComplete|TestRuntimeDirForUID'`

Expected: FAIL because the lifecycle fields, constants, and UID path helper do not exist.

- [ ] **Step 3: Add lifecycle constants and fields**

Add these exact public shapes while preserving existing JSON fields:

```go
const (
	KindUnitReady          = "unit.ready"
	KindUnitMainPIDChanged = "unit.main-pid-changed"
	KindUnitStopped        = "unit.stopped"

	LifecycleUnknown = "unknown"
	LifecycleReady   = "ready"
	LifecycleStopped = "stopped"
)

type UnitSnapshot struct {
	// Existing fields remain unchanged.
	UID              uint32 `json:"uid"`
	MainPIDStartTime uint64 `json:"main_pid_starttime"`
	VisionEpoch      string `json:"vision_epoch"`
	Generation       uint64 `json:"generation"`
	Lifecycle        string `json:"lifecycle"`
}

type MetaResponse struct {
	VisionEpoch              string `json:"vision_epoch"`
	Mode                     string `json:"mode"`
	UID                      uint32 `json:"uid"`
	ServicectlEventsConnected bool   `json:"servicectl_events_connected"`
	ServicectlEventsError     string `json:"servicectl_events_error,omitempty"`
	SnapshotReady             bool   `json:"snapshot_ready"`
	SnapshotError             string `json:"snapshot_error,omitempty"`
}

func RuntimeDirForUID(uid uint32) string {
	return filepath.Join("/run/user", strconv.FormatUint(uint64(uid), 10), "servicectl")
}

func SysvisionSocketPathForUID(uid uint32) string {
	return filepath.Join(RuntimeDirForUID(uid), SysvisionDirName, SystemSysvisionSockName)
}
```

Extend `EventEnvelope` with `UID uint32`, `VisionEpoch string`, and `Generation uint64`. Add `UID *uint32` to `WatchFilter`; `Matches` compares it only when non-nil so system UID 0 remains distinguishable from no UID filter.

- [ ] **Step 4: Run lifecycle type tests**

Run: `go test ./internal/visionapi`

Expected: PASS.

- [ ] **Step 5: Commit the lifecycle contract**

```bash
git add internal/visionapi/types.go internal/visionapi/types_test.go
git commit -m "feat: define service lifecycle identity"
```

## Task 2: sysvisiond Single-Plane Runtime

**Files:**
- Modify: `cmd/sysvisiond/main.go`
- Create: `cmd/sysvisiond/main_test.go`
- Modify: `servicectl_api.go`
- Create: `servicectl_api_test.go`
- Modify: `s6_backend.go`
- Modify: `mode_isolation_test.go`

- [ ] **Step 1: Write failing mode and metadata tests**

Create tests around a parseable configuration instead of invoking `main`:

```go
func TestParseConfigRequiresOnePlane(t *testing.T) {
	cfg, err := parseConfig([]string{"--mode=user"}, 1000)
	if err != nil || cfg.mode != visionapi.ModeUser || cfg.uid != 1000 {
		t.Fatalf("config = %#v, %v", cfg, err)
	}
	if _, err := parseConfig([]string{"--mode=invalid"}, 0); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestMetaIncludesStableEpoch(t *testing.T) {
	d := newDaemon(config{mode: visionapi.ModeSystem, uid: 0}, "epoch-test")
	first := d.meta()
	second := d.meta()
	if first.VisionEpoch != "epoch-test" || first != second {
		t.Fatalf("unstable meta: %#v %#v", first, second)
	}
}
```

Update the s6 source assertion to require:

```text
sysvisiond --mode=user
servicectl --user serve-api
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./cmd/sysvisiond . -run 'TestParseConfigRequiresOnePlane|TestMetaIncludesStableEpoch|Test.*Sysvisiond.*Mode'`

Expected: FAIL because `parseConfig`, `newDaemon`, and the `--mode` run line do not exist.

- [ ] **Step 3: Refactor sysvisiond to one plane**

Introduce:

```go
type config struct {
	mode         string
	uid          uint32
	pollInterval time.Duration
}

func parseConfig(args []string, euid int) (config, error)
func newDaemon(cfg config, epoch string) *daemon
func randomEpoch() (string, error)
```

Rules:

- `--mode=system` requires EUID 0 and uses UID 0.
- `--mode=user` uses the process EUID and only that user's runtime tree.
- one process opens one servicectl event stream, one notify ingress socket, and one API socket.
- default mode is `system` for compatibility with the packaged system service.
- `randomEpoch` reads 16 bytes from `crypto/rand` and hex-encodes them.

Change `ensureSysvisiondSource` to generate one explicit plane:

```go
runLine := sysvisiondBinaryPath() + " --mode=system"
if userMode() {
	runLine = sysvisiondBinaryPath() + " --mode=user"
}
```

Change `servicectlAPIServer` to construct only `newServicectlPlaneServer(config.Mode, hub)`. Change `ensureServicectlAPISource` to emit `servicectl --user serve-api` in user mode and `servicectl serve-api` in system mode.

Make root-only property service inclusion explicit:

```go
entries = appendUniqueLinePreserveOrder(entries, s6ServicectlAPIServiceName())
entries = appendUniqueLinePreserveOrder(entries, s6SysvisiondServiceName())
if !userMode() {
	entries = appendUniqueLinePreserveOrder(entries, s6SysPropertydServiceName())
}
```

Call `ensureSysPropertydSource` and add it to sysvisiond dependencies only in system mode. User `sysvisiond` lifecycle queries depend only on the user servicectl API.

- [ ] **Step 4: Run mode tests and the full existing suite**

Run: `go test ./cmd/sysvisiond ./...`

Expected: PASS with no system/user path regression.

- [ ] **Step 5: Commit the one-plane runtime**

```bash
git add cmd/sysvisiond/main.go cmd/sysvisiond/main_test.go servicectl_api.go servicectl_api_test.go s6_backend.go mode_isolation_test.go
git commit -m "refactor: isolate sysvisiond event planes"
```

## Task 3: sysvisiond Lifecycle Normalization

**Files:**
- Modify: `cmd/sysvisiond/main.go`
- Modify: `cmd/sysvisiond/main_test.go`
- Create: `internal/procinfo/stat.go`
- Create: `internal/procinfo/stat_test.go`
- Modify: `internal/visionapi/types.go`

- [ ] **Step 1: Write failing lifecycle transition tests**

Use a deterministic state machine with injected snapshots and start-time lookup:

```go
func TestLifecycleNormalizerDistinguishesDirectNotifyAndLazy(t *testing.T) {
	n := newNormalizer("epoch-a", 1000)

	direct := visionapi.UnitSnapshot{Name: "direct.service", ManagedBy: "dinit", State: "STARTED", MainPID: "10"}
	notify := visionapi.UnitSnapshot{Name: "notify.service", ManagedBy: "sys-notifyd", State: "STARTED", Phase: "starting", MainPID: "11"}
	lazy := visionapi.UnitSnapshot{Name: "lazy.service", ManagedBy: "sys-notifyd", State: "STARTED", Phase: "ready", ManagerPID: "12", MainPID: ""}
	events := n.Update([]visionapi.UnitSnapshot{direct, notify, lazy}, map[int]uint64{10: 101, 11: 102})
	assertLifecycleEvent(t, events, visionapi.KindUnitReady, "direct.service", 1)
	if eventFor(events, "notify.service") != nil {
		t.Fatalf("notify emitted lifecycle event too early: %#v", events)
	}
	if eventFor(events, "lazy.service") != nil {
		t.Fatalf("manager-only lazy service emitted lifecycle event: %#v", events)
	}
}

func eventFor(events []visionapi.EventEnvelope, unit string) *visionapi.EventEnvelope {
	for i := range events {
		if events[i].Unit == unit {
			return &events[i]
		}
	}
	return nil
}

func assertLifecycleEvent(t *testing.T, events []visionapi.EventEnvelope, kind string, unit string, generation uint64) {
	t.Helper()
	event := eventFor(events, unit)
	if event == nil || event.Kind != kind || event.Generation != generation {
		t.Fatalf("event for %s = %#v, all=%#v", unit, event, events)
	}
}
```

Add tests for MainPID/start-time change, stopped transition, unchanged snapshot producing no event, generation monotonicity, and epoch inclusion.

- [ ] **Step 2: Verify lifecycle tests fail**

Run: `go test ./cmd/sysvisiond -run 'TestLifecycle'`

Expected: FAIL because the normalizer is absent.

- [ ] **Step 3: Write and verify the shared proc stat parser**

Create the shared parser test first:

```go
func TestParseStatHandlesClosingParen(t *testing.T) {
	stat, err := ParseStat("42 (worker ) name) S 7 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 12345 0")
	if err != nil || stat.PID != 42 || stat.PPID != 7 || stat.StartTime != 12345 {
		t.Fatalf("stat = %#v, %v", stat, err)
	}
}
```

Run: `go test ./internal/procinfo`

Expected: FAIL because the package does not exist.

Implement:

```go
type Stat struct { PID int; Comm string; State byte; PPID int; StartTime uint64 }
func ParseStat(line string) (Stat, error)
func ReadStat(procRoot string, pid int) (Stat, error)
```

Find the first `(` and final `)`, parse PID separately, then parse fields 3, 4, and 22 with bounded input and exact numeric conversion. Add malformed/truncated/oversized tests.

- [ ] **Step 4: Implement the normalizer and snapshot endpoints**

Implement focused types:

```go
type lifecycleInstance struct {
	lifecycle string
	mainPID   int
	startTime uint64
	generation uint64
}

type normalizer struct {
	epoch string
	uid   uint32
	units map[string]lifecycleInstance
}

func (n *normalizer) Update(snapshots []visionapi.UnitSnapshot, startTimes map[int]uint64) []visionapi.EventEnvelope
```

The daemon polls `/v1/units` at a configurable interval, default 500 ms. Existing incoming `unit.command` and `unit.runtime` events trigger an immediate debounced poll. The normalizer reads MainPID start times through `procinfo.ReadStat`, then enriches cached snapshots with UID, epoch, generation, start time, and lifecycle.

Change `/v1/query/units` and `/v1/query/unit/<name>` to return the normalized cache instead of pass-through responses. Return `503` before the first successful snapshot. `/v1/meta` returns `visionapi.MetaResponse` plus upstream connection status.

- [ ] **Step 5: Make slow-subscriber overflow close the stream**

Replace silent event dropping with subscriber removal and channel close:

```go
select {
case sub.ch <- event:
default:
	delete(d.subs, id)
	close(sub.ch)
	d.logger.Printf("closing slow subscriber %d", id)
}
```

Add a test that fills a small injected subscriber queue and verifies closure.

- [ ] **Step 6: Run lifecycle and repository tests**

Run: `go test -race ./cmd/sysvisiond ./internal/procinfo ./internal/visionapi ./...`

Expected: PASS.

- [ ] **Step 7: Commit normalized lifecycle events**

```bash
git add cmd/sysvisiond/main.go cmd/sysvisiond/main_test.go internal/procinfo/stat.go internal/procinfo/stat_test.go internal/visionapi/types.go
git commit -m "feat: publish normalized service lifecycle events"
```

## Task 4: cgroup Domain Types and Path Codec

**Files:**
- Create: `internal/cgrouptrack/types.go`
- Create: `internal/cgrouptrack/types_test.go`

- [ ] **Step 1: Write failing validation and codec tests**

```go
func TestUnitKeyRoundTrip(t *testing.T) {
	key := UnitKey{Mode: ModeUser, UID: 1000, Unit: "dbus-org.freedesktop.locale1.service"}
	encoded, err := key.EncodedUnit()
	if err != nil { t.Fatal(err) }
	decoded, err := DecodeUnit(ModeUser, 1000, encoded)
	if err != nil || decoded != key { t.Fatalf("decoded = %#v, %v", decoded, err) }
}

func TestUnitKeyRejectsTraversalAndNonCanonicalNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../x.service", "a/b.service", `a\\b.service`, "x", "x.service.service"} {
		if err := (UnitKey{Mode: ModeSystem, Unit: name}).Validate(); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/cgrouptrack -run 'TestUnitKey'`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement exact domain types**

Define:

```go
type Mode string
const (ModeSystem Mode = "system"; ModeUser Mode = "user")

type UnitKey struct { Mode Mode; UID uint32; Unit string }
type InstanceIdentity struct {
	UnitKey
	BootID string
	MainPID int
	MainPIDStartTime uint64
	VisionEpoch string
	Generation uint64
}
type TrackingState string
const (
	StatePending TrackingState = "pending"
	StateTracked TrackingState = "tracked"
	StatePartial TrackingState = "partial"
	StateDegraded TrackingState = "degraded"
	StateStopped TrackingState = "stopped"
	StateOrphanedPopulated TrackingState = "orphaned-populated"
	StateUnknownUnit TrackingState = "unknown-unit"
	StateEventSourceOffline TrackingState = "event-source-offline"
)

type DaemonStatus struct {
	Healthy bool `json:"healthy"`
	CgroupRoot string `json:"cgroup_root"`
	LastReconcile string `json:"last_reconcile,omitempty"`
	Pending int `json:"pending"`
	Abnormal int `json:"abnormal"`
}

type UnitStatus struct {
	Identity InstanceIdentity `json:"identity"`
	State TrackingState `json:"state"`
	Path string `json:"path"`
	MemberCount int `json:"member_count"`
	LastMigration string `json:"last_migration,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

type ProcessStatus struct {
	PID int `json:"pid"`
	StartTime uint64 `json:"starttime"`
	UID uint32 `json:"uid"`
	Comm string `json:"comm"`
	MainPID bool `json:"main_pid"`
}
```

Use URL-safe base64 without padding for reversible unit encoding. Decode, require 1..255 bytes, valid UTF-8, exactly one `.service` suffix, no ASCII control character, slash, backslash, `.` or `..` path component, and require re-encoding to equal the original encoded component.

- [ ] **Step 4: Run package tests**

Run: `go test ./internal/cgrouptrack`

Expected: PASS.

- [ ] **Step 5: Commit domain types**

```bash
git add internal/cgrouptrack/types.go internal/cgrouptrack/types_test.go
git commit -m "feat: define cgroup tracking domain model"
```

## Task 5: Safe Process Inspection

**Files:**
- Create: `internal/cgrouptrack/proc.go`
- Create: `internal/cgrouptrack/proc_test.go`

- [ ] **Step 1: Write failing `/proc` parser and graph tests**

```go
func TestDescendantsAreParentFirst(t *testing.T) {
	procs := []Process{{PID: 10, PPID: 1}, {PID: 11, PPID: 10}, {PID: 12, PPID: 11}}
	if got := Descendants(10, procs); !reflect.DeepEqual(got, []int{11, 12}) {
		t.Fatalf("descendants = %#v", got)
	}
}
```

Also test zombie state, kernel thread detection from missing executable, user UID filtering, vanished proc entries, graph cycles, and namespace inode mismatch.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/cgrouptrack -run 'TestDescendants|TestProcessIdentity|TestPIDNamespace'`

Expected: FAIL because process inspection is absent.

- [ ] **Step 3: Implement an injectable process source**

```go
type ProcFS interface {
	ReadStat(pid int) (procinfo.Stat, error)
	ReadStatus(pid int) (ProcStatus, error)
	ReadCgroup(pid int) (string, error)
	ReadExecutable(pid int) (string, error)
	ListPIDs() ([]int, error)
	OpenPIDFD(pid int) (int, error)
	PIDNamespace(pid int) (FileIdentity, error)
}

type FileIdentity struct { Device uint64; Inode uint64 }
type ProcStatus struct { UID uint32 }
type Process struct {
	PID int
	PPID int
	UID uint32
	StartTime uint64
	State byte
	Comm string
	Cgroup string
}
```

Implement `LinuxProcFS` with root `/proc`, bounded reads, numeric-directory filtering, `procinfo.ReadStat`, `unix.PidfdOpen`, and namespace identity from stat device/inode. `Inspect` returns `(pid,starttime,uid,state,comm,cgroup,pidfd)` and rejects zombies and kernel threads.

- [ ] **Step 4: Run process tests and race tests**

Run: `go test -race ./internal/procinfo ./internal/cgrouptrack -run 'TestParseStat|TestDescendants|TestProcessIdentity|TestPIDNamespace'`

Expected: PASS.

- [ ] **Step 5: Commit process inspection**

```bash
git add internal/cgrouptrack/proc.go internal/cgrouptrack/proc_test.go
git commit -m "feat: inspect stable process identities"
```

## Task 6: Rooted cgroup v2 Filesystem

**Files:**
- Create: `internal/cgrouptrack/cgroupfs.go`
- Create: `internal/cgrouptrack/cgroupfs_test.go`

- [ ] **Step 1: Write failing rooted-filesystem tests**

```go
func TestCgroupFSOnlyWritesProcs(t *testing.T) {
	root := newFakeCgroupRoot(t)
	fs, err := openCgroupFS(root, skipMagicCheck)
	if err != nil { t.Fatal(err) }
	key := UnitKey{Mode: ModeSystem, Unit: "demo.service"}
	if err := fs.Ensure(key); err != nil { t.Fatal(err) }
	procs := filepath.Join(root, "system", mustEncoded(t, key), "cgroup.procs")
	if err := os.WriteFile(procs, nil, 0o644); err != nil { t.Fatal(err) }
	if err := fs.MovePID(key, 42); err != nil { t.Fatal(err) }
	content, err := os.ReadFile(procs)
	if err != nil || string(content) != "42\n" { t.Fatalf("content=%q err=%v", content, err) }
}

func TestCgroupFSRejectsSymlinkEscape(t *testing.T) {
	root := newFakeCgroupRoot(t)
	if err := os.Symlink("/tmp", filepath.Join(root, "system")); err != nil { t.Fatal(err) }
	if _, err := openCgroupFS(root, skipMagicCheck); err == nil { t.Fatal("symlink root accepted") }
}
```

Add tests for wrong filesystem magic, non-empty removal, malformed encoded directories, user path construction, and never opening controller/kill/freeze files.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/cgrouptrack -run 'TestCgroupFS'`

Expected: FAIL because `OpenCgroupFS` is absent.

- [ ] **Step 3: Implement root-relative operations**

Define:

```go
type CgroupFS interface {
	Ensure(UnitKey) error
	MovePID(UnitKey, int) error
	PIDs(UnitKey) ([]int, error)
	RemoveIfEmpty(UnitKey) (bool, error)
	Scan() ([]GroupSnapshot, error)
	Path(UnitKey) string
}

type GroupSnapshot struct {
	Key UnitKey
	PIDs []int
}
```

Production `LinuxCgroupFS` opens and pins the configured root, verifies `CGROUP2_SUPER_MAGIC`, and uses `unix.Openat2` with `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS`. The only writable leaf file is the literal `cgroup.procs`. Tests inject the filesystem-magic check and seed ordinary temporary directories with fake `cgroup.procs` files; production code never creates a control file.

- [ ] **Step 4: Run cgroupfs tests**

Run: `go test -race ./internal/cgrouptrack -run 'TestCgroupFS'`

Expected: PASS.

- [ ] **Step 5: Commit cgroupfs operations**

```bash
git add internal/cgrouptrack/cgroupfs.go internal/cgrouptrack/cgroupfs_test.go
git commit -m "feat: add rooted cgroup v2 operations"
```

## Task 7: Bounded Process-Tree Migration

**Files:**
- Create: `internal/cgrouptrack/migrate.go`
- Create: `internal/cgrouptrack/migrate_test.go`

- [ ] **Step 1: Write failing migration tests**

Use fake ProcFS and CgroupFS implementations that record operations:

```go
func TestMigratorMovesMainPIDBeforeDescendants(t *testing.T) {
	proc := fakeProcTree(10, 11, 12)
	groups := &recordingGroups{}
	m := Migrator{Proc: proc, Groups: groups, MaxRounds: 8, Deadline: time.Second}
	result := m.Migrate(context.Background(), systemInstance("demo.service", 10, 100))
	if result.State != StateTracked || !reflect.DeepEqual(groups.moves, []int{10, 11, 12}) {
		t.Fatalf("result=%#v moves=%#v", result, groups.moves)
	}
}

func TestMigratorReturnsPartialAtBound(t *testing.T) {
	proc := continuouslyForkingProc()
	m := Migrator{Proc: proc, Groups: &recordingGroups{}, MaxRounds: 2, Deadline: time.Second}
	if got := m.Migrate(context.Background(), systemInstance("demo.service", 10, 100)); got.State != StatePartial {
		t.Fatalf("state = %s", got.State)
	}
}
```

Add tests for changed MainPID start time, user descendant with another UID, process exit during write, post-write mismatch, and deadline expiry.

Define test helpers in `migrate_test.go`: `systemInstance` returns a complete fixed-boot/epoch identity, `fakeProcTree` returns immutable parent-first Process data, `recordingGroups` appends every `MovePID`, and `continuouslyForkingProc` exposes one new descendant after each `ListPIDs` call. These helpers never read host `/proc` or cgroupfs.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/cgrouptrack -run 'TestMigrator'`

Expected: FAIL because `Migrator` is absent.

- [ ] **Step 3: Implement minimal converging migration**

```go
type Migrator struct {
	Proc      ProcFS
	Groups    CgroupFS
	MaxRounds int
	Deadline  time.Duration
}

type MigrationResult struct {
	State TrackingState
	Moved []int
	Skipped []PIDError
	Err error
}

type PIDError struct {
	PID int
	Error string
}
```

Algorithm:

1. Revalidate MainPID identity.
2. Ensure target leaf.
3. Move MainPID and verify membership.
4. Scan descendants and move them parent-first.
5. Repeat until no eligible new PID appears.
6. Return `partial` when limits expire, `degraded` for cgroup errors, and `tracked` only after an empty convergence round.

- [ ] **Step 4: Run migration tests**

Run: `go test -race ./internal/cgrouptrack -run 'TestMigrator'`

Expected: PASS.

- [ ] **Step 5: Commit migration logic**

```bash
git add internal/cgrouptrack/migrate.go internal/cgrouptrack/migrate_test.go
git commit -m "feat: migrate service process trees into cgroups"
```

## Task 8: Runtime Registry

**Files:**
- Create: `internal/cgrouptrack/registry.go`
- Create: `internal/cgrouptrack/registry_test.go`

- [ ] **Step 1: Write failing registry tests**

```go
func TestRegistryRoundTripAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	r := Registry{Version: 1, Units: []UnitRecord{{Identity: systemInstance("demo.service", 10, 100), State: StateTracked}}}
	if err := WriteRegistry(path, r); err != nil { t.Fatal(err) }
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 { t.Fatalf("mode = %o", info.Mode().Perm()) }
	got, err := ReadRegistry(path)
	if err != nil || !reflect.DeepEqual(got, r) { t.Fatalf("got=%#v err=%v", got, err) }
}

func TestRegistryQuarantinesCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	os.WriteFile(path, []byte("{"), 0o600)
	if _, err := ReadOrQuarantine(path, time.Unix(100, 0)); err == nil { t.Fatal("corruption accepted") }
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 { t.Fatalf("quarantine files = %#v", matches) }
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/cgrouptrack -run 'TestRegistry'`

Expected: FAIL because registry functions do not exist.

- [ ] **Step 3: Implement bounded atomic persistence**

Define the persisted schema:

```go
type Registry struct {
	Version int `json:"version"`
	Units []UnitRecord `json:"units"`
}

type UnitRecord struct {
	Identity InstanceIdentity `json:"identity"`
	State TrackingState `json:"state"`
	LastMigration string `json:"last_migration,omitempty"`
	RetryCount int `json:"retry_count,omitempty"`
	LastError string `json:"last_error,omitempty"`
}
```

Use versioned JSON, a 1 MiB maximum, `O_NOFOLLOW`, owner/mode checks, temporary file mode 0600, file `fsync`, rename, and parent-directory `fsync`. A missing registry returns an empty version-1 value. Corrupt data is renamed only within the same validated directory.

- [ ] **Step 4: Run registry tests**

Run: `go test -race ./internal/cgrouptrack -run 'TestRegistry'`

Expected: PASS.

- [ ] **Step 5: Commit runtime persistence**

```bash
git add internal/cgrouptrack/registry.go internal/cgrouptrack/registry_test.go
git commit -m "feat: persist cgroup recovery hints"
```

## Task 9: Authenticated Daemon Protocol

**Files:**
- Create: `internal/cgrouptrack/protocol.go`
- Create: `internal/cgrouptrack/protocol_test.go`
- Create: `internal/cgrouptrack/client.go`
- Create: `internal/cgrouptrack/server.go`
- Create: `internal/cgrouptrack/server_test.go`

- [ ] **Step 1: Write failing framing and authorization tests**

```go
func TestDecodeRejectsOversizedFrame(t *testing.T) {
	frame := make([]byte, 4)
	binary.BigEndian.PutUint32(frame, MaxRequestBytes+1)
	if _, err := DecodeRequest(bytes.NewReader(frame)); err == nil { t.Fatal("oversized frame accepted") }
}

func TestAuthorizeScopesUserPeer(t *testing.T) {
	peer := Peer{PID: 40, UID: 1000, PIDNamespace: FileIdentity{Device: 1, Inode: 2}}
	request := Request{Operation: OpListUnits, Mode: ModeUser, UID: 1000}
	if err := Authorize(peer, request, FileIdentity{Device: 1, Inode: 2}); err != nil { t.Fatal(err) }
	request.UID = 1001
	if err := Authorize(peer, request, FileIdentity{Device: 1, Inode: 2}); err == nil { t.Fatal("cross-uid request accepted") }
}
```

Add tests for root system access, unprivileged system denial, global status redaction, cross-PID-namespace denial, attach PID ownership, rejection when a user PID currently belongs to the system or another UID's servicectl cgroup, same-user service-to-service reassignment, unknown operation, connection limit, and malformed JSON.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/cgrouptrack -run 'TestDecode|TestAuthorize|TestServer'`

Expected: FAIL because protocol/server types are absent.

- [ ] **Step 3: Implement bounded protocol types**

```go
const MaxRequestBytes = 64 * 1024

type Operation string
const (
	OpStatus Operation = "status"
	OpListUnits Operation = "list-units"
	OpGetUnit Operation = "get-unit"
	OpListPIDs Operation = "list-pids"
	OpAttach Operation = "attach"
)

type Request struct {
	Operation Operation `json:"operation"`
	Mode Mode `json:"mode,omitempty"`
	UID uint32 `json:"uid,omitempty"`
	Unit string `json:"unit,omitempty"`
	PID int `json:"pid,omitempty"`
}

type Response struct {
	OK bool `json:"ok"`
	Error *APIError `json:"error,omitempty"`
	Status *DaemonStatus `json:"status,omitempty"`
	Units []UnitStatus `json:"units,omitempty"`
	Unit *UnitStatus `json:"unit,omitempty"`
	PIDs []ProcessStatus `json:"pids,omitempty"`
}

type APIError struct {
	Code string `json:"code"`
	Message string `json:"message"`
}

type Scope struct { Mode Mode; UID uint32; Global bool }

type Service interface {
	Status(context.Context, Scope) (DaemonStatus, error)
	ListUnits(context.Context, Scope) ([]UnitStatus, error)
	GetUnit(context.Context, Scope, string) (UnitStatus, error)
	ListPIDs(context.Context, Scope, string) ([]ProcessStatus, error)
	Attach(context.Context, Scope, string, int) (UnitStatus, error)
}
```

Frames are a four-byte big-endian length plus one JSON object. Decode with `DisallowUnknownFields` and reject trailing data.

- [ ] **Step 4: Implement server peer checks and client**

`Server` obtains `SO_PEERCRED`, verifies PID namespace identity, enforces 32 global connections, 4 concurrent requests per UID, a token-bucket attach limit, and request deadlines. Dispatch uses an injected `Service` interface. User responses are filtered before encoding; global errors are root-only. Before user attach, reject a current cgroup path under `system/` or `user/<different-uid>/`; permit untracked PIDs and reassignment between units under the authenticated user's own subtree.

`Client` dials the Unix socket with context, writes one frame, reads one frame, and converts structured errors into typed Go errors.

- [ ] **Step 5: Run protocol tests**

Run: `go test -race ./internal/cgrouptrack -run 'TestDecode|TestAuthorize|TestServer|TestClient'`

Expected: PASS.

- [ ] **Step 6: Commit the daemon protocol**

```bash
git add internal/cgrouptrack/protocol.go internal/cgrouptrack/protocol_test.go internal/cgrouptrack/client.go internal/cgrouptrack/server.go internal/cgrouptrack/server_test.go
git commit -m "feat: add authenticated cgroup daemon protocol"
```

## Task 10: sys-cgroupd Scheduler and Reconciliation

**Files:**
- Create: `cmd/sys-cgroupd/main.go`
- Create: `cmd/sys-cgroupd/main_test.go`

- [ ] **Step 1: Write failing configuration and scheduler tests**

```go
func TestConfigBounds(t *testing.T) {
	cfg, err := parseConfig([]string{"--cgroup-root=/tmp/cg", "--settle-delay=100ms", "--migration-max-rounds=8"})
	if err != nil || cfg.settleDelay != 100*time.Millisecond || cfg.maxRounds != 8 { t.Fatalf("cfg=%#v err=%v", cfg, err) }
	if _, err := parseConfig([]string{"--settle-delay=1us"}); err == nil { t.Fatal("unsafe delay accepted") }
}

func TestNewGenerationCancelsPendingMigration(t *testing.T) {
	s := newTestScheduler(t)
	s.Ready(instance("demo.service", 10, 100, "epoch-a", 1))
	s.Ready(instance("demo.service", 11, 200, "epoch-a", 2))
	s.Advance(100 * time.Millisecond)
	if got := s.MigratedPIDs(); !reflect.DeepEqual(got, []int{11}) { t.Fatalf("migrated = %#v", got) }
}

func TestAttachRevalidatesRunningTargetAndPID(t *testing.T) {
	s := newTestScheduler(t)
	s.SetSnapshot(readySnapshot("demo.service", 10, 100, "epoch-a", 1))
	s.SetProcess(processIdentity(42, 500, 1000))
	_, err := s.Attach(context.Background(), cgrouptrack.Scope{Mode: cgrouptrack.ModeUser, UID: 1000}, "demo.service", 42)
	if err != nil { t.Fatal(err) }
	if got := s.GroupForPID(42); got.Unit != "demo.service" { t.Fatalf("group = %#v", got) }
}
```

Add tests for stopped cancellation, epoch replacement, event-source offline state, reconnect snapshot, retry backoff, empty cleanup, populated orphan retention, unknown groups, and graceful shutdown.

Implement the test scheduler with an injected manual clock, fake VisionSource, fake ProcFS, and recording CgroupFS. `instance`, `readySnapshot`, and `processIdentity` return complete fixed boot/epoch values; `Advance` runs due timer callbacks synchronously. No scheduler unit test sleeps or touches host cgroupfs.

- [ ] **Step 2: Verify RED**

Run: `go test ./cmd/sys-cgroupd`

Expected: FAIL because the command does not exist.

- [ ] **Step 3: Implement source and scheduler interfaces**

```go
type VisionSource interface {
	Meta(context.Context) (visionapi.MetaResponse, error)
	Units(context.Context) ([]visionapi.UnitSnapshot, error)
	Unit(context.Context, string) (visionapi.UnitSnapshot, error)
	Watch(context.Context) (<-chan visionapi.EventEnvelope, error)
}

type scheduler struct {
	mu sync.Mutex
	units map[cgrouptrack.UnitKey]*unitWork
	migrator cgrouptrack.Migrator
	groups cgrouptrack.CgroupFS
	registryPath string
}
```

Use event notifications only to trigger `Unit` queries. Every startup/reconnect performs `Meta`, `Units`, starts `Watch`, then performs another `Units` reconciliation. Timers retain the full identity and compare it immediately before migration.

Implement the protocol `Service` methods on the scheduler. `Attach` performs a fresh target query, verifies ready identity, inspects and authorizes the PID, writes it once, re-queries the target generation, and verifies post-write membership. `ListPIDs` reads only `cgroup.procs` for the selected unit and enriches those PIDs through ProcFS; it never scans arbitrary host processes for query output.

- [ ] **Step 4: Implement user-plane discovery**

The root daemon watches `/run/user` with periodic discovery. Accept only numeric UID directories owned by that UID, then require the expected `servicectl/sysvision/sysvisiond.sock` to be a Unix socket owned by that UID. Build a source whose trusted UID comes from discovery, not JSON. Remove only the source connection when the runtime directory disappears; retain group state as offline.

- [ ] **Step 5: Implement daemon setup and signals**

Parse the design flags with these bounds:

```text
settle delay: 10ms..10s
reconcile interval: 1s..1h
migration deadline: 10ms..30s
max rounds: 1..64
```

For the default path only, parse `/proc/self/mountinfo` to locate the `cgroup2` mount and `/proc/self/cgroup` to require a `0::` unified entry. Ignore all nonzero v1 controller entries. Open and verify that mount before creating `servicectl.slice`; a custom `--cgroup-root` must already exist on cgroup2. Then open the managed root, read boot ID, load/quarantine registry, start API mode 0666, connect the system source, discover user sources, and reconcile. Failure to open the root keeps the daemon API available in degraded state and retries with bounded backoff. `SIGHUP` triggers immediate reconciliation. `SIGTERM` stops sources, waits for active writes, saves registry, and exits without cleanup.

- [ ] **Step 6: Run daemon tests**

Run: `go test -race ./cmd/sys-cgroupd ./internal/cgrouptrack`

Expected: PASS.

- [ ] **Step 7: Commit the daemon**

```bash
git add cmd/sys-cgroupd/main.go cmd/sys-cgroupd/main_test.go
git commit -m "feat: add sys-cgroupd reconciliation daemon"
```

## Task 11: servicectl cgroup CLI

**Files:**
- Create: `cgroup_cli.go`
- Create: `cgroup_cli_test.go`
- Modify: `main.go`

- [ ] **Step 1: Write failing CLI tests**

```go
func TestParseCgroupAttach(t *testing.T) {
	request, err := parseCgroupCommand([]string{"attach", "demo.service", "42"}, true, 1000)
	if err != nil { t.Fatal(err) }
	if request.Operation != cgrouptrack.OpAttach || request.Mode != cgrouptrack.ModeUser || request.UID != 1000 || request.PID != 42 {
		t.Fatalf("request = %#v", request)
	}
}

func TestCgroupCommandRejectsExtraArguments(t *testing.T) {
	if _, err := parseCgroupCommand([]string{"status", "extra"}, false, 0); err == nil {
		t.Fatal("extra argument accepted")
	}
}
```

Add table tests for `status`, `list`, `inspect`, `pids`, attach PID validation, user/system mode, socket errors, and redacted human output. Do not add a CLI-only JSON dialect; machine consumers use the framed daemon API.

- [ ] **Step 2: Verify RED**

Run: `go test . -run 'TestCgroup'`

Expected: FAIL because CLI functions are absent.

- [ ] **Step 3: Implement command parsing and output**

Add:

```go
var cgroupdSocketPath = "/run/servicectl/sys-cgroupd.sock"

func cgroupCommand(args []string) int
func parseCgroupCommand(args []string, userMode bool, uid uint32) (cgrouptrack.Request, error)
func printCgroupResponse(io.Writer, cgrouptrack.Request, cgrouptrack.Response) error
```

Use the existing global `--user` mode. Keep command forms exactly as documented. Output only PID, start time, UID, comm, and MainPID marker; never print environment or full argv.

Update help and dispatch before `ensureUserModeReady` so `cgroup status` can report daemon/source failures without trying to initialize supervision first.

- [ ] **Step 4: Run CLI and repository tests**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 5: Commit CLI support**

```bash
git add cgroup_cli.go cgroup_cli_test.go main.go
git commit -m "feat: add servicectl cgroup commands"
```

## Task 12: Packaging and Runtime Wiring

**Files:**
- Create: `packaging/sys-cgroupd`
- Modify: `packaging/servicectl.tmpfiles`
- Modify: `packaging/servicectl-stack.spec`
- Modify: `scripts/install.sh`
- Modify: `scripts/test-install-paths.sh`

- [ ] **Step 1: Write failing install-path assertions**

Extend `scripts/test-install-paths.sh` with:

```bash
grep -Fq 'build_and_install sys-cgroupd "$ROOT/cmd/sys-cgroupd"' "$INSTALL_SCRIPT"
grep -Fq 'command = /usr/local/bin/sys-cgroupd' "$INSTALL_SCRIPT"
grep -Fq -- '--mode=user' "$ROOT/s6_backend.go"
```

Run: `bash scripts/test-install-paths.sh`

Expected: FAIL because the daemon and service are not installed.

- [ ] **Step 2: Add the Dinit service and tmpfiles entry**

Create `packaging/sys-cgroupd`:

```text
# servicectl cgroup v2 process tracker
type = process
command = /usr/bin/sys-cgroupd
restart = true
smooth-recovery = true
log-type = buffer
```

Add:

```text
d /run/servicectl/sys-cgroupd 0700 root root -
```

Do not add any package scriptlet that starts the service or creates a cgroup.

- [ ] **Step 3: Wire local installation**

Build `sys-cgroupd`, install `/etc/dinit.d/sys-cgroupd`, list the binary in output, and preserve the no-auto-start behavior.

- [ ] **Step 4: Wire RPM build and payload**

Set `%global servicectl_version 0.2.0` and add a matching changelog entry so this feature cannot overwrite the published 0.1.0 SRPM/RPM names. Add `go build ... ./cmd/sys-cgroupd`, install the binary and config, include both in `%files`, and add it to the binary existence loop. Do not add controller-related runtime dependencies. The integration-script `%check` line is added only in Task 13 after that script exists.

- [ ] **Step 5: Run path, syntax, and package tests**

Run:

```bash
bash -n scripts/install.sh scripts/test-install-paths.sh
bash scripts/test-install-paths.sh
go test ./...
rpmspec -q --qf '%{VERSION}-%{RELEASE}\n' packaging/servicectl-stack.spec
```

Expected: tests PASS and `rpmspec` reports `0.2.0-1` plus the current distro suffix when defined.

- [ ] **Step 6: Commit deployment wiring**

```bash
git add packaging/sys-cgroupd packaging/servicectl.tmpfiles packaging/servicectl-stack.spec scripts/install.sh scripts/test-install-paths.sh
git commit -m "build: package sys-cgroupd runtime"
```

## Task 13: Real cgroup v2 Integration and Documentation

**Files:**
- Create: `scripts/test-cgroupd-integration.sh`
- Create: `cmd/sys-cgroupd/integration_test.go`
- Modify: `README.md`
- Modify: `packaging/SRPM-DESIGN.md`
- Modify: `packaging/servicectl-stack.spec`

- [ ] **Step 1: Create integration-script self tests**

The script accepts `--self-test`, validates dependencies and argument parsing without touching cgroupfs, and exits 0. Normal mode requires EUID 0 and an explicitly provided writable delegated root:

```bash
usage() {
  printf '%s\n' 'usage: test-cgroupd-integration.sh [--self-test|--cgroup-root PATH]'
}

if [[ ${1:-} == --self-test ]]; then
  command -v go >/dev/null
  bash -n "$0"
  printf '%s\n' 'cgroupd integration self-test passed'
  exit 0
fi
```

Do not auto-select `/sys/fs/cgroup` and do not use `cgroup.kill` during cleanup.

After the self-test exists, add `bash scripts/test-cgroupd-integration.sh --self-test` to RPM `%check`; it must remain unprivileged and must not inspect or modify host cgroupfs.

- [ ] **Step 2: Implement real delegated-tree scenarios**

The script exports `SERVICECTL_CGROUP_TEST_ROOT` and runs `go test ./cmd/sys-cgroupd -run TestCgroupV2Integration -count=1`. `cmd/sys-cgroupd/integration_test.go` starts an in-process fake `VisionSource`, the real scheduler/server, and fixture processes, then validates:

1. Direct ready migration after at least the settle delay.
2. Notify service exclusion before ready.
3. MainPID-first descendant convergence.
4. Explicit same-UID attach through the socket.
5. Cross-UID attach rejection when an alternate test user exists.
6. Daemon restart recovery.
7. Epoch change and stale timer cancellation.
8. Empty stopped-group removal.
9. Populated stopped-group preservation.
10. No writes to controller, freeze, or kill files, checked through an injected audit wrapper in the test build.

Cleanup terminates only fixture PIDs it created with ordinary `kill`, waits for them, and removes only empty test cgroups.

- [ ] **Step 3: Run self-test and real test when available**

Run:

```bash
bash scripts/test-cgroupd-integration.sh --self-test
sudo bash scripts/test-cgroupd-integration.sh --cgroup-root /sys/fs/cgroup/servicectl-test
```

Expected: self-test always PASS. Real test PASS on a writable delegated cgroup v2 root; otherwise print a precise prerequisite error without modifying the host hierarchy.

- [ ] **Step 4: Document operations**

Add README sections containing:

```text
servicectl cgroup status
servicectl cgroup list [--user]
servicectl cgroup inspect UNIT [--user]
servicectl cgroup pids UNIT [--user]
servicectl cgroup attach UNIT PID [--user]
```

Document asynchronous tracking, default 100 ms delay, no lifecycle/resource authority, custom delegated roots, user-plane isolation, no detach, and degraded behavior.

Update `packaging/SRPM-DESIGN.md` with the new binary, Dinit config, runtime directory, `%check` self-test, and explicit statement that installation does not start the daemon or create/migrate cgroups.

- [ ] **Step 5: Run complete verification**

Run:

```bash
gofmt -w cmd/sysvisiond/*.go cmd/sys-cgroupd/*.go internal/procinfo/*.go internal/visionapi/*.go internal/cgrouptrack/*.go cgroup_cli.go cgroup_cli_test.go servicectl_api.go servicectl_api_test.go
go test -count=1 ./...
go test -race ./cmd/sysvisiond ./cmd/sys-cgroupd ./internal/visionapi ./internal/cgrouptrack .
go vet ./...
bash -n scripts/install.sh scripts/test-install-paths.sh scripts/test-cgroupd-integration.sh
bash scripts/test-install-paths.sh
bash scripts/test-cgroupd-integration.sh --self-test
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 6: Rebuild SRPM and RPM**

Run:

```bash
./packaging/fetch-sources.sh --offline
./packaging/build-srpm.sh --allow-dirty --offline
mkdir -p /tmp/opencode/servicectl-cgroupd-rpmbuild
rpmbuild --rebuild --define '_topdir /tmp/opencode/servicectl-cgroupd-rpmbuild' dist/srpm/servicectl-stack-*.src.rpm
rpm -K dist/srpm/*.src.rpm /tmp/opencode/servicectl-cgroupd-rpmbuild/RPMS/*/*.rpm
```

Expected: `%check` passes, RPMs are written, and all digests report OK.

- [ ] **Step 7: Audit package payload and forbidden functionality**

Run:

```bash
rpm -qpl /tmp/opencode/servicectl-cgroupd-rpmbuild/RPMS/*/servicectl-*.rpm | grep -E 'sys-cgroupd|sysvisiond'
rg 'cgroup\.kill|cgroup\.freeze|cgroup\.subtree_control|cpu\.|memory\.|io\.|pids\.|cpuset\.' cmd/sys-cgroupd internal/cgrouptrack
```

Expected: payload lists `/usr/bin/sys-cgroupd` and `/etc/dinit.d/sys-cgroupd`; the forbidden-file search finds only explicit rejection/allowlist tests and diagnostics, never a write path.

- [ ] **Step 8: Commit integration and documentation**

```bash
git add scripts/test-cgroupd-integration.sh cmd/sys-cgroupd/integration_test.go README.md packaging/SRPM-DESIGN.md packaging/servicectl-stack.spec
git commit -m "test: verify sys-cgroupd on cgroup v2"
```

## Final Review Checklist

- [ ] Every design requirement has a corresponding task and test.
- [ ] `sys-cgroupd` never starts, stops, signals, freezes, or kills a service.
- [ ] No service command is wrapped or gated on cgroup availability.
- [ ] Direct Dinit, notify, and lazy manager semantics are distinct and tested.
- [ ] Epoch, generation, boot ID, PID, and start time are checked before migration.
- [ ] MainPID is moved before existing descendants.
- [ ] Explicit attach moves only the requested PID and has no detach operation.
- [ ] User sockets, snapshots, queries, and PID writes are scoped by authenticated UID.
- [ ] A user cannot move a process out of the system plane or another UID's subtree.
- [ ] User `sysvisiond` runs as that user with `--mode=user`; root does not impersonate user planes.
- [ ] `servicectl serve-api` exposes only the process-selected event plane.
- [ ] All cgroup paths derive from validated structured keys under a pinned root.
- [ ] Kernel membership wins over registry hints after restart.
- [ ] Event loss always leads to snapshot reconciliation rather than guessed replay.
- [ ] RPM and local installation do not auto-start the daemon or modify cgroupfs.
