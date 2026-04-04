# servicectl

`servicectl` is a `systemctl`-like CLI for a dinit-based service stack, extended with an s6 orchestration layer and a local event/query bus.

## Components

- `servicectl`: the main CLI and local control surface
- `dinit`: the real service supervisor for workload units
- `sys-notifyd`: runtime state bridge for notify/socket-managed services
- `sys-logd`: syslog endpoint using the standard `/dev/log` path
- `servicectl serve-api`: local unit snapshot and event API
- `sysvisiond`: watch/query bus and event broker
- `sys-orchestrd`: per-unit local orchestrator managed by s6
- `s6`: top-level orchestration presence and dependency layer

## Architecture

Runtime control flow:

`s6 -> servicectl-api -> sysvisiond -> sys-orchestrd -> servicectl -> dinit/sys-notifyd -> real service`

Key rules:

- `servicectl` remains the only CLI/control surface
- `dinit` remains the final real service supervisor
- `sysvisiond` is bus-only: watch/query/broadcast, no persisted state and no decisions
- `sys-orchestrd` is the per-unit local state machine and calls `servicectl`
- `s6` supervises orchestrators and static startup dependencies

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

Run the full regression suite:

```bash
bash ./scripts/test-all.sh
```

Useful focused tests:

```bash
bash ./scripts/test-sysvisiond-bus.sh
bash ./scripts/test-sys-orchestrd.sh
bash ./scripts/test-s6-orchestrd.sh
```

`scripts/test-s6-live.sh` is for manual live s6 validation and is intentionally not part of `test-all.sh`.

## Runtime Paths

System mode:

- `/run/servicectl`
- `/s6/rc`

User mode:

- `/run/user/<uid>/servicectl`
- `/run/user/<uid>/s6/rc`

The user backend path is based on `/run/user/<uid>` semantics, not `XDG_RUNTIME_DIR` semantics.
