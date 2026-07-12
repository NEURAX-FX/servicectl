# servicectl Status Topology Design

## Summary

Replace the existing field-grid implementation of `servicectl status` with a
topology-first operational view. The new view answers four questions in order:

1. Is the service process running?
2. Is the control and supervision path healthy?
3. Which concrete component is responsible for each part of that path?
4. What evidence explains an unhealthy or unobservable component?

The implementation uses one native graph model as the source for terminal,
plain-text, and JSON output. Renderers do not infer components or health from
display strings.

This document supersedes the status-specific sections of
`2026-07-11-servicectl-terminal-ui-design.md`. It does not change that
document's `list` design.

## Scope

This change covers:

- `servicectl [--user] status UNIT`
- `status --plain`
- `status --json`, with a new schema version 2 contract
- a new `status --verbose` human-readable mode
- status health aggregation and exit status
- compact recent logs in human-readable and JSON output

This change does not redesign:

- `list` or its JSON schema version 1 contract
- `show`
- group, property, cgroup, or activation commands
- lifecycle operations
- the service manager or supervision architecture itself
- a full-screen or interactive TUI

## Command Interface

The accepted forms are:

```text
servicectl [--user] status UNIT
servicectl [--user] status UNIT --plain
servicectl [--user] status UNIT --verbose
servicectl [--user] status UNIT --plain --verbose
servicectl [--user] status UNIT --json
```

`--plain` and `--json` remain mutually exclusive. `--json --verbose` is a usage
error because JSON always contains the complete available model and has no
display density setting.

Output-mode precedence remains:

1. explicit `--json`
2. explicit `--plain`
3. terminal output when stdout is a TTY
4. automatic plain output when stdout is not a TTY

## Design Principles

### Show Real Participation

The topology contains only components that participate in this service's
control, activation, supervision, accounting, or observation path. It does not
print a fixed architecture template and does not add `N/A` placeholders for
layers that do not apply.

A component that authoritative data says should participate remains in the
graph when it is missing at runtime. That node is evidence of a broken expected
relationship, not an unused architecture layer.

### Preserve Relationship Semantics

The graph distinguishes a primary control path from parallel relationships.
For example, cgroup accounting and state observation are side branches unless
the authoritative source explicitly identifies them as part of the primary
path. The renderer must not serialize parallel components into a fake call
chain.

### Separate Runtime State from Orchestration Health

The service runtime state and orchestration health are separate values. A
service may be `active` while its accounting controller is missing. The final
display state preserves both facts as `active (degraded)`.

### Evidence Before Inference

Authoritative structured relationships decide whether a node belongs in the
graph. Runtime probes validate those relationships. A probe may change a
declared node's observed state, but a process-name, path, or socket-name match
alone must not invent a participating node.

## Status Domain Model

The renderer-neutral status model contains:

- schema version
- observation time
- identity
- summary
- orchestration graph
- diagnostics
- recent logs

### Identity

Identity contains:

- full unit name
- short display name
- description
- service type
- scope (`system` or `user@UID`)
- source unit path

### Summary

Summary contains:

- canonical runtime state
- orchestration health
- aggregate health
- combined display state
- enabled state
- main PID when known
- process start time and active duration in seconds when known

Canonical runtime states remain independent of graph health:

- `failed`
- `activating`
- `deactivating`
- `active`
- `inactive`
- `unknown`

### Nodes

Each graph node has these common properties:

- stable ID
- component type
- human-readable name
- scope
- health
- component-specific state
- whether the node is expected by authoritative data
- key identity such as PID, endpoint, bus owner, or cgroup path when applicable
- observation time when known
- evidence records

The stable ID format is:

```text
TYPE:SCOPE:IDENTITY
```

Examples:

```text
service:system:cliproxyapi.service
sys-orchestrd:system:cliproxyapi.service
sys-cgroupd:system:system
sysvbus:user@1000:user@1000
dbus:system:system
```

IDs are deterministic and never include a runtime PID. Component identities
must be normalized before ID construction so the same component retains its ID
across samples.

ID segments use these canonical forms:

- type is the lowercase protocol or component type
- scope is `system` or `user@DECIMAL_UID`
- a service or per-unit manager identity is its canonical unit name, including
  the `.service` suffix
- a scope-wide singleton uses `system` or `user@UID` as its identity
- a bus uses its declared instance name; the default instance uses its scope
- aliases are resolved before constructing an ID

Each UTF-8 segment is percent-encoded byte-by-byte outside
`A-Z a-z 0-9 . _ @ -`; `%` and `:` are therefore always encoded inside a
segment. IDs that still collide after canonicalization are a collection error,
not an opportunity to append an unstable suffix. The repeated scope in an ID
such as `sysvbus:user@1000:user@1000` is deliberate: the second segment is the
component's execution scope and the third is the declared bus instance.

Bus nodes are named by actual protocol and scope. Human-readable examples are
`sysvbus · system`, `sysvbus · user@1000`, `dbus · system`, and
`dbus · user@1000`. The UI must not collapse these to a generic `Bus` node.

### Edges

Each edge contains:

- source node ID
- target node ID
- relation type
- whether it belongs to the primary path

The initial stable relation vocabulary is:

- `controls`
- `activates`
- `supervises`
- `accounts`
- `observes`

New relation values require a schema review. Renderers may choose different
visual treatment for primary and secondary edges, but must not change their
meaning.

The terminal renderer labels important edges with their relation. It may omit
a redundant label only when the adjacent node roles make the relationship
unambiguous. JSON includes a relation on every edge.

### Evidence

An evidence record identifies:

- source
- result
- whether the source is authoritative
- collector check time
- optional source observation time
- optional detail

Examples of authoritative sources include the manager response, orchestration
registry, unit configuration, and sysvision snapshot. Examples of validation
sources include PID identity, socket reachability, cgroup membership, and
component status queries.

Evidence is merged according to these rules:

1. Authoritative data establishes participation and expected relationships.
2. A successful probe establishes observed state and key identity.
3. A conclusive not-found probe for an expected node produces `missing`.
4. A failed, timed-out, stale, or inconclusive probe produces `unknown` unless
   another current authoritative source proves the node unhealthy.
5. Optional metadata that is unavailable does not make a healthy node unknown.
6. Probe-only discoveries do not become topology nodes.

Evidence sources are stable snake-case identifiers. The initial source values
are `manager`, `orchestration_registry`, `unit_configuration`,
`sysvision_snapshot`, `pid_probe`, `socket_probe`, `cgroup_probe`, and
`component_status`. Evidence results are `expected`, `present`, `healthy`,
`unhealthy`, `not_found`, `timeout`, `stale`, and `error`. Adding a source or
result is an additive schema change; changing an existing meaning requires a
schema version change.

Freshness is determined from each source's generation or observation metadata,
not from renderer timing. A source-specific collector owns its freshness bound.
The normalizer records `stale` evidence with both the source observation time
and collector check time. If two equally preferred current authoritative
sources conflict, the affected relationship is retained, its health becomes
unknown, and a `conflicting_authority` diagnostic identifies both sources.
Source precedence is explicit and relationship-specific in collector code and
is covered by tests.

## Health Model

### Node Health

Every node has one of these health values:

- `healthy`: the expected component is present and its relevant state is good
- `failed`: the service process has a failed runtime state
- `degraded`: the component is conclusively unhealthy or expected but missing
- `unknown`: participation is known but current health cannot be established

`missing` is a component state paired with `degraded` health. It is not treated
as `unknown`.

The service process node is the terminal node of the primary path. Its health
is `failed` when runtime state is failed, `unknown` when runtime state is
unknown, and `healthy` for active, inactive, activating, or deactivating. An
inactive or transitional state is a lifecycle fact, not by itself a broken
status query. Non-service component failures map to `degraded`, not `failed`.

### Aggregate Health

Aggregate severity is deterministic:

```text
failed > degraded > unknown > healthy
```

Health-affecting diagnostics participate in aggregation at their declared
domain and severity. A failed diagnostic explains an already-failed service
runtime; it cannot independently promote another runtime state to failed. The
rules are:

1. A failed service node produces aggregate `failed`.
2. Otherwise, any degraded participating node or health-affecting degraded
   diagnostic produces aggregate `degraded`.
3. Otherwise, any unknown participating node or health-affecting unknown
   diagnostic produces aggregate `unknown`.
4. Otherwise, aggregate health is `healthy`.

`orchestration_health` excludes the service process node and has the values
`healthy`, `degraded`, or `unknown`. It includes health-affecting diagnostics
whose domain is orchestration. `aggregate_health` includes both the
runtime-derived service node, runtime diagnostics, and orchestration. Both
values are present in the summary and JSON.

Multiple conditions are retained in diagnostics even though the header shows
only the highest severity.

The combined display state is deterministic. When runtime state is `failed`, it
is exactly `failed`, regardless of lower-severity orchestration conditions.
Otherwise it is the runtime state by itself when aggregate health is healthy,
and `RUNTIME (AGGREGATE_HEALTH)` when aggregate health is degraded or unknown.
Examples include:

```text
active
active (degraded)
active (unknown)
activating (degraded)
inactive (unknown)
failed
```

`failed` is never rendered as `failed (failed)` or `failed (degraded)`; lower
severity conditions remain visible in topology and diagnostics.

### Exit Status

Status exit behavior is:

| Exit | Meaning |
| ---: | --- |
| `0` | Unit was resolved and aggregate health is healthy |
| `1` | Usage, mandatory collection, rendering, or encoding error |
| `3` | Unit was resolved and aggregate health is failed, degraded, or unknown |
| `4` | Unit was not found |

A healthy inactive or transitional unit returns `0`; the exit status reports
status-query health, not an `is-active` predicate. Callers that need lifecycle
state must inspect `summary.runtime_state`.

## Topology Construction

### Collection Pipeline

Status generation has four stages:

1. Collect raw unit, manager, registry, process, bus, cgroup, and log evidence.
2. Normalize the evidence into stable nodes and typed edges.
3. Aggregate node health, service health, diagnostics, and exit classification.
4. Render the completed model as terminal, plain, or JSON output.

Renderers never query the operating system and never add, remove, or reclassify
nodes.

Unit resolution, the canonical unit definition, and the service runtime query
are mandatory. If any fails without a valid authoritative fallback, collection
cannot form a status model and returns exit 1; a conclusive unit absence returns
exit 4.

Before querying relationships, the collector obtains a complete authoritative
participation manifest from the resolved manager response. A versioned,
deterministic manifest derived from the canonical unit configuration and mode
is a valid fallback. The manifest lists every applicable relationship namespace
and explicitly marks non-applicable namespaces; absence alone never means not
applicable. If neither manifest is complete, collection returns exit 1.

Each authoritative relationship source then reports whether its response is
complete for its owned namespace and generation. Graph membership is safe only
when every namespace listed as applicable has one complete authoritative
response, either from the preferred source or a valid complete fallback. An
incomplete response may contribute evidence but never proves that omitted nodes
or edges do not exist. If no complete source covers an applicable namespace,
collection returns exit 1. Once membership is complete, failures while
validating a known participant become node-level unknown and exit 3. Partial
validation failures are retained as evidence and diagnostics rather than
aborting the model.

### Primary Path and Side Branches

The primary path is selected from explicit `primary` edges supplied by the
normalizer. It represents the principal control and supervision path and ends
at the service node.

Primary edges must induce exactly one connected simple directed path. It has
one unique root with no incoming primary edge, the service node is its unique
terminal node with no outgoing primary edge, every other primary node has
exactly one incoming and one outgoing primary edge, every primary edge is
reachable from the root, all endpoints exist, and the path has no cycle. A
graph that violates these invariants is a normalization error and returns exit
1. A valid path may consist only of the service node when authoritative data
proves there is no external control component.

Non-primary edges render as branches from their source node. Typical examples
are cgroup accounting and state observation. A service may have more than one
branch and more than one bus node if authoritative relationships require them.

Node and edge ordering is deterministic. Siblings sort by relation, component
type, scope, and stable ID. Input query order must not affect output.

Non-primary edges may contain cycles. JSON retains those valid edges. Human
renderers expand a node once and display a reference at subsequent visits
instead of recursing.

## Terminal Output

### Overall Structure

The terminal status page is borderless and uses these sections:

1. identity and summary
2. `ORCHESTRATION`
3. `DIAGNOSTICS`, only when entries exist
4. `RECENT LOGS`, when logs exist or log retrieval produced a diagnostic

It replaces the old `STATE`, `PROCESS`, `ORCHESTRATION`, and `CONTROL PLANE`
field grid.

### Normal Example

This example is illustrative. Actual output includes only nodes that the
service verifiably participates in.

```text
cliproxyapi.service · CLI proxy API
● active  healthy    enabled · system · notify
PID 2418 · running 3h 12m
/etc/systemd/system/cliproxyapi.service

ORCHESTRATION
servicectl-api · system                           healthy  pid 1102
    │ controls
    ▼
s6 supervisor · system                           healthy  pid 734
    │ supervises
    ▼
sys-orchestrd · cliproxyapi.service              healthy  pid 2401
    ├── accounts ── sys-cgroupd · system         healthy
    │               /servicectl.slice/cliproxyapi
    ├── observes ── sysvisiond · system          healthy
    │
    ▼ activates
sysvbus · system                                 healthy  owner :1.42
    │ supervises
    ▼
cliproxyapi.service                              healthy  pid 2418

RECENT LOGS
12:31:04 ready on 127.0.0.1:8317
12:31:04 registered org.example.Cliproxy
```

### Degraded Example

The header preserves the fact that the service process is running while making
the control-plane failure visible.

```text
cliproxyapi.service · CLI proxy API
● active (degraded)    enabled · system · notify
PID 2418 · running 3h 12m

ORCHESTRATION
s6 supervisor · system                           healthy  pid 734
    │ supervises
    ▼
sys-orchestrd · cliproxyapi.service              healthy
    ├── accounts ── sys-cgroupd · system         missing
    │               expected by cgroup policy
    ▼ activates
cliproxyapi.service                              healthy  pid 2418

DIAGNOSTICS
! Expected node sys-cgroupd:system:system is missing.
  Confirmed 12:30:00 via runtime probe; last seen 12:29:51.
  Hint: inspect `servicectl cgroup status`.

RECENT LOGS
12:31:04 ready on 127.0.0.1:8317
```

### Unknown Example

An unobservable known participant remains in place:

```text
sysvbus · user@1000                            unknown
  status query timed out · observed 12:29:51
```

The summary becomes `active (unknown)` unless a higher-severity condition is
also present.

### Responsive Behavior

At widths that can display the primary path and a side branch without losing
node identity, the renderer uses the main-chain-plus-branches form. Below that
threshold it renders the same graph as an indented tree:

```text
cliproxyapi.service
● active (degraded)
enabled · system · notify
PID 2418

ORCHESTRATION
s6 supervisor · system
▼ supervises
sys-orchestrd · cliproxyapi.service
├─ accounts
│  sys-cgroupd · system
│  degraded · missing
│
▼ activates
cliproxyapi.service
healthy · pid 2418
```

The `▼` spine is always the primary path; `├─` and `└─` are side relationships.
The narrow renderer retains every node and relationship. It wraps long values
instead of horizontally clipping key identity or diagnostic text. Rendering is
deterministic for a supplied terminal width so complete output can be golden
tested. Widths of 96 columns and above use the normal branch layout; narrower
widths use the indented layout.

### Styling

TTY output uses Unicode line drawing and optional ANSI styling:

- green for healthy and active
- yellow for degraded and transitional states
- red for failed
- gray or dim yellow for unknown
- a restrained accent for section headings

Color never carries meaning by itself. `NO_COLOR`, `TERM=dumb`, and non-TTY
stdout disable color. Disabling color does not remove labels, health words, or
topology lines.

### Default and Verbose Density

Each default node shows:

- component or protocol name
- scope or service identity
- health
- the most useful PID, endpoint, bus owner, or cgroup path
- a short reason when unhealthy or unknown

`--verbose` additionally shows all available:

- endpoints and paths
- evidence source and result
- observation times
- manager and child process identity
- process tree information
- diagnostic detail
- expanded log context

Verbose mode changes presentation only. It does not trigger a different health
model or include nodes that default mode omitted from the graph.

## Plain Output

Plain output presents the same summary, graph, diagnostics, and logs with:

- ASCII connectors
- no ANSI styling
- stable ordering
- no terminal-width adaptation
- no truncation

Its structural characters are ASCII; user-controlled descriptions and log data
remain unchanged and may contain Unicode. It is human-readable and
deterministic but is not a machine compatibility contract. Automation must use
JSON.

## Recent Logs

The collector requests the newest 50 entries whose timestamps are not later
than the top-level `observed_at`. During normalization, every entry receives a
non-negative `source_sequence`; a backend without sequence metadata supplies
zero. Entries are canonically ordered ascending by timestamp, source sequence,
stream, severity, and message. This same tuple selects the newest 50 when more
are available; exact duplicate tuples are interchangeable because all their
required JSON values are identical. JSON and verbose human output contain
those collected entries in canonical order.

Default human-readable output contains at most five. `error` and `critical`
are error severities. If the collected set contains either, default output uses
a five-entry window centered on the canonically newest error, taking up to two
entries before and two after it and filling any unused slots with its nearest
remaining neighbors. Otherwise it uses the final five entries. Text matching
is never used to guess error severity.

Log collection follows these rules:

- log failure never changes service or orchestration health
- unavailable logs produce a non-health-affecting diagnostic
- log messages remain raw data until rendering
- terminal output may wrap messages but must not alter their content
- JSON preserves all 50 or fewer collected timestamps, streams, severities,
  and messages
- status remains bounded and never turns into an unbounded log-follow command

## JSON Schema Version 2

`status --json` moves to schema version 2. Version 2 replaces the status v1
`process`, `control_plane`, and flat `orchestration` sections. There is no
compatibility flag and no duplicate v1 representation.

`list --json` remains schema version 1 because list is outside this change.

### Field Contract

The success object's required top-level fields are `schema_version`,
`observed_at`, `identity`, `summary`, `orchestration`, `diagnostics`, and
`logs`.

Top-level `observed_at` is a cutoff captured after status evidence collection
and immediately before the bounded log query. All evidence `checked_at` values
are not later than this cutoff, and the log query excludes later entries. It is
not a source generation timestamp.

Required summary fields are:

| Field | JSON type | Values or meaning |
| --- | --- | --- |
| `runtime_state` | string | canonical runtime state |
| `orchestration_health` | string | `healthy`, `degraded`, or `unknown` |
| `aggregate_health` | string | `healthy`, `failed`, `degraded`, or `unknown` |
| `display_state` | string | combined human-readable state |
| `enabled_state` | string | canonical enabled state |

Optional summary fields are numeric `main_pid`, RFC 3339 `started_at`, and
non-negative integer `active_duration_seconds`.

Every node requires `id`, `type`, `name`, `scope`, `health`, `state`,
`expected`, `observed_at`, and `evidence`. `state` is a component-specific
snake-case value and consumers must preserve unknown values. The following
typed optional fields carry key identity without component-specific untyped
objects:

| Field | JSON type |
| --- | --- |
| `pid` | number |
| `manager_pid` | number |
| `child_pids` | array of numbers |
| `endpoint` | string |
| `bus_owner` | string |
| `cgroup_path` | string |
| `process_started_at` | RFC 3339 string |
| `active_duration_seconds` | non-negative integer |
| `last_seen_at` | RFC 3339 string |

Node `observed_at` is the latest evidence `checked_at` used to assign node
health. It is required even for healthy nodes. For `missing`, it is the time
absence was confirmed; `last_seen_at` is the optional source observation time
at which the component was last known present.

Every evidence object requires string `source`, string `result`, boolean
`authoritative`, and RFC 3339 `checked_at`. RFC 3339 `source_observed_at` and
string `detail` are optional. Every edge requires string `from`, string `to`,
string `relation`, and boolean `primary`. Both edge IDs must reference nodes in
the same object.

Every diagnostic requires `severity`, `code`, `message`, `affects_health`, and
`domain`, and `observed_at`. Severity values are `failed`, `degraded`,
`unknown`, and `info`; domain values are `runtime`, `orchestration`, and
`output`. `node_id`, `hint`, and `source` are optional strings. A
health-affecting diagnostic must use the runtime or orchestration domain and
the `failed`, `degraded`, or `unknown` severity. `failed` is valid only for the
runtime domain and only when `summary.runtime_state` is also `failed`; it
explains that runtime fact but does not independently affect aggregation. A
broken non-service component uses `degraded`. An output or informational
diagnostic must set `affects_health` to false.

Every log object requires RFC 3339 `timestamp`, string `stream`, string
`severity`, and string `message`. Log severity values are `debug`, `info`,
`warning`, `error`, `critical`, and `unknown`. Non-negative integer
`source_sequence` is required.

### Success Object

```json
{
  "schema_version": 2,
  "observed_at": "2026-07-12T10:30:00Z",
  "identity": {
    "unit": "cliproxyapi.service",
    "name": "cliproxyapi",
    "description": "CLI proxy API",
    "type": "notify",
    "scope": "system",
    "source_path": "/etc/systemd/system/cliproxyapi.service"
  },
  "summary": {
    "runtime_state": "active",
    "orchestration_health": "degraded",
    "aggregate_health": "degraded",
    "display_state": "active (degraded)",
    "enabled_state": "enabled",
    "main_pid": 2418
  },
  "orchestration": {
    "nodes": [
      {
        "id": "sys-cgroupd:system:system",
        "type": "sys-cgroupd",
        "name": "sys-cgroupd",
        "scope": "system",
        "health": "degraded",
        "state": "missing",
        "expected": true,
        "observed_at": "2026-07-12T10:30:00Z",
        "last_seen_at": "2026-07-12T10:29:51Z",
        "evidence": [
          {
            "source": "orchestration_registry",
            "result": "expected",
            "authoritative": true,
            "checked_at": "2026-07-12T10:30:00Z",
            "source_observed_at": "2026-07-12T10:29:59Z"
          },
          {
            "source": "component_status",
            "result": "not_found",
            "authoritative": false,
            "checked_at": "2026-07-12T10:30:00Z"
          }
        ]
      },
      {
        "id": "sys-orchestrd:system:cliproxyapi.service",
        "type": "sys-orchestrd",
        "name": "sys-orchestrd",
        "scope": "system",
        "health": "healthy",
        "state": "active",
        "expected": true,
        "observed_at": "2026-07-12T10:30:00Z",
        "pid": 2401,
        "evidence": [
          {
            "source": "orchestration_registry",
            "result": "healthy",
            "authoritative": true,
            "checked_at": "2026-07-12T10:30:00Z",
            "source_observed_at": "2026-07-12T10:29:59Z"
          }
        ]
      },
      {
        "id": "service:system:cliproxyapi.service",
        "type": "service",
        "name": "cliproxyapi.service",
        "scope": "system",
        "health": "healthy",
        "state": "active",
        "expected": true,
        "observed_at": "2026-07-12T10:30:00Z",
        "pid": 2418,
        "evidence": [
          {
            "source": "sysvision_snapshot",
            "result": "healthy",
            "authoritative": true,
            "checked_at": "2026-07-12T10:30:00Z",
            "source_observed_at": "2026-07-12T10:29:58Z"
          }
        ]
      }
    ],
    "edges": [
      {
        "from": "sys-orchestrd:system:cliproxyapi.service",
        "to": "sys-cgroupd:system:system",
        "relation": "accounts",
        "primary": false
      },
      {
        "from": "sys-orchestrd:system:cliproxyapi.service",
        "to": "service:system:cliproxyapi.service",
        "relation": "activates",
        "primary": true
      }
    ]
  },
  "diagnostics": [
    {
      "severity": "degraded",
      "domain": "orchestration",
      "code": "expected_node_missing",
      "node_id": "sys-cgroupd:system:system",
      "message": "Expected cgroup controller is missing.",
      "hint": "Inspect servicectl cgroup status.",
      "affects_health": true,
      "observed_at": "2026-07-12T10:30:00Z",
      "source": "component_status"
    }
  ],
  "logs": [
    {
      "timestamp": "2026-07-12T10:29:58Z",
      "source_sequence": 81,
      "stream": "stdout",
      "severity": "info",
      "message": "ready on 127.0.0.1:8317"
    }
  ]
}
```

All data values are raw and untruncated. PIDs are JSON numbers. Timestamps use
RFC 3339 with available precision.

These arrays are always present, even when empty:

- `orchestration.nodes`
- `orchestration.edges`
- each node's `evidence`
- `diagnostics`
- `logs`

Optional scalar fields are omitted when unknown. Required fields are never
omitted. `--verbose` never changes the JSON shape or amount of collected status
data.

### Diagnostics

Diagnostic codes are stable snake-case identifiers. Non-health-affecting
conditions such as unavailable logs use informational severity and set
`affects_health` to false.

### Errors

JSON errors also use schema version 2. A missing unit returns:

```json
{
  "schema_version": 2,
  "error": {
    "code": "unit_not_found",
    "message": "Unit missing.service could not be found.",
    "unit": "missing.service"
  }
}
```

Unexpected operational diagnostics may use stderr. JSON encoding failure does
not fall back to plain output.

## Error Handling

- Partial optional metadata does not fail status collection.
- A failed mandatory root query follows the collection boundary and exits 1.
- Missing or unreadable validation evidence for a known participant produces
  degraded or unknown according to the health rules; it is never silently
  converted to inactive.
- Invalid flag combinations fail before status collection.
- A broken pipe follows normal CLI behavior and does not print a second
  decorative error.
- Renderer failure does not produce a different output mode.
- Log retrieval failure is diagnostic only and does not change exit status.

## Testing

### Normalization Tests

- stable node IDs across PID changes
- system and user scope normalization
- protocol-specific bus names including sysvbus and dbus
- authoritative relationships create nodes and edges
- runtime probes validate but do not invent nodes
- expected missing nodes become degraded/missing
- inconclusive probes become unknown
- unavailable optional fields do not degrade a node
- deterministic sibling and edge ordering
- cycles terminate in human renderers

### Aggregation Tests

- failed outranks degraded, unknown, and healthy
- degraded outranks unknown and healthy
- unknown outranks healthy
- healthy inactive and transitional units remain successful queries
- all diagnostics survive aggregation
- display-state composition does not produce `failed (failed)`
- exit statuses 0, 1, 3, and 4 match the contract

### Renderer Tests

- normal primary chain
- side branches for accounting and observation
- multiple bus protocols and scopes
- degraded and unknown nodes remain in topology
- service process is the terminal primary node
- narrow layout preserves all nodes and edges
- verbose details do not change model health
- ANSI-aware alignment
- `NO_COLOR`, `TERM=dumb`, and non-TTY behavior
- plain output uses ASCII structure and does not truncate user data
- recent-log limit, error-context selection, and unavailable-log diagnostic

Golden tests use fixed widths, timestamps, and durations and compare complete
output for healthy, failed, degraded, unknown, system, user, wide, narrow, and
color-disabled cases.

### JSON Contract Tests

- exact schema version 2 field names and types
- status v1 sections are absent
- required arrays are present when empty
- raw values are not truncated
- typed edges reference existing node IDs
- PIDs are numeric and timestamps are RFC 3339
- diagnostics retain stable codes and health impact
- missing-unit errors use schema version 2 and exit 4
- `list --json` remains schema version 1

## Acceptance Criteria

The design is complete when:

- `status` no longer renders the old four-section field grid
- every displayed topology node is backed by authoritative participation data
- sysvbus, dbus, and future bus protocols are shown by actual type and scope
- primary and parallel relationships are visually and structurally distinct
- active services with broken control components show `active (degraded)`
- known but unobservable nodes show `unknown` without disappearing
- terminal and plain output derive only from the shared graph model
- JSON schema version 2 exposes the complete node and edge graph
- unhealthy and unknown aggregate states return exit 3
- narrow output preserves topology information without horizontal clipping
- default output remains compact and verbose output exposes supporting evidence
- log failures do not misclassify service health
