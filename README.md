# servicectl

`servicectl` is a `systemctl`-like CLI for a dinit-based service stack, extended with an s6 orchestration layer and a local event/query bus.

## Components

- `servicectl`: the main CLI and local control surface
- `dinit`: the real service supervisor for workload units
- `sys-notifyd`: runtime state bridge for notify/socket-managed services
- `sys-logd`: syslog endpoint using the standard `/dev/log` path
- `sys-propertyd`: property/group state source for virtual targets and persisted enablement
- `servicectl serve-api`: local unit snapshot and event API
- `sysvisiond`: watch/query bus and event broker
- `sys-orchestrd`: local orchestrator managed by s6 for units and groups
- `s6`: top-level orchestration presence and dependency layer

## Architecture

Runtime control flow:

`s6 -> servicectl-api/sys-propertyd -> sysvisiond -> sys-orchestrd -> servicectl -> dinit/sys-notifyd -> real service`

Key rules:

- `servicectl` remains the only CLI/control surface
- `dinit` remains the final real service supervisor
- `sys-propertyd` is the truth source for persisted group/property state
- `sysvisiond` is bus-only: watch/query/broadcast, no persisted state and no decisions
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
- `scripts/`: install and regression test scripts

## Build

Build the main CLI:

```bash
go build -o servicectl .
```

Install all main binaries:

```bash
bash ./scripts/install.sh
```

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
```

`scripts/test-s6-live.sh` is for manual live s6 validation and is intentionally not part of `test-all.sh`.

## Runtime Paths

System mode:

- `/run/servicectl`
- `/s6/rc`

User mode:

- `/run/user/<uid>/servicectl`
- shared `/s6/rc` graph with user-mode daemons started via `--user`

The user runtime path is based on `/run/user/<uid>` semantics, not `XDG_RUNTIME_DIR` semantics. The s6 source graph is shared; system/user differences live in the daemon arguments and runtime socket paths rather than separate s6 source trees.
