# servicectl

`servicectl` is a systemd-unit-compatible service control stack built around
Dinit and s6-rc. It reads familiar `.service` and `.socket` definitions,
generates Dinit services for workload execution, and adds a local control plane
for orchestration, state observation, cgroup membership, D-Bus activation,
logging, and system/user isolation.

It is not a replacement PID 1 and it is not a complete reimplementation of
systemd. The project focuses on running and operating service units on a
Dinit/s6 runtime while preserving useful systemd-facing interfaces where that
can be done accurately.

## Highlights

| Area | Current behavior |
| --- | --- |
| Unit execution | Translates supported systemd service/socket definitions into Dinit services. Dinit remains the final workload supervisor. |
| Orchestration | Uses s6-rc for the system control plane, per-unit orchestrators, groups, targets, and startup dependencies. |
| Status | Builds a topology-first view from Dinit, s6, cgroup, D-Bus, observation, and control-plane evidence. |
| State APIs | Exposes local Unix-socket query, event, property, group, and snapshot APIs through `servicectl-api`, `sysvisiond`, and `sys-propertyd`. |
| Activation | Supports notify, socket, and strict D-Bus activation paths through dedicated helper daemons. |
| Cgroups | Tracks ready service process trees in cgroup v2 leaves without taking ownership of resource-controller policy. |
| Logging | Runs per-unit `sys-logd` workers through a root system broker and emits standard journald object-unit fields, with syslog fallback. |
| Modes | Keeps system and user service planes, runtime sockets, Dinit instances, and service identities separate. |
| Compatibility | Accepts virtual `.target` inputs and can optionally expose a minimal `org.freedesktop.systemd1` API through the separate `systemd1-broker` project. |

## Requirements

The source installation expects a Linux host with:

- Go 1.22 or newer
- Dinit and `dinitctl`
- s6 and s6-rc
- systemd-style unit files in the standard system or user unit paths
- a writable runtime directory for the selected mode

journald is used for structured unit-attributed logs when its native socket is
available. Per-unit log workers fall back to the normal syslog path when the
system log broker cannot be reached. cgroup v2 and D-Bus integration are
optional at runtime; their status becomes degraded or unavailable rather than
being silently fabricated.

## Installation

### From source

Install the CLI and helper daemons under `~/.local`:

```bash
bash scripts/install.sh
```

Install directly under `/usr/local`:

```bash
sudo bash scripts/install.sh --system
```

Deployment shells that deliberately want both the current account's local
prefix and a system copy can use `--also-system`. Under `sudo -H`, the local
copy is installed under root's home:

```bash
sudo -H bash scripts/install.sh --also-system
```

The system installation path also reconciles the system s6 source graph by
running `servicectl ensure-s6`. Running the command again is safe when the core
service definitions need to be rebuilt:

```bash
sudo servicectl ensure-s6
```

Installation does not replace PID 1, reconfigure the host bootloader, or turn
the host into a Dinit-based boot automatically. Boot integration and ownership
of the live s6 supervision tree remain deployment decisions.

### Fedora packages

Fedora 44+ SRPM and RPM instructions are maintained in
[`packaging/README.md`](packaging/README.md). The package filesystem and source
strategy are documented in
[`packaging/SRPM-DESIGN.md`](packaging/SRPM-DESIGN.md).

## Quick Start

List known application units. Add `--all` to include generated and internal
services:

```bash
servicectl list --plain
servicectl list --all --json
```

Operate a system unit:

```bash
servicectl status demo.service
servicectl start demo.service
servicectl restart demo.service
servicectl stop demo.service
servicectl logs -n 100 demo.service
servicectl logs -f demo.service
```

Use the same commands against the user plane:

```bash
servicectl --user list --all --plain
servicectl --user status pipewire.service
servicectl --user restart pipewire.service
servicectl --user logs -f pipewire.service
```

Useful state checks for scripts:

```bash
servicectl is-active demo.service
servicectl is-failed demo.service
servicectl is-enabled demo.service
```

Use `servicectl dinit ...` only when raw Dinit output or an operation outside
the normal servicectl surface is required.

## Enablement, Groups, and Virtual Targets

Persistent enablement is stored by the property/group control plane and is
realized by the s6 backend:

```bash
servicectl enable demo.service
servicectl disable demo.service
servicectl is-enabled demo.service

servicectl enable group:audio
servicectl --group audio status
servicectl --group audio disable
```

`.target` names are accepted as a compatibility input. Internally they resolve
to groups and properties managed by `sys-propertyd`:

```bash
servicectl enable pipewire.target
servicectl --group pipewire status
```

`sys-propertyd` imports `Wants=` and `Requires=` from target files and from
`.target.wants/` or `.target.requires/` directories. Explicit definitions take
priority over imported target membership:

- system groups: `/etc/servicectl/groups.d/*.conf`
- system target aliases: `/etc/servicectl/targets.d/*.conf`
- user groups: `~/.config/servicectl/groups.d/*.conf`
- user target aliases: `~/.config/servicectl/targets.d/*.conf`

Example group definition:

```ini
[Group]
Name=audio
Units=pipewire.service pipewire-pulse.service wireplumber.service
Targets=pipewire.target
```

Example target alias:

```ini
[Target]
Name=pipewire.target
Group=audio
```

For `enable`, `disable`, `is-enabled`, and `status`, a service that belongs to
exactly one group can be resolved automatically. An ambiguous service requires
an explicit `--group` selection.

## Status and Diagnostics

`servicectl status UNIT` separates workload lifecycle from the health of the
components that control or observe it:

```bash
servicectl status demo.service
servicectl status demo.service --plain
servicectl status demo.service --verbose
servicectl status demo.service --json
```

- Terminal output shows a primary control path and side branches. Narrow
  terminals switch to an indented tree without dropping topology data.
- `--plain` emits stable ASCII without ANSI styling or width truncation.
- `--verbose` expands endpoints, evidence, timestamps, process metadata, and
  the bounded log sample.
- `--json` emits status schema version 2 with typed nodes, edges, diagnostics,
  and logs. `list --json` remains schema version 1.
- `active (degraded)` means the workload is running while a declared
  participant is conclusively unhealthy or missing.
- `active (unknown)` means the workload is running but required evidence could
  not be collected reliably.

Status exits with `0` for a resolved healthy model, `3` for failed, degraded,
or unknown aggregate health, `4` for a missing unit, and `1` for usage,
collection, rendering, or encoding failures. The workload lifecycle remains
available separately as `summary.runtime_state` in JSON.

`NO_COLOR`, `TERM=dumb`, and non-TTY output disable color without removing
state labels or topology structure.

## Logging and journald

New generated logger definitions run a per-unit `sys-logd --worker` process
with the target unit identity. Workers send fixed-schema records to the system
`sys-logd` broker at `/run/servicectl/logd`; the broker is a root s6 service and
authenticates each route with Unix peer credentials, process metadata, and the
Dinit logger PID.

The broker writes native journald records with:

- `OBJECT_SYSTEMD_UNIT=<unit>.service` for system units
- `OBJECT_SYSTEMD_USER_UNIT=<unit>.service` for user units
- `SYSLOG_IDENTIFIER=servicectl[<unit>]`

This makes standard journal filtering work:

```bash
journalctl -u demo.service
journalctl --user-unit pipewire.service
```

When the broker cannot accept a record, the worker uses the existing syslog
path and periodically retries the broker. It spills to a private per-route
directory only if both delivery paths fail. Legacy generated logger definitions
are rewritten without restarting business services and take effect at the next
runtime or unit restart.

## Cgroup v2 Tracking

`sys-cgroupd` tracks ready servicectl-managed services in dedicated cgroup v2
leaves. It moves the MainPID first and then descendants visible at migration
time. It does not guess detached processes and it does not configure CPU,
memory, IO, pids, cpuset, freeze, or kill policy.

```bash
servicectl cgroup status demo.service
servicectl cgroup list
servicectl cgroup inspect demo.service
servicectl cgroup pids demo.service
servicectl cgroup attach demo.service 1234

servicectl --user cgroup list
servicectl --user cgroup attach demo.service 5678
```

User requests are authenticated with the Unix socket peer UID and cannot
inspect or modify another user's plane. There is no detach operation. The
default managed root is `/sys/fs/cgroup/servicectl.slice`; use
`sys-cgroupd --cgroup-root` when an outer manager provides a different writable
delegated subtree. `--no-auto-mount` disables the daemon's conservative empty
directory cgroup2 mount behavior.

## D-Bus Activation

`sys-dbusd` provides the strict activation frontend for system units with
`BusName=`. It resolves systemd D-Bus service entries to managed servicectl
routes and activates only registered unit control sockets.

Inspect and configure the D-Bus daemon helper as root:

```bash
servicectl dbus-activation check --backend=daemon
servicectl dbus-activation status --backend=daemon
sudo servicectl dbus-activation enable --backend=daemon
```

Enabling or disabling the helper writes a managed manifest so the previous bus
configuration can be restored safely. Reload or restart the system bus only in
a controlled maintenance window, as printed by the command. Use
`servicectl dbus-activation disable --backend=daemon` to restore the previous
configuration.

## Architecture

The high-level data flow is:

```text
systemd unit files
        |
        v
   servicectl ------> Dinit ------> workload process
        |                |
        |                +-------> per-unit sys-logd worker
        v
   s6-rc control plane -------> sys-orchestrd / core daemons
        |                               |
        +------> sysvisiond <-----------+
                     |
                     +------> status / APIs / cgroup reconciliation
```

| Component | Responsibility |
| --- | --- |
| `servicectl` | CLI, unit parsing, Dinit generation, local API, and mode selection. |
| Dinit | Final supervisor for workload processes and generated per-unit logger services. |
| s6-rc | Supervises the system control plane and UID-qualified user infrastructure; owns orchestrator dependency graphs. |
| `servicectl-api` | Local unit catalog, typed snapshots, status manifests, refresh, and event stream. |
| `sysvisiond` | Normalizes lifecycle events and serves watch/query APIs for one system or user plane. |
| `sys-orchestrd` | Per-unit or per-group state machine that reconciles desired state through servicectl. |
| `sys-propertyd` | Persistent property, group, target, enabled-list, and runner-list truth source. |
| `sys-cgroupd` | Authenticated cgroup membership tracking and reconciliation. |
| `sys-notifyd` | `Type=notify` readiness and managed process state bridge. |
| `sys-socketd` | Managed socket activation for supported `.socket` units. |
| `sys-dbusd` | Managed `BusName=` activation and D-Bus control frontend. |
| `sys-logd` | Root s6 journal broker plus least-privilege per-unit Dinit workers and fallback delivery. |

System and user modes have distinct runtime sockets and Dinit instances. The
s6 source graph is shared, but user infrastructure service names are
UID-qualified.

## systemd Interoperability

### Built-in interoperability

The core project interoperates with systemd-facing tools in several bounded
ways:

- consumes supported systemd unit-file directives
- accepts compatible `.target` names and target dependency inputs
- emits standard journald unit attribution for `journalctl -u` and
  `journalctl --user-unit`
- provides D-Bus daemon activation for supported `BusName=` services
- exposes typed local snapshots that compatibility frontends can consume

### Optional `systemd1-broker`

The separate `systemd1-broker` worktree in the upstream systemd source tree can
act as an optional compatibility frontend. It is not built or packaged by the
current servicectl RPM.

Its servicectl backend connects to:

- `/run/servicectl/servicectl.sock`
- `/run/servicectl/sysvision/sysvisiond.sock`
- `/run/servicectl/managed`

The broker dynamically publishes a minimal `org.freedesktop.systemd1` Manager,
Unit, Service, and Job surface. The current implementation supports unit
catalog/list/get/load operations, lifecycle start/stop/restart/reload jobs, and
typed properties populated from servicectl snapshots. This is sufficient for
a useful subset of `systemctl` and sd-bus clients; it is not the complete
systemd manager API. Continue to use `servicectl enable` and `disable` for
servicectl enablement semantics.

Build the optional targets from a configured systemd build tree:

```bash
meson compile -C build systemd1-broker systemd1-broker-backend-servicectl
```

Example standalone invocation:

```bash
SYSTEMD1_BUILD=/path/to/systemd/build

sudo "$SYSTEMD1_BUILD/systemd1-broker" \
  --socket=/run/systemd/private \
  --bus-address=unix:path=/run/dbus/system_bus_socket \
  --backend="$SYSTEMD1_BUILD/src/systemd1-broker/libsystemd1-broker-backend-servicectl.so"
```

`--socket` is required. The D-Bus address and backend are optional to the
generic broker executable but both are needed for normal servicectl-backed
`systemctl` interoperability. The target bus name and `/run/systemd/private`
must not already be owned by a real systemd manager.

With the broker active, supported lifecycle and status calls can use familiar
clients:

```bash
systemctl list-units --type=service
systemctl status demo.service
systemctl start demo.service
systemctl restart demo.service
systemctl stop demo.service
```

Unit-file enable/disable APIs are intentionally outside the current broker
surface; use `servicectl enable` and `servicectl disable` for those operations.

Backend path overrides are available for test or non-default deployments:

| Variable | Default |
| --- | --- |
| `SYSTEMD1_BROKER_SERVICECTL_SOCKET` | `/run/servicectl/servicectl.sock` |
| `SYSTEMD1_BROKER_SYSVISION_SOCKET` | `/run/servicectl/sysvision/sysvisiond.sock` |
| `SYSTEMD1_BROKER_SERVICECTL_RUNTIME` | `/run/servicectl/managed` |
| `SYSTEMD1_BROKER_SYSTEMD_UNIT_PATH` | Standard system unit search path |

Before starting the broker, ensure the servicectl s6 control plane and the two
query sockets are ready. Run the compatibility broker under a supervisor in a
real deployment rather than as an interactive shell process.

## Local APIs and Runtime Paths

The control-plane daemons use HTTP-like protocols over local Unix sockets.
They are intended for local tools and orchestrators, not as network services.

| Purpose | System path | User path |
| --- | --- | --- |
| servicectl query API | `/run/servicectl/servicectl.sock` | `/run/user/<uid>/servicectl/servicectl.sock` |
| servicectl event stream | `/run/servicectl/servicectl-events.sock` | `/run/user/<uid>/servicectl/servicectl-events.sock` |
| sysvision query API | `/run/servicectl/sysvision/sysvisiond.sock` | `/run/user/<uid>/servicectl/sysvision/sysvisiond.sock` |
| sysvision ingress | `/run/servicectl/sysvision/events.sock` | `/run/user/<uid>/servicectl/sysvision/events.sock` |
| managed service state | `/run/servicectl/managed` | `$XDG_RUNTIME_DIR/servicectl/managed` |
| log broker | `/run/servicectl/logd` | Shared system broker |
| generated Dinit files | `/run/dinit.d/generated` | `$XDG_RUNTIME_DIR/dinit.d/generated` |
| s6 source graph | `/s6/rc` | Shared graph with UID-qualified services |

The system API normally runs as the s6 `servicectl-api` service. It can also be
started directly for development with `servicectl serve-api`. Example queries:

```bash
curl --unix-socket /run/servicectl/servicectl.sock \
  'http://unix/v1/units?all=1'

curl --unix-socket /run/servicectl/servicectl.sock \
  'http://unix/v1/units/demo.service'

curl --unix-socket /run/servicectl/servicectl.sock \
  'http://unix/v1/status-manifest/demo.service'
```

## Development and Testing

Fast repository checks:

```bash
go test ./... -count=1
go vet ./...
go build ./...
bash scripts/test-status-topology.sh
bash scripts/test-cgroupd-integration.sh --self-test
```

The full integration runner is:

```bash
bash scripts/test-all.sh
```

It runs host integration suites after a five-second warning. Those suites may
start or stop real services and write systemd, Dinit, s6, and servicectl
runtime state. Set `SERVICECTL_NO_HOST_WARNING=1` only to suppress the warning;
it does not make the suite non-mutating.

Useful focused integration scripts include:

```bash
bash scripts/test-sys-notifyd.sh
bash scripts/test-system-user-env.sh
bash scripts/test-logs-follow.sh
bash scripts/test-servicectl-dinit.sh
bash scripts/test-property-targets.sh
bash scripts/test-group-orchestration.sh
bash scripts/test-sysvisiond-bus.sh
bash scripts/test-sys-orchestrd.sh
bash scripts/test-s6-orchestrd.sh
```

Real cgroup migration tests require an explicitly delegated, writable cgroup
v2 subtree:

```bash
sudo bash scripts/test-cgroupd-integration.sh \
  --cgroup-root /sys/fs/cgroup/servicectl-test
```

## Repository Layout

- top-level `*.go`: main CLI, unit parsing, generation, API, and shared control logic
- `cmd/`: standalone runtime daemons and helpers
- `internal/visionapi/`: runtime paths, event types, snapshots, and status contracts
- `internal/statusview/`: status topology model and health derivation
- `internal/cgrouptrack/`: cgroup protocol, identity, membership, and recovery
- `internal/dbusactivation/`: D-Bus route resolution and activation frontend
- `scripts/`: installation and regression/integration tests
- `packaging/`: Fedora spec, SRPM tooling, units, tmpfiles, and package design

## Limitations

- Only the systemd unit directives implemented by the parser and generated
  backends have servicectl semantics. Unknown directives do not become full
  systemd behavior automatically.
- Dinit remains the workload supervisor; s6-rc orchestrates the control plane
  and desired state rather than replacing Dinit.
- cgroup tracking is membership/accounting infrastructure, not resource policy.
- D-Bus and `systemd1-broker` compatibility cover defined subsets and should
  not be presented to clients as a complete systemd manager.
- Source installation does not provide a complete boot transaction or choose
  how Dinit and the live s6 tree are started by the host.

## License

`servicectl` is licensed under the MIT License. See [LICENSE](LICENSE).
