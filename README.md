# servicectl

`servicectl` is a `systemctl`-like CLI for a dinit-based service stack, extended with an s6 orchestration layer and a local event/query bus.

## License

`servicectl` is licensed under the MIT License. See [LICENSE](LICENSE).

## Components

- `servicectl`: the main CLI and local control surface
- `dinit`: the real service supervisor for workload units
- `sys-notifyd`: runtime state bridge for notify/socket-managed services
- `sys-logd`: syslog endpoint using the standard `/dev/log` path
- `sys-propertyd`: property/group state source for virtual targets and persisted enablement
- `servicectl serve-api`: local unit snapshot and event API
- `sysvisiond`: watch/query bus and event broker
- `sys-cgroupd`: cgroup v2 process-membership tracker for ready services
- `sys-orchestrd`: local orchestrator managed by s6 for units and groups
- `s6`: top-level orchestration presence and dependency layer

## Architecture

Runtime control flow:

`s6 -> servicectl-api/sys-propertyd -> sysvisiond -> sys-orchestrd/sys-cgroupd -> servicectl -> dinit/sys-notifyd -> real service`

Key rules:

- `servicectl` remains the only CLI/control surface
- `dinit` remains the final real service supervisor
- `sys-propertyd` is the truth source for persisted group/property state
- each `sysvisiond` process serves one system or user event plane and normalizes ready/stopped/MainPID lifecycle state
- `sys-cgroupd` consumes normalized lifecycle state and asynchronously assigns process trees to cgroup v2 leaves
- cgroup tracking never configures CPU, memory, IO, pids, cpuset, freeze, kill, or service lifecycle policy
- `sys-orchestrd` is the local state machine for enabled units/groups and calls `servicectl`
- `s6` supervises orchestrators and static startup dependencies

## Virtual Targets

`servicectl` can now accept virtual target inputs such as `group:audio` and `pipewire.target`.

- External compatibility: `.target` names are accepted at the CLI boundary
- Internal standard: targets are converted into `group`/`property` state managed by `sys-propertyd`
- Persisted enablement uses Android-style keys such as `persist.group.audio=1`
- Runtime overrides can use `prop.group.audio=1`

This keeps `.target` as an input compatibility layer while the internal control plane only speaks properties and groups.

Automatic target conversion:

- `sys-propertyd` scans systemd unit directories for `*.target`
- it imports `[Unit] Wants=` and `Requires=` from target files
- it also imports entries from `<name>.target.wants/` and `<name>.target.requires/`
- nested target-to-target references are flattened until they resolve to `.service` members
- the default internal group name is the target basename, so `pipewire.target` becomes `group:pipewire`
- only `.service` members are imported into groups in this model; non-service unit types stay outside the group orchestration path

Definition priority:

- explicit `groups.d` and `targets.d` definitions override automatically imported target groups
- automatic target import is the compatibility/default path, not the final authority when an explicit mapping exists

CLI entry points:

```bash
servicectl enable pipewire.target
servicectl --group pipewire status
servicectl --group pipewire disable
```

For `enable`, `disable`, `is-enabled`, and `status`, `servicectl` can also auto-resolve a service to its unique group. If a service belongs to exactly one group, group semantics are used automatically. If it belongs to more than one group, `servicectl` stops and asks for an explicit `--group` selection.

Suggested config locations:

- system groups: `/etc/servicectl/groups.d/*.conf`
- system target aliases: `/etc/servicectl/targets.d/*.conf`
- user groups: `~/.config/servicectl/groups.d/*.conf`
- user target aliases: `~/.config/servicectl/targets.d/*.conf`

Example explicit group definition:

```ini
[Group]
Name=audio
Units=pipewire.service pipewire-pulse.service wireplumber.service
Targets=pipewire.target
```

Example explicit target alias:

```ini
[Target]
Name=pipewire.target
Group=audio
```

## Repository Layout

- `main.go` and top-level `*.go`: core `servicectl` CLI and shared logic
- `cmd/`: standalone helper daemons and test binaries
- `internal/visionapi/`: shared runtime path and event/query types
- `internal/cgrouptrack/`: cgroup v2 membership, process identity, protocol, and recovery logic
- `scripts/`: install and regression test scripts

## Packaging

Fedora 44+ SRPM build and installation instructions are in
[`packaging/README.md`](packaging/README.md). The package and filesystem design
is documented in [`packaging/SRPM-DESIGN.md`](packaging/SRPM-DESIGN.md).

## Build

Build the main CLI:

```bash
go build -o servicectl .
```

Install all main binaries:

```bash
bash ./scripts/install.sh
```

## cgroup v2 Tracking

`sys-cgroupd` tracks every ready servicectl-managed system or user service in a
dedicated cgroup v2 leaf. Migration starts after a global 100 ms settle delay.
The service remains running if tracking is unavailable or incomplete; status
shows the degraded or partial state and reconciliation retries later.

The daemon moves the service MainPID first, then its currently visible
descendants. Processes that detached before discovery are never guessed. An
operator can explicitly attach one additional PID:

```bash
servicectl cgroup status
servicectl cgroup list
servicectl cgroup inspect demo.service
servicectl cgroup pids demo.service
servicectl cgroup attach demo.service 1234

servicectl --user cgroup list
servicectl --user cgroup attach demo.service 5678
```

User requests are authenticated through the Unix socket peer UID and can only
query or modify that user's plane. There is no detach operation. Reassignment
between services owned by the same user is allowed; moving a PID out of the
system plane or another user's subtree is rejected.

The default managed root is `/sys/fs/cgroup/servicectl.slice`. On hosts where
an outer manager owns that location, start `sys-cgroupd` with an existing
writable delegated root using `--cgroup-root`. If no cgroup2 mount is visible,
the daemon safely mounts cgroup2 at `/sys/fs/cgroup` only when that path is a
real empty directory. Use `--no-auto-mount` to disable this behavior. The daemon
never remounts or unmounts cgroup2, mounts over existing content, enables
controllers, freezes processes, sends signals, or writes `cgroup.kill`.

Service leaf directories use readable service stems. For example,
`demo.service` maps to `system/demo`, and a root user service maps to
`user/0/demo`. Existing Base64-named leaves from earlier builds are migrated
during reconciliation without killing their member processes.

## Test

Run the default safe regression suite:

```bash
bash ./scripts/test-all.sh
```

The default suite runs build checks, `go test`, and the safe `sys-notifyd` integration path. Host-mutating integration suites are opt-in:

```bash
SERVICECTL_RUN_HOST_INTEGRATION=1 bash ./scripts/test-all.sh
```

Useful focused tests:

```bash
bash ./scripts/test-property-targets.sh
bash ./scripts/test-sysvisiond-bus.sh
bash ./scripts/test-sys-orchestrd.sh
bash ./scripts/test-group-orchestration.sh
bash ./scripts/test-s6-orchestrd.sh
bash ./scripts/test-cgroupd-integration.sh --self-test
```

Real cgroup migration tests are opt-in and require an explicitly delegated,
writable cgroup v2 subtree:

```bash
sudo bash ./scripts/test-cgroupd-integration.sh --cgroup-root /sys/fs/cgroup/servicectl-test
```

The script never auto-selects the host cgroup root and cleans up only fixture
processes it created with ordinary signals.

`scripts/test-s6-live.sh` is for manual live s6 validation and is intentionally not part of `test-all.sh`.

`scripts/test-test-all.sh` is a meta-test that validates `test-all.sh` itself using a fake `go` binary; it is intentionally not part of `test-all.sh`.

## Runtime Paths

System mode:

- `/run/servicectl`
- `/run/servicectl/sys-cgroupd`
- `/s6/rc`

User mode:

- `/run/user/<uid>/servicectl`
- shared `/s6/rc` graph with UID-qualified `sysvisiond-user-<uid>` and
  `servicectl-api-user-<uid>` infrastructure services

The user runtime path is based on `/run/user/<uid>` semantics. Generated user
s6 run scripts set that path explicitly as `XDG_RUNTIME_DIR`; system and user
event planes have distinct infrastructure service names even though the s6
source graph is shared.
