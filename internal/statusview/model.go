package statusview

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = 2

type RuntimeState string

const (
	RuntimeFailed       RuntimeState = "failed"
	RuntimeActivating   RuntimeState = "activating"
	RuntimeDeactivating RuntimeState = "deactivating"
	RuntimeActive       RuntimeState = "active"
	RuntimeInactive     RuntimeState = "inactive"
	RuntimeUnknown      RuntimeState = "unknown"
)

type Health string

const (
	HealthHealthy  Health = "healthy"
	HealthFailed   Health = "failed"
	HealthDegraded Health = "degraded"
	HealthUnknown  Health = "unknown"
)

type Relation string

const (
	RelationControls   Relation = "controls"
	RelationActivates  Relation = "activates"
	RelationSupervises Relation = "supervises"
	RelationAccounts   Relation = "accounts"
	RelationObserves   Relation = "observes"
)

type EvidenceSource string

const (
	EvidenceManager               EvidenceSource = "manager"
	EvidenceOrchestrationRegistry EvidenceSource = "orchestration_registry"
	EvidenceUnitConfiguration     EvidenceSource = "unit_configuration"
	EvidenceSysvisionSnapshot     EvidenceSource = "sysvision_snapshot"
	EvidencePIDProbe              EvidenceSource = "pid_probe"
	EvidenceSocketProbe           EvidenceSource = "socket_probe"
	EvidenceCgroupProbe           EvidenceSource = "cgroup_probe"
	EvidenceComponentStatus       EvidenceSource = "component_status"
)

type EvidenceResult string

const (
	EvidenceExpected  EvidenceResult = "expected"
	EvidencePresent   EvidenceResult = "present"
	EvidenceHealthy   EvidenceResult = "healthy"
	EvidenceUnhealthy EvidenceResult = "unhealthy"
	EvidenceNotFound  EvidenceResult = "not_found"
	EvidenceTimeout   EvidenceResult = "timeout"
	EvidenceStale     EvidenceResult = "stale"
	EvidenceError     EvidenceResult = "error"
)

type DiagnosticSeverity string

const (
	SeverityFailed   DiagnosticSeverity = "failed"
	SeverityDegraded DiagnosticSeverity = "degraded"
	SeverityUnknown  DiagnosticSeverity = "unknown"
	SeverityInfo     DiagnosticSeverity = "info"
)

type DiagnosticDomain string

const (
	DomainRuntime       DiagnosticDomain = "runtime"
	DomainOrchestration DiagnosticDomain = "orchestration"
	DomainOutput        DiagnosticDomain = "output"
)

type LogSeverity string

const (
	LogDebug    LogSeverity = "debug"
	LogInfo     LogSeverity = "info"
	LogWarning  LogSeverity = "warning"
	LogError    LogSeverity = "error"
	LogCritical LogSeverity = "critical"
	LogUnknown  LogSeverity = "unknown"
)

type Identity struct {
	Unit        string `json:"unit"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Scope       string `json:"scope"`
	SourcePath  string `json:"source_path"`
}

type Summary struct {
	RuntimeState          RuntimeState `json:"runtime_state"`
	OrchestrationHealth   Health       `json:"orchestration_health"`
	AggregateHealth       Health       `json:"aggregate_health"`
	DisplayState          string       `json:"display_state"`
	EnabledState          string       `json:"enabled_state"`
	MainPID               int          `json:"main_pid,omitempty"`
	StartedAt             *time.Time   `json:"started_at,omitempty"`
	ActiveDurationSeconds int64        `json:"active_duration_seconds,omitempty"`
}

type Evidence struct {
	Source           EvidenceSource `json:"source"`
	Result           EvidenceResult `json:"result"`
	Authoritative    bool           `json:"authoritative"`
	CheckedAt        time.Time      `json:"checked_at"`
	SourceObservedAt *time.Time     `json:"source_observed_at,omitempty"`
	Detail           string         `json:"detail,omitempty"`
}

type Node struct {
	ID                    string     `json:"id"`
	Type                  string     `json:"type"`
	Name                  string     `json:"name"`
	Scope                 string     `json:"scope"`
	Health                Health     `json:"health"`
	State                 string     `json:"state"`
	Expected              bool       `json:"expected"`
	ObservedAt            time.Time  `json:"observed_at"`
	Evidence              []Evidence `json:"evidence"`
	PID                   int        `json:"pid,omitempty"`
	ManagerPID            int        `json:"manager_pid,omitempty"`
	ChildPIDs             []int      `json:"child_pids,omitempty"`
	Endpoint              string     `json:"endpoint,omitempty"`
	BusOwner              string     `json:"bus_owner,omitempty"`
	CgroupPath            string     `json:"cgroup_path,omitempty"`
	ProcessStartedAt      *time.Time `json:"process_started_at,omitempty"`
	ActiveDurationSeconds int64      `json:"active_duration_seconds,omitempty"`
	LastSeenAt            *time.Time `json:"last_seen_at,omitempty"`
}

type Edge struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	Relation Relation `json:"relation"`
	Primary  bool     `json:"primary"`
}

type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

type Diagnostic struct {
	Severity      DiagnosticSeverity `json:"severity"`
	Domain        DiagnosticDomain   `json:"domain"`
	Code          string             `json:"code"`
	Message       string             `json:"message"`
	AffectsHealth bool               `json:"affects_health"`
	ObservedAt    time.Time          `json:"observed_at"`
	NodeID        string             `json:"node_id,omitempty"`
	Hint          string             `json:"hint,omitempty"`
	Source        string             `json:"source,omitempty"`
}

type LogEntry struct {
	Timestamp      time.Time   `json:"timestamp"`
	SourceSequence uint64      `json:"source_sequence"`
	Stream         string      `json:"stream"`
	Severity       LogSeverity `json:"severity"`
	Message        string      `json:"message"`
}

type Model struct {
	SchemaVersion int          `json:"schema_version"`
	ObservedAt    time.Time    `json:"observed_at"`
	Identity      Identity     `json:"identity"`
	Summary       Summary      `json:"summary"`
	Orchestration Graph        `json:"orchestration"`
	Diagnostics   []Diagnostic `json:"diagnostics"`
	Logs          []LogEntry   `json:"logs"`
}

func NewModel() Model {
	return Model{
		SchemaVersion: SchemaVersion,
		Orchestration: Graph{
			Nodes: []Node{},
			Edges: []Edge{},
		},
		Diagnostics: []Diagnostic{},
		Logs:        []LogEntry{},
	}
}

func NewNode(id, typeName, name, scope string) Node {
	return Node{
		ID:       id,
		Type:     typeName,
		Name:     name,
		Scope:    scope,
		Evidence: []Evidence{},
	}
}

func CanonicalScope(mode string, uid int) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "system":
		return "system", nil
	case "user":
		if uid < 0 {
			return "", fmt.Errorf("user scope requires a non-negative UID")
		}
		return "user@" + strconv.Itoa(uid), nil
	default:
		return "", fmt.Errorf("unsupported scope mode %q", mode)
	}
}

func CanonicalUnitName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("unit name is empty")
	}
	if !strings.HasSuffix(name, ".service") {
		name += ".service"
	}
	return name, nil
}

func NewNodeID(typeName, scope, identity string) (string, error) {
	typeName = strings.ToLower(strings.TrimSpace(typeName))
	scope = strings.TrimSpace(scope)
	identity = strings.TrimSpace(identity)
	if typeName == "" || scope == "" || identity == "" {
		return "", fmt.Errorf("node ID requires type, scope, and identity")
	}
	if !isCanonicalScope(scope) {
		return "", fmt.Errorf("invalid canonical scope %q", scope)
	}
	return encodeIDSegment(typeName) + ":" + encodeIDSegment(scope) + ":" + encodeIDSegment(identity), nil
}

func (m Model) ValidateNodeIDs() error {
	seen := make(map[string]struct{}, len(m.Orchestration.Nodes))
	for _, node := range m.Orchestration.Nodes {
		if _, ok := seen[node.ID]; ok {
			return fmt.Errorf("duplicate node ID %q", node.ID)
		}
		seen[node.ID] = struct{}{}
	}
	return nil
}

func isCanonicalScope(scope string) bool {
	if scope == "system" {
		return true
	}
	if !strings.HasPrefix(scope, "user@") || len(scope) == len("user@") {
		return false
	}
	for _, r := range scope[len("user@"):] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func encodeIDSegment(segment string) string {
	var b strings.Builder
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if isIDByte(c) {
			b.WriteByte(c)
			continue
		}
		const hex = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

func isIDByte(c byte) bool {
	return c >= 'A' && c <= 'Z' ||
		c >= 'a' && c <= 'z' ||
		c >= '0' && c <= '9' ||
		c == '.' || c == '_' || c == '@' || c == '-'
}

// Finalize validates the topology, derives health, and applies deterministic ordering.
func Finalize(model Model) (Model, error) {
	finalized := cloneModel(model)
	serviceID, err := validateGraph(finalized)
	if err != nil {
		return Model{}, err
	}

	deriveNodeHealth(&finalized, serviceID)
	sortGraph(&finalized.Orchestration)
	aggregateHealth(&finalized, serviceID)
	return finalized, nil
}

func ExitCode(model Model) int {
	if model.Summary.AggregateHealth == HealthHealthy {
		return 0
	}
	return 3
}

func validateGraph(model Model) (string, error) {
	if err := model.ValidateNodeIDs(); err != nil {
		return "", err
	}

	nodes := make(map[string]Node, len(model.Orchestration.Nodes))
	serviceID := ""
	for _, node := range model.Orchestration.Nodes {
		nodes[node.ID] = node
		if node.Type == "service" {
			if serviceID != "" {
				return "", fmt.Errorf("multiple service nodes %q and %q", serviceID, node.ID)
			}
			serviceID = node.ID
		}
	}
	if serviceID == "" {
		return "", fmt.Errorf("topology has no service node")
	}

	incoming := make(map[string]int)
	outgoing := make(map[string]int)
	next := make(map[string]string)
	primaryNodes := make(map[string]struct{})
	primaryEdges := 0
	for _, edge := range model.Orchestration.Edges {
		if _, ok := nodes[edge.From]; !ok {
			return "", fmt.Errorf("edge from %q has missing endpoint", edge.From)
		}
		if _, ok := nodes[edge.To]; !ok {
			return "", fmt.Errorf("edge to %q has missing endpoint", edge.To)
		}
		if !edge.Primary {
			continue
		}
		primaryEdges++
		primaryNodes[edge.From] = struct{}{}
		primaryNodes[edge.To] = struct{}{}
		outgoing[edge.From]++
		if outgoing[edge.From] > 1 {
			return "", fmt.Errorf("node %q has multiple outgoing primary edges", edge.From)
		}
		incoming[edge.To]++
		if incoming[edge.To] > 1 {
			return "", fmt.Errorf("node %q has multiple incoming primary edges", edge.To)
		}
		next[edge.From] = edge.To
	}
	if primaryEdges == 0 {
		return serviceID, nil
	}

	roots := make([]string, 0, 1)
	terminals := make([]string, 0, 1)
	for id := range primaryNodes {
		if incoming[id] == 0 {
			roots = append(roots, id)
		}
		if outgoing[id] == 0 {
			terminals = append(terminals, id)
		}
	}
	if len(roots) != 1 {
		return "", fmt.Errorf("primary path has %d primary roots, want 1", len(roots))
	}
	if outgoing[serviceID] != 0 {
		return "", fmt.Errorf("service node %q is not the primary terminal node", serviceID)
	}
	if len(terminals) != 1 || terminals[0] != serviceID {
		return "", fmt.Errorf("primary terminal node is not service %q", serviceID)
	}

	visited := make(map[string]struct{}, len(primaryNodes))
	for current := roots[0]; current != ""; current = next[current] {
		if _, ok := visited[current]; ok {
			return "", fmt.Errorf("primary path contains a cycle at %q", current)
		}
		visited[current] = struct{}{}
	}
	if len(visited) != len(primaryNodes) {
		return "", fmt.Errorf("primary path is disconnected: reached %d of %d nodes", len(visited), len(primaryNodes))
	}
	return serviceID, nil
}

func deriveNodeHealth(model *Model, serviceID string) {
	for i := range model.Orchestration.Nodes {
		node := &model.Orchestration.Nodes[i]
		if node.ID == serviceID {
			switch model.Summary.RuntimeState {
			case RuntimeFailed:
				node.Health = HealthFailed
			case RuntimeUnknown:
				node.Health = HealthUnknown
			default:
				node.Health = HealthHealthy
			}
			continue
		}
		if node.Health == HealthFailed {
			node.Health = HealthDegraded
		}
	}
}

func aggregateHealth(model *Model, serviceID string) {
	orchestration := HealthHealthy
	aggregate := HealthHealthy
	for _, node := range model.Orchestration.Nodes {
		if node.ID == serviceID {
			aggregate = moreSevere(aggregate, node.Health)
			continue
		}
		orchestration = moreSevere(orchestration, node.Health)
		aggregate = moreSevere(aggregate, node.Health)
	}
	for _, diagnostic := range model.Diagnostics {
		if !diagnostic.AffectsHealth {
			continue
		}
		health := diagnosticHealth(diagnostic)
		if diagnostic.Domain == DomainOrchestration {
			orchestration = moreSevere(orchestration, health)
		}
		if diagnostic.Severity != SeverityFailed {
			aggregate = moreSevere(aggregate, health)
		}
	}
	model.Summary.OrchestrationHealth = orchestration
	model.Summary.AggregateHealth = aggregate
	model.Summary.DisplayState = displayState(model.Summary.RuntimeState, aggregate)
}

func diagnosticHealth(diagnostic Diagnostic) Health {
	switch diagnostic.Severity {
	case SeverityFailed:
		return HealthFailed
	case SeverityDegraded:
		return HealthDegraded
	case SeverityUnknown:
		return HealthUnknown
	default:
		return HealthHealthy
	}
}

func displayState(runtime RuntimeState, aggregate Health) string {
	if runtime == RuntimeFailed {
		return string(RuntimeFailed)
	}
	if aggregate == HealthHealthy {
		return string(runtime)
	}
	return fmt.Sprintf("%s (%s)", runtime, aggregate)
}

func moreSevere(left, right Health) Health {
	if healthRank(right) > healthRank(left) {
		return right
	}
	return left
}

func healthRank(health Health) int {
	switch health {
	case HealthFailed:
		return 3
	case HealthDegraded:
		return 2
	case HealthUnknown:
		return 1
	default:
		return 0
	}
}

func sortGraph(graph *Graph) {
	sort.SliceStable(graph.Nodes, func(i, j int) bool {
		left, right := graph.Nodes[i], graph.Nodes[j]
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if left.Scope != right.Scope {
			return left.Scope < right.Scope
		}
		return left.ID < right.ID
	})
	nodeByID := make(map[string]Node, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodeByID[node.ID] = node
	}
	sort.SliceStable(graph.Edges, func(i, j int) bool {
		left, right := graph.Edges[i], graph.Edges[j]
		if left.From != right.From {
			return left.From < right.From
		}
		if left.Relation != right.Relation {
			return left.Relation < right.Relation
		}
		leftTarget, rightTarget := nodeByID[left.To], nodeByID[right.To]
		if leftTarget.Type != rightTarget.Type {
			return leftTarget.Type < rightTarget.Type
		}
		if leftTarget.Scope != rightTarget.Scope {
			return leftTarget.Scope < rightTarget.Scope
		}
		if left.To != right.To {
			return left.To < right.To
		}
		if left.Primary != right.Primary {
			return left.Primary
		}
		return false
	})
}

func cloneModel(in Model) Model {
	out := in
	out.Orchestration.Nodes = make([]Node, len(in.Orchestration.Nodes))
	for i, node := range in.Orchestration.Nodes {
		out.Orchestration.Nodes[i] = node
		out.Orchestration.Nodes[i].Evidence = append([]Evidence(nil), node.Evidence...)
		out.Orchestration.Nodes[i].ChildPIDs = append([]int(nil), node.ChildPIDs...)
	}
	out.Orchestration.Edges = make([]Edge, len(in.Orchestration.Edges))
	copy(out.Orchestration.Edges, in.Orchestration.Edges)
	out.Diagnostics = make([]Diagnostic, len(in.Diagnostics))
	copy(out.Diagnostics, in.Diagnostics)
	out.Logs = make([]LogEntry, len(in.Logs))
	copy(out.Logs, in.Logs)
	return out
}
