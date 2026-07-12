# servicectl Terminal UI Design

> The `status` design in this document is superseded by
> [servicectl Status Topology Design](2026-07-12-servicectl-status-topology-design.md).
> The `list` design remains current.

## Summary

Upgrade the interactive output of `servicectl list` and `servicectl status`
without turning the command into a full-screen TUI. The default terminal
experience becomes a compact operational dashboard with clear state emphasis,
responsive layout, and stronger separation between application units and
servicectl's internal services.

The rendering implementation uses one shared view-model layer and three output
modes:

- terminal UI for an interactive TTY
- plain human-readable text for `--plain` or non-TTY stdout
- versioned structured output for `--json`

JSON is the stable machine interface. Plain output is intended for humans and
must be deterministic, but scripts should not parse its spacing or labels.

## Goals

- Make failures and transitional states immediately visible.
- Use horizontal terminal space effectively without producing a dense wall of
  fields.
- Show application units by default and move implementation services behind
  `list --all`.
- Keep narrow terminals usable through deterministic responsive behavior.
- Provide an explicit, versioned JSON contract for automation.
- Preserve the existing command semantics and exit codes.

## Non-Goals

- a full-screen interactive TUI
- live polling, keyboard navigation, filtering, or selection
- redesigning `show`, group, cgroup, property, or raw dinit output
- exposing renderer-specific strings as a machine API
- changing service lifecycle, readiness, or backend behavior

## Command Interface

The affected commands accept these display flags:

```text
servicectl [--user] list [--all] [--plain | --json]
servicectl [--user] status UNIT [--plain | --json]
```

`--plain` and `--json` are mutually exclusive. Supplying both is a usage error
and returns a non-zero status.

Output-mode precedence is:

1. explicit `--json`
2. explicit `--plain`
3. terminal UI when stdout is a TTY
4. automatic plain output when stdout is redirected or piped

`NO_COLOR`, `TERM=dumb`, and a non-TTY stdout disable color. They do not change
an explicitly selected JSON mode.

## Architecture

### Shared View Models

Data collection and presentation are separated. Existing service data is
normalized into renderer-neutral structures before any text is produced.

The list model contains:

- schema version
- mode (`system` or `user`)
- generation timestamp
- application-unit rows
- optional internal-service rows

Each list row contains raw values for:

- full unit name
- short display name
- description
- canonical runtime state
- enabled state
- service type
- main PID
- manager/backend metadata
- whether the row is internal

The status model contains these logical sections:

- identity
- state
- process
- orchestration
- control plane
- failure

Renderers receive the same model. They must not query dinit, sysvisiond,
propertyd, process state, or unit files themselves.

### Data Sources

Application-unit state is sourced from the current sysvision snapshot path,
with the existing direct-query fallback retained where required. Enabled state
uses the current effective-enabled calculation. Unit description, source path,
and `Type=` come from the parsed systemd unit.

The default list contains only application `.service` units. `list --all`
keeps those rows in the primary section and appends a separate `INTERNAL`
section for backend services such as orchestrators, notify/dbus managers,
loggers, and other generated supervisor entries. Internal rows are not mixed
into the application-unit sort order.

Missing data is not silently converted to `inactive`. Unknown important state
is represented as `unknown`; optional values such as PID or bus owner are
omitted when unavailable.

### Canonical Runtime States

Backend-specific values are normalized to a small display vocabulary:

- `failed`
- `activating`
- `deactivating`
- `active`
- `inactive`
- `unknown`

More detailed backend state remains available in status sections and JSON.
Sorting uses the order above, with activating and deactivating sharing the
transitional priority. Rows with the same priority sort by short unit name.

## List Terminal UI

### Default Layout

The application list uses a borderless three-segment row:

```text
SERVICES · system · 5 units

● failed       api                         [notify] enabled  pid 1842
● activating   worker                      [simple] enabled
● active       metrics                     [simple] disabled pid 902
● inactive     cleanup                     [oneshot] disabled
● unknown      legacy                      [service] disabled
```

The segments are:

1. left: status dot and canonical runtime state
2. center: left-aligned short service name, without `.service`; the column is
   only as wide as the longest name in the current section
3. right: `[service type]`, enabled state, and effective main PID when present

The service-type badge prefers the unit file's `Type=` value, including
`simple`, `notify`, `forking`, `oneshot`, and `dbus`. Missing type metadata uses
`[service]`.

The header includes scope and count, but does not repeat a `Mode` column on
every row. State color is used on the dot and state word only. Names and
metadata remain neutral so the display does not become visually noisy.

### `list --all`

`--all` appends a second section:

```text
INTERNAL · 8 services

● active       api-orchestrd               [orchestrator] pid 2011
● active       api-notifyd                  [manager]      pid 2024
● inactive     api-log                      [logger]
```

Internal badges describe the implementation role, not the unit-file service
type. This section is explicitly diagnostic and may expose backend names.

### Empty State

An empty application list prints:

```text
SERVICES · system · 0 units

No service units found.
```

JSON returns an empty `units` array.

## Status Terminal UI

### Wide Layout

Status uses a low-contrast outer frame whose width is approximately half of the
available terminal. At sufficient panel width, the body is a roughly equal
two-column panel, and the failure section spans the full panel width.

```text
┌ api ─────────────────────────────────────────── [notify] ┐
│ API gateway                                      ● failed │
├ STATE ─────────────────────┬ ORCHESTRATION ───────────────┤
│ Runtime       failed       │ Phase          failed         │
│ Enabled       yes          │ Child          stopped        │
│ Main PID      1842         │ Manager        sys-notifyd    │
│ Since         2m 14s       │ Backend        dinit          │
├ PROCESS ───────────────────┼ CONTROL PLANE ────────────────┤
│ Manager PID   1810         │ Mode           system         │
│ Start time    12:31:04     │ Source         /etc/.../api…  │
│ Status        waiting      │ Bus            degraded       │
├ FAILURE ───────────────────────────────────────────────────┤
│ Reason        readiness timeout      Result     status 1   │
│ Last change   2m 14s                 Hint       inspect…   │
└────────────────────────────────────────────────────────────┘
```

The header presents identity, description, unit type, and primary state. The
body uses these pairings:

- left upper: runtime state, enabled state, main PID, active duration
- right upper: phase, child state, manager, backend/activation model
- left lower: process and manager details
- right lower: mode, source, orchestration/control-plane state

Empty optional rows are omitted. Paired sections independently compact their
rows; a missing left-side value does not require a blank placeholder if the
layout can move the next value upward.

The `FAILURE` section appears only when the unit is failed, degraded, or has a
failure/result/diagnostic value. Failure text may wrap. The failure heading and
primary failure state use error color; the frame remains dim.

### Narrow Layout

Below the wide-layout threshold, the same outer frame is retained but sections
stack vertically in this order:

1. state
2. process
3. orchestration
4. control plane
5. failure, when present

No horizontal scrolling is introduced. Long paths, commands, and status text
are truncated only in terminal UI mode. Truncation preserves the most useful
path suffix where practical and uses a single ellipsis marker.

Terminal width is read from stdout with a Linux terminal-width helper and a
conservative fallback. Rendering is deterministic for a supplied width so it
can be snapshot-tested without a real terminal.

## Plain Output

Plain output is selected explicitly with `--plain` or automatically for a
non-TTY stdout. It has:

- no ANSI sequences
- no Unicode state dots or box drawing
- no terminal-width adaptation
- no truncation of values
- stable section and field ordering

Plain output remains human-readable and line-oriented. It is not the supported
machine contract; automation should use `--json`.

List plain output uses one row per unit with fixed labels/order. Status plain
output uses section headers followed by `Label: value` lines. Empty optional
fields and the absent failure section are omitted consistently.

## JSON Contract

### List

`list --json` returns an object so top-level metadata can evolve without
changing the root type:

```json
{
  "schema_version": 1,
  "mode": "system",
  "generated_at": "2026-07-11T12:34:56Z",
  "units": [
    {
      "unit": "api.service",
      "name": "api",
      "description": "API gateway",
      "runtime_state": "failed",
      "enabled_state": "enabled",
      "type": "notify",
      "main_pid": 1842,
      "internal": false
    }
  ]
}
```

`list --all --json` uses the same array and marks internal entries with
`"internal": true`. Internal rows may additionally include role and backend
name fields. Numeric PIDs are JSON numbers and are omitted when unknown.

### Status

`status --json` mirrors the logical UI sections while preserving raw,
untruncated values:

```json
{
  "schema_version": 1,
  "identity": {
    "unit": "api.service",
    "name": "api",
    "description": "API gateway",
    "type": "notify"
  },
  "state": {
    "runtime_state": "failed",
    "enabled_state": "enabled",
    "activation": "explicit",
    "updated_at": "2026-07-11T12:32:42Z"
  },
  "process": {
    "main_pid": 1842,
    "manager_pid": 1810
  },
  "orchestration": {
    "phase": "failed",
    "child_state": "stopped",
    "orchestrator": "api-orchestrd",
    "managed_by": "sys-notifyd",
    "backend": "dinit"
  },
  "control_plane": {
    "mode": "system",
    "source_path": "/etc/systemd/system/api.service",
    "bus_state": "degraded"
  },
  "failure": {
    "reason": "readiness timeout",
    "result": "status 1"
  }
}
```

Optional empty sections are omitted. Field names and the meaning of
`schema_version: 1` are compatibility-tested. UI labels and colors are not part
of this contract.

### JSON Errors

When JSON mode cannot resolve a requested unit, stderr remains available for
unexpected operational diagnostics, while stdout receives a stable error
object:

```json
{
  "schema_version": 1,
  "error": {
    "code": "unit_not_found",
    "message": "Unit missing.service could not be found.",
    "unit": "missing.service"
  }
}
```

The command retains a non-zero exit status.

## Styling

The visual language is a restrained operational dashboard:

- green: active/running
- yellow: transitional/degraded
- red: failed/error
- gray: inactive/unknown and structural metadata
- cyan or terminal accent: section headings only

Color supplements text and never carries meaning alone. `NO_COLOR` disables
all ANSI styling while retaining the layout on a TTY.

The implementation extends the existing lightweight ANSI helpers rather than
adding a full TUI framework. Box drawing, visible-width calculation,
truncation, padding, and two-column composition are isolated in renderer
helpers. ANSI escape bytes must not count toward visible width.

## Error Handling

- Unit-not-found behavior preserves the existing non-zero exit status.
- A failed primary data query falls back only where the command already has a
  valid fallback path; renderer code never invents data.
- Partial optional metadata does not fail the whole display.
- Invalid flag combinations fail before service queries run.
- JSON encoding failures return non-zero and do not fall back to plain output.
- Broken-pipe behavior follows normal CLI conventions and must not print a
  second decorative error message.

## Testing

### View-Model Tests

- canonical state mapping for direct, notify, socket, and dbus-managed units
- enabled and PID normalization
- type extraction and `[service]` fallback
- application versus internal classification
- failure-first sorting and name tie-breaking
- missing metadata remains unknown/omitted rather than inactive

### Renderer Tests

- wide status frame and two-column alignment
- narrow status stacking order
- list three-segment alignment
- ANSI-aware visible-width calculations
- path/text truncation and no truncation in plain/JSON modes
- failure section shown only when applicable
- empty list message
- `NO_COLOR` and non-TTY behavior

Golden tests use fixed widths and normalized timestamps. They compare complete
rendered output for representative active, inactive, transitional, failed,
notify-managed, and dbus-managed units.

### JSON Contract Tests

- exact top-level keys for schema version 1
- nested status section names
- numeric PID representation
- omitted optional fields and sections
- `list --all` internal markers
- stable error object and non-zero exit status
- JSON remains free of ANSI sequences and display truncation

### CLI Tests

- `--plain` and `--json` are mutually exclusive
- non-TTY defaults to plain
- TTY defaults to the terminal renderer
- `--all` changes list scope without changing application rows
- existing lifecycle command exit semantics remain unchanged

## Implementation Boundaries

The first implementation should keep the change focused:

- add view models and normalization helpers
- add terminal, plain, and JSON renderers
- route only `list` and `status` through the new layer
- reuse existing parsers and state queries
- avoid changing `show` or broad shared command behavior

The renderer API should accept an `io.Writer`, explicit color capability, and
explicit terminal width. This keeps tests deterministic and avoids global
stdout mutation.

## Success Criteria

- A failed unit is visually discoverable near the top of `list` without
  reading backend-specific text.
- `status` uses horizontal space on a normal terminal and remains readable on
  a narrow terminal.
- Default list output is application-focused; internal services remain
  available with `--all`.
- Piped output contains no ANSI codes or box drawing.
- `--json` is stable, complete, untruncated, and suitable for automation.
- Existing service management behavior and exit codes are unchanged.
