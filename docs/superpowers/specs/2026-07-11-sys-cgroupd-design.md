# sys-cgroupd cgroup v2 Process Tracking Design

## Summary

`sys-cgroupd` is a root daemon that assigns servicectl-managed service
processes to dedicated cgroup v2 leaf groups. Its only responsibility is
process membership tracking.

It does not:

- configure CPU, memory, IO, pids, cpuset, or other resource controllers
- freeze processes
- send signals
- write `cgroup.kill`
- replace Dinit, s6, `sys-notifyd`, or servicectl lifecycle control
- discover service ownership by executable name, command line, or UID
- remount or unmount cgroup v2
- mount cgroup2 over an existing filesystem or non-empty directory

The first release covers both system and user services. All servicectl-managed
services are eligible for automatic tracking. A service starts independently
of `sys-cgroupd`; tracking is asynchronous and converges after the service is
ready. Tracking failures never stop an otherwise running service.

## Goals

- Give every running servicectl-managed service a dedicated cgroup v2 leaf.
- Move the service MainPID and its descendants after readiness stabilizes.
- Let callers explicitly attach additional PIDs to a running service.
- Recover after daemon restarts, event loss, and event-source reconnects.
- Support system services and isolated per-UID user services.
- Reject guessed, stale, cross-user, and path-injection process assignments.
- Preserve running processes when state is uncertain or cleanup is unsafe.

## Non-Goals

- Resource limits or accounting policy.
- Service start, stop, restart, signal, watchdog, or failure policy.
- Killing residual processes during service stop or package removal.
- Moving an explicitly attached process back to its former cgroup.
- Automatically matching detached processes to services.
- Atomically placing a process in its service cgroup before its first exec.
- Managing cgroups outside the configured servicectl subtree.

## Components

### sys-cgroupd

`sys-cgroupd` owns the servicectl cgroup subtree, subscribes to normalized
service lifecycle events, reconciles kernel membership, serves status queries,
and processes explicit attach requests.

It runs as root because it must manage both system and user service cgroups.
There is no per-user `sys-cgroupd` process.

### sysvisiond

`sysvisiond` remains the source of normalized service identity and lifecycle
state. It combines direct Dinit state with `sys-notifyd` runtime state and
publishes one lifecycle model for both kinds of service.

`sys-cgroupd` treats lifecycle events as invalidation hints. It queries a fresh
unit snapshot before acting and does not trust a PID carried only in an event.

### Existing Supervisors

Dinit, s6, `sys-notifyd`, and servicectl keep all existing start, stop, signal,
and readiness responsibilities. No service command is wrapped with a cgroup
startup gate.

## cgroup Hierarchy

The daemon first locates the cgroup v2 mount by parsing `/proc/self/mountinfo`
for filesystem type `cgroup2`, then confirms that `/proc/self/cgroup` contains
the unified `0::` entry. Legacy v1 controller entries such as `freezer`,
`memory`, `cpuset`, `cpu`, or `blkio` are ignored. If no cgroup2 mount is
visible, the daemon may mount cgroup2 at `/sys/fs/cgroup` only when the target
is a real empty directory. Existing filesystems and non-empty directories are
never hidden. Auto-mount is enabled by default and can be disabled with
`--no-auto-mount`. On a normal unified or hybrid host the default managed root
is therefore:

```text
/sys/fs/cgroup/servicectl.slice
```

An administrator may pass `--cgroup-root` to select an existing writable,
delegated cgroup v2 subtree. `sys-cgroupd` verifies the selected root and never
mounts directly at that custom leaf. It never remounts or unmounts cgroup2 and
does not modify the outer manager's controller configuration.

The fixed hierarchy is:

```text
/sys/fs/cgroup/servicectl.slice/
|-- system/
|   `-- <service-stem>/
`-- user/
    `-- <uid>/
        `-- <service-stem>/
```

Service groups are leaves. Clients never provide a cgroup path. They provide a
structured mode, UID, and unit name, from which the daemon derives the path.

Unit directories use the canonical service name without its final `.service`
suffix. The decoder restores exactly one `.service` suffix. Empty names, `.`,
`..`, NUL, slash, backslash, and non-canonical aliases are rejected. Legacy
Base64 directories are recognized for known units and migrated to readable
names during reconciliation; if both leaves exist, member PIDs are moved into
the readable leaf before the legacy leaf is removed.

`sys-cgroupd` does not write `cgroup.subtree_control` and does not enable any
controllers. It never writes `cpu.*`, `memory.*`, `io.*`, `pids.*`,
`cpuset.*`, `cgroup.freeze`, or `cgroup.kill`.

## Lifecycle Events

### Normalized Events

`sysvisiond` publishes these lifecycle events:

- `unit.ready`
- `unit.main-pid-changed`
- `unit.stopped`

Each event identifies mode, UID, canonical unit, vision epoch, and generation.
The event may include a MainPID for diagnostics, but consumers must query the
unit snapshot before using it.

### Direct Dinit Services

A directly managed Dinit service becomes ready when Dinit reports it started
and a valid MainPID exists.

### sys-notifyd Services

A `sys-notifyd`-managed service becomes ready only when its runtime phase is
`ready` and it has a valid MainPID.

For lazy socket- or D-Bus-activated services, a ready manager without a backend
MainPID does not trigger migration. The lifecycle becomes ready only after the
backend process starts and reaches its normal readiness condition.

### Generation Rules

`sysvisiond` creates a random `vision_epoch` at each daemon start and exposes
it through `/v1/meta`.

Within one epoch, generation increases monotonically for each
`mode+uid+unit`. A changed MainPID, changed MainPID start time, stopped service,
or new ready instance advances the generation. A restarted `sysvisiond` gets a
new epoch, so delayed work from the former process cannot match new state even
if generation values restart.

### Snapshot and Stream Ordering

On startup and reconnect, `sys-cgroupd` first obtains the event source metadata
and a full unit snapshot, then starts stream consumption and performs another
reconciliation pass. Events received during this transition only trigger
fresh queries. Correctness does not depend on replaying every event.

Queue overflow closes the subscriber instead of silently dropping an
unbounded history. Reconnection always includes full snapshot reconciliation.

## User Event Planes

System lifecycle events come from the system `sysvisiond` plane. Each user
plane is served from that user's runtime directory under `/run/user/<uid>`.

Unit snapshots and lifecycle events gain an explicit UID field:

- system plane snapshots use UID 0
- user plane snapshots use the actual user UID, including UID 0 for the root
  user's isolated user plane

The root `sys-cgroupd` observes valid numeric runtime directories, verifies
directory and socket ownership, and subscribes to each user's watch endpoint.
The trusted UID comes from the verified socket path and filesystem owner, not
from event JSON.

When a user plane appears, the daemon performs a full snapshot before normal
stream processing. When it disappears, existing kernel membership remains
unchanged and the source becomes `event-source-offline`. Reappearance triggers
full reconciliation.

## Service Instance Identity

Each tracked service instance uses this joint identity:

```text
mode
uid
canonical unit
boot_id
main_pid
main_pid_starttime
vision_epoch
generation
```

`boot_id` comes from `/proc/sys/kernel/random/boot_id`.
`main_pid_starttime` comes from `/proc/<pid>/stat` and protects against PID
reuse. Epoch and generation protect delayed work against service and
`sysvisiond` restarts.

The daemon revalidates this identity before every migration attempt. A changed
field cancels stale work and creates a new settle timer when appropriate.

## Automatic Migration

### Stable Delay

After a valid `unit.ready` event, `sys-cgroupd` waits a configurable delay,
defaulting to 100 ms. A changed or stopped generation cancels the timer. The
first release provides a global delay; per-service overrides are deferred.

### Candidate Discovery

At timer expiry, the daemon:

1. Queries the unit snapshot again.
2. Confirms the same instance identity is still ready/running.
3. Opens a pidfd for MainPID where supported.
4. Revalidates PID, start time, UID, and current cgroup.
5. Reads `/proc` and constructs a PPID graph.
6. Finds MainPID and every descendant visible in that graph.
7. Revalidates each candidate before migration.

For system services, descendants may have changed UID as part of normal
privilege dropping. For user services, only processes still owned by that user
are moved. A descendant with a different UID is skipped and reported.

### Converging Migration

MainPID is written to the target leaf's `cgroup.procs` first, so processes it
forks after that point inherit the target cgroup. The descendants captured by
the initial scan are then written in parent-before-child order. The daemon
scans again after each round. Migration finishes when a complete round finds
no new eligible descendants.

The work is bounded by both a maximum round count and a migration deadline. If
the bounds are reached, the unit becomes `partial`; periodic reconciliation
continues trying to converge.

After writing a PID, the daemon verifies its pidfd/start-time identity and
checks that `/proc/<pid>/cgroup` resolves to the intended target. A process may
exit at any point without turning the whole unit into a permanent error.

Because cgroup v2 accepts a numeric PID in `cgroup.procs`, this asynchronous
design cannot claim atomic placement or complete elimination of PID reuse
races. pidfds, start-time checks, and post-write verification reduce and detect
the race. Processes that detached from the MainPID tree before scanning are
not guessed; they require explicit attach.

Once a process is in the service cgroup, future descendants inherit that
membership through normal cgroup v2 semantics.

## Explicit PID Attach

An authenticated client may attach one additional PID to a currently running
service. Attach moves only the specified process. Its future descendants
inherit the target cgroup; existing descendants are not implicitly selected by
the attach operation.

There is no detach operation. A later explicit attach to another valid service
may change ownership if all authorization rules permit it.

An attach succeeds only when:

- the target unit exists in the trusted `sysvisiond` plane
- the target unit is ready/running
- its epoch and generation remain unchanged through the operation
- the PID exists, is not a kernel thread or zombie, and has stable identity
- a user caller owns the PID and targets only its own user plane
- a user caller does not move a PID out of the system plane or another UID's
  servicectl cgroup
- a system-plane request comes from UID 0
- post-write verification observes the PID in the target cgroup

Success means the identified process was observed in the target cgroup after
the write. It does not promise that the process remains alive.

## API and Authorization

`sys-cgroupd` listens on:

```text
/run/servicectl/sys-cgroupd.sock
```

The protocol uses bounded, length-framed JSON requests and responses over a
Unix stream socket. Requests contain structured fields only; arbitrary cgroup
paths and arbitrary cgroup file names are not accepted.

The socket is root-owned and mode 0666 so every local user can reach its user
plane. Authorization never relies on socket mode alone. Unprivileged status
responses are scoped to the authenticated user's plane and do not enumerate
system units, other users, or global error details. UID 0 receives the global
view.

Supported operations are:

- `status`
- `list-units`
- `get-unit`
- `list-pids`
- `attach`

The daemon authenticates every connection using `SO_PEERCRED`:

- system-plane operations require peer UID 0
- a user peer with UID N may query or attach only within `user/N`
- a requested user UID must equal the authenticated peer UID
- peer-supplied UID values never broaden authority

For each PID operation, the daemon reads process status, stat, and cgroup
membership, records `(pid, starttime, uid)`, opens a pidfd where supported, and
rechecks the identity before and after writing. Cross-UID, dead, zombie,
kernel-thread, reused, or identity-changing PIDs are rejected.

The peer and daemon must be in the same PID namespace. The daemon compares the
namespace identities exposed by `/proc/<peer-pid>/ns/pid` and
`/proc/self/ns/pid`; callers in another PID namespace are rejected because a
numeric request PID would be ambiguous.

The server sets request-size, connection, concurrent-request, and per-peer rate
limits to prevent user-plane access from becoming a root-daemon resource
exhaustion path.

## CLI

servicectl exposes:

```text
servicectl cgroup status
servicectl cgroup list [--user]
servicectl cgroup inspect UNIT [--user]
servicectl cgroup pids UNIT [--user]
servicectl cgroup attach UNIT PID [--user]
```

`status` reports daemon health, cgroup root, event-source state, last full
reconciliation, and counts of pending or abnormal units.

`list` reports unit, UID, generation, MainPID, member count, and tracking
state. `inspect` adds the complete instance identity, target path, last
migration, last error, and a member summary. `pids` reports PID, start time,
UID, command name, and MainPID status. Full process arguments and environment
are not displayed by default.

`attach` is the only write operation. The CLI always uses the daemon API and
never writes cgroupfs directly. The first release uses human-readable CLI
output; machine consumers use the structured daemon API until servicectl gains
a repository-wide machine-output convention.

## Reconciliation and Recovery

Reconciliation runs:

- at daemon startup
- after any event-source reconnect
- after stream overflow or protocol failure
- on a fixed interval
- on `SIGHUP`

It reads only the configured servicectl cgroup subtree and trusted
`sysvisiond` snapshots. It never scans the whole process table to infer unknown
service ownership.

All cgroup filesystem operations are relative to a file descriptor for the
validated root. Implementations use no-symlink, beneath-root resolution and
verify cgroupfs inode types instead of reopening client-influenced absolute
paths.

Rules are:

- a ready/running unit with a valid MainPID enters delayed migration
- a stopped unit with an empty group has its leaf removed
- a stopped unit with a populated group remains as `orphaned-populated`
- a group whose unit no longer exists is removed only when empty
- a populated group with no known unit remains as `unknown-unit`
- an offline event source preserves current kernel facts and becomes
  `event-source-offline`
- cleanup proceeds leaf-to-root and touches only decodable, validated paths

`sys-cgroupd` never kills or moves residual members during reconciliation.

## Runtime Registry

cgroupfs cannot store arbitrary servicectl metadata. Recovery hints are stored
at:

```text
/run/servicectl/sys-cgroupd/registry.json
```

The registry records instance identity, last state, migration timestamps,
retry state, and recent errors. It is root-owned, mode 0600, size-limited, and
written by atomic replacement with directory synchronization.

The registry is never the source of process-membership truth. Kernel cgroup
files win when they disagree. A malformed registry is moved to a timestamped
quarantine file, after which state is reconstructed from cgroupfs and
`sysvisiond`. Corruption never causes deletion of a populated cgroup.

## State and Error Model

Per-unit tracking states are:

- `pending`: waiting for the readiness settle delay
- `tracked`: MainPID and all currently discovered eligible descendants were
  observed in the target group
- `partial`: migration bounds were reached before convergence
- `degraded`: a required query, root operation, or cgroup write is failing
- `stopped`: the service is stopped and its group is empty or removed
- `orphaned-populated`: the service stopped while members remain
- `unknown-unit`: a cgroup exists without a unit in a trusted plane
- `event-source-offline`: kernel membership is retained while lifecycle state
  cannot be refreshed

Invalid units, cross-UID access, PID identity mismatch, and attach to a stopped
target are permanent request failures. Event disconnects, transient process
exit, query timeout, and cgroup write errors use bounded exponential backoff.
One unit's failure never blocks other units or terminates the daemon.

## Configuration and Signals

Representative invocation:

```text
sys-cgroupd \
  --cgroup-root=/sys/fs/cgroup/servicectl.slice \
  --socket=/run/servicectl/sys-cgroupd.sock \
  --settle-delay=100ms \
  --reconcile-interval=30s \
  --migration-deadline=250ms \
  --migration-max-rounds=8
```

Timing and limit parameters have safe lower and upper bounds to prevent
accidental high-frequency `/proc` scans.

`SIGHUP` reloads adjustable timing and limit settings and starts a full
reconciliation. The cgroup root and socket path cannot change online.

`SIGTERM` stops subscriptions, lets current cgroup writes finish, persists the
registry, and exits. It does not delete groups, migrate processes, or signal
members.

Logs include mode, UID, canonical unit, epoch, generation, PID, operation, and
error class. They do not include process environments or complete argv values.

## Deployment

Packaging adds:

- `/usr/bin/sys-cgroupd`
- a Dinit service definition
- runtime directory creation rules
- the daemon and CLI files to the RPM manifests

`sysvisiond` lifecycle epoch, generation, UID, snapshot, and normalized event
support must be deployed before `sys-cgroupd` is enabled.

Installation and upgrade do not:

- mount cgroup2
- replace PID 1
- enable cgroup controllers
- migrate existing processes in package scriptlets
- start or stop application services

If the default root is not writable under an outer manager, the administrator
must configure an already delegated root. Uninstall removes empty runtime
state where safe but never removes a populated cgroup.

## Testing

### Unit Tests

Unit tests cover:

- unit encoding, decoding, canonicalization, and traversal rejection
- parsing `/proc/<pid>/stat`, including command names containing spaces and `)`
- PPID graph construction, depth ordering, disappearing processes, and bounds
- PID start-time, pidfd, UID, and post-write membership verification
- epoch/generation transitions, stale timer cancellation, and reconnects
- direct Dinit and `sys-notifyd` lifecycle normalization
- lazy activation with a ready manager but absent backend MainPID
- system and user `SO_PEERCRED` authorization
- user-scoped status visibility and cross-PID-namespace rejection
- cross-UID and stale-PID rejection
- registry atomic writes, corruption quarantine, and recovery
- state transitions, retry classes, and backoff limits
- assurance that no API accepts arbitrary cgroup paths or control files

### Real cgroup v2 Integration Tests

Integration tests run in an isolated delegated cgroup v2 subtree and cover:

- direct Dinit migration after the readiness delay
- notify service exclusion before `READY=1`
- lazy socket and D-Bus manager exclusion until backend readiness
- MainPID restart during the settle window
- migration of a multi-level descendant tree
- migration ordering that moves MainPID first so concurrent new forks inherit
  the target group
- continued forking during migration, ending as `tracked` or diagnosable
  `partial`
- explicit same-UID attach
- cross-UID attach and attach-to-stopped-unit rejection
- `sys-cgroupd` restart recovery
- `sysvisiond` restart and epoch changes
- event loss, queue overflow, reconnect, and full reconciliation
- removal of stopped empty groups
- retention of stopped populated groups without signals or kills
- custom delegated roots
- degraded behavior for non-cgroup2 or unwritable roots
- verification that no controller, freeze, or kill files are written

### Acceptance Criteria

- Every identifiable ready servicectl service eventually converges to its
  dedicated cgroup under healthy conditions.
- A process whose ownership cannot be proven is never assigned by guesswork.
- Tracking failure does not stop or prevent an application service.
- System and user authorization boundaries hold in real multi-user tests.
- PID identity checks detect stale and reused PIDs before reporting success.
- Restart, event-loss, and registry-corruption tests converge without killing
  or losing track of populated groups.
- No implementation path configures resource policy or assumes service
  lifecycle authority.
