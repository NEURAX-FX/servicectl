package main

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"servicectl/internal/statusview"
)

const statusWideLayoutWidth = 96

type statusGraphView struct {
	nodes       map[string]statusview.Node
	primaryPath []string
	primaryNext map[string]statusview.Edge
	sides       map[string][]statusSideEdge
}

type statusSideEdge struct {
	key   int
	edge  statusview.Edge
	other string
}

func renderStatusTerminalV2(w io.Writer, model statusview.Model, caps renderCapabilities, verbose bool) error {
	if caps.Width <= 0 {
		caps.Width = 80
	}
	if caps.Width < 20 {
		caps.Width = 20
	}
	graph, err := buildStatusGraphView(model)
	if err != nil {
		return err
	}
	if err := renderStatusSummaryV2(w, model, caps); err != nil {
		return err
	}
	if err := writeStatusSectionHeading(w, "ORCHESTRATION", caps); err != nil {
		return err
	}
	if caps.Width >= statusWideLayoutWidth {
		err = renderStatusGraphWide(w, graph, caps, verbose)
	} else {
		err = renderStatusGraphNarrow(w, graph, caps, verbose)
	}
	if err != nil {
		return err
	}
	if len(model.Diagnostics) > 0 {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		if err := writeStatusSectionHeading(w, "DIAGNOSTICS", caps); err != nil {
			return err
		}
		for _, diagnostic := range model.Diagnostics {
			prefix := "! "
			if diagnostic.Severity == statusview.SeverityInfo {
				prefix = "- "
			}
			if err := writeStatusWrapped(w, prefix, diagnostic.Message, caps.Width, caps); err != nil {
				return err
			}
			if diagnostic.Hint != "" {
				if err := writeStatusWrapped(w, "  Hint: ", diagnostic.Hint, caps.Width, caps); err != nil {
					return err
				}
			}
			if verbose {
				detail := "code=" + diagnostic.Code + " · domain=" + string(diagnostic.Domain) + " · observed=" + formatStatusTimestamp(diagnostic.ObservedAt)
				if diagnostic.Source != "" {
					detail += " · source=" + diagnostic.Source
				}
				if err := writeStatusWrapped(w, "  ", detail, caps.Width, caps); err != nil {
					return err
				}
			}
		}
	}
	logs := model.Logs
	if !verbose {
		logs = selectDefaultStatusLogs(logs)
	}
	if len(logs) > 0 || hasStatusLogDiagnostic(model.Diagnostics) {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		if err := writeStatusSectionHeading(w, "RECENT LOGS", caps); err != nil {
			return err
		}
		for _, entry := range logs {
			prefix := entry.Timestamp.UTC().Format("15:04:05") + " "
			if verbose || entry.Severity == statusview.LogError || entry.Severity == statusview.LogCritical {
				prefix += string(entry.Severity) + " "
			}
			if err := writeStatusWrapped(w, prefix, entry.Message, caps.Width, caps); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderStatusPlainV2(w io.Writer, model statusview.Model, verbose bool) error {
	graph, err := buildStatusGraphView(model)
	if err != nil {
		return err
	}
	title := model.Identity.Unit
	if model.Identity.Description != "" {
		title += " - " + model.Identity.Description
	}
	if _, err := fmt.Fprintln(w, title); err != nil {
		return err
	}
	metadata := strings.ReplaceAll(compactStatusParts(model.Summary.EnabledState, model.Identity.Scope, model.Identity.Type), " · ", " - ")
	if _, err := fmt.Fprintln(w, model.Summary.DisplayState+"  "+metadata); err != nil {
		return err
	}
	process := make([]string, 0, 2)
	if model.Summary.MainPID > 0 {
		process = append(process, "PID "+strconv.Itoa(model.Summary.MainPID))
	}
	if model.Summary.ActiveDurationSeconds > 0 {
		process = append(process, "running "+formatStatusDuration(model.Summary.ActiveDurationSeconds))
	}
	if len(process) > 0 {
		if _, err := fmt.Fprintln(w, strings.Join(process, " | ")); err != nil {
			return err
		}
	}
	if model.Identity.SourcePath != "" {
		if _, err := fmt.Fprintln(w, model.Identity.SourcePath); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, "\nORCHESTRATION"); err != nil {
		return err
	}
	visitedNodes := make(map[string]bool, len(graph.nodes))
	renderedEdges := make(map[int]bool)
	for _, id := range graph.primaryPath {
		visitedNodes[id] = true
	}
	for _, id := range graph.primaryPath {
		node := graph.nodes[id]
		if _, err := fmt.Fprintln(w, statusPlainName(node.Name)); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, statusNodeStateASCII(node)); err != nil {
			return err
		}
		if verbose {
			if err := writeStatusNodeDetailsPlain(w, node, "  "); err != nil {
				return err
			}
		}
		if err := renderStatusSideEdgesPlain(w, graph, id, "", verbose, visitedNodes, renderedEdges); err != nil {
			return err
		}
		if edge, ok := graph.primaryNext[id]; ok {
			if _, err := fmt.Fprintf(w, "v %s\n", edge.Relation); err != nil {
				return err
			}
		}
	}
	for _, id := range sortedStatusNodeIDs(graph.nodes) {
		if visitedNodes[id] {
			continue
		}
		visitedNodes[id] = true
		if _, err := fmt.Fprintln(w, "`- detached"); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "   "+statusPlainName(graph.nodes[id].Name)); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "   "+statusNodeStateASCII(graph.nodes[id])); err != nil {
			return err
		}
		if err := renderStatusSideEdgesPlain(w, graph, id, "   ", verbose, visitedNodes, renderedEdges); err != nil {
			return err
		}
	}
	if len(model.Diagnostics) > 0 {
		if _, err := fmt.Fprintln(w, "\nDIAGNOSTICS"); err != nil {
			return err
		}
		for _, diagnostic := range model.Diagnostics {
			if _, err := fmt.Fprintln(w, "! "+diagnostic.Message); err != nil {
				return err
			}
			if diagnostic.Hint != "" {
				if _, err := fmt.Fprintln(w, "  Hint: "+diagnostic.Hint); err != nil {
					return err
				}
			}
			if verbose {
				detail := "  code=" + diagnostic.Code + " | domain=" + string(diagnostic.Domain) + " | observed=" + formatStatusTimestamp(diagnostic.ObservedAt)
				if diagnostic.Source != "" {
					detail += " | source=" + diagnostic.Source
				}
				if _, err := fmt.Fprintln(w, detail); err != nil {
					return err
				}
			}
		}
	}
	logs := model.Logs
	if !verbose {
		logs = selectDefaultStatusLogs(logs)
	}
	if len(logs) > 0 || hasStatusLogDiagnostic(model.Diagnostics) {
		if _, err := fmt.Fprintln(w, "\nRECENT LOGS"); err != nil {
			return err
		}
		for _, entry := range logs {
			prefix := entry.Timestamp.UTC().Format("15:04:05") + " "
			if verbose || entry.Severity == statusview.LogError || entry.Severity == statusview.LogCritical {
				prefix += string(entry.Severity) + " "
			}
			if _, err := fmt.Fprintln(w, prefix+entry.Message); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderStatusSideEdgesPlain(w io.Writer, graph statusGraphView, id, indent string, verbose bool, visitedNodes map[string]bool, renderedEdges map[int]bool) error {
	edges := unrenderedStatusSideEdges(graph.sides[id], renderedEdges)
	for index, side := range edges {
		renderedEdges[side.key] = true
		branch := "+-"
		childIndent := indent + "|  "
		if index == len(edges)-1 {
			branch = "`-"
			childIndent = indent + "   "
		}
		other := graph.nodes[side.other]
		if _, err := fmt.Fprintf(w, "%s%s %s\n", indent, branch, side.edge.Relation); err != nil {
			return err
		}
		if visitedNodes[other.ID] {
			if _, err := fmt.Fprintf(w, "%s(see %s)\n", childIndent, statusPlainName(other.Name)); err != nil {
				return err
			}
			continue
		}
		visitedNodes[other.ID] = true
		if _, err := fmt.Fprintln(w, childIndent+statusPlainName(other.Name)); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, childIndent+statusNodeStateASCII(other)); err != nil {
			return err
		}
		if verbose {
			if err := writeStatusNodeDetailsPlain(w, other, childIndent+"  "); err != nil {
				return err
			}
		}
		if err := renderStatusSideEdgesPlain(w, graph, other.ID, childIndent, verbose, visitedNodes, renderedEdges); err != nil {
			return err
		}
	}
	return nil
}

func renderStatusSummaryV2(w io.Writer, model statusview.Model, caps renderCapabilities) error {
	title := model.Identity.Unit
	if model.Identity.Description != "" {
		title += " · " + model.Identity.Description
	}
	if err := writeStatusWrapped(w, "", title, caps.Width, renderCapabilities{Width: caps.Width}); err != nil {
		return err
	}
	stateLine := "● " + model.Summary.DisplayState
	metadata := compactStatusParts(model.Summary.EnabledState, model.Identity.Scope, model.Identity.Type)
	if metadata != "" {
		stateLine += "    " + metadata
	}
	if err := writeStatusSummaryState(w, stateLine, model.Summary.AggregateHealth, caps); err != nil {
		return err
	}
	process := make([]string, 0, 2)
	if model.Summary.MainPID > 0 {
		process = append(process, "PID "+strconv.Itoa(model.Summary.MainPID))
	}
	if model.Summary.ActiveDurationSeconds > 0 {
		process = append(process, "running "+formatStatusDuration(model.Summary.ActiveDurationSeconds))
	}
	if len(process) > 0 {
		if err := writeStatusWrapped(w, "", strings.Join(process, " · "), caps.Width, caps); err != nil {
			return err
		}
	}
	if model.Identity.SourcePath != "" {
		if err := writeStatusWrapped(w, "", model.Identity.SourcePath, caps.Width, renderCapabilities{Width: caps.Width}); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

func buildStatusGraphView(model statusview.Model) (statusGraphView, error) {
	view := statusGraphView{
		nodes:       make(map[string]statusview.Node, len(model.Orchestration.Nodes)),
		primaryNext: make(map[string]statusview.Edge),
		sides:       make(map[string][]statusSideEdge),
	}
	serviceID := ""
	incomingPrimary := make(map[string]int)
	primaryNodes := make(map[string]bool)
	for _, node := range model.Orchestration.Nodes {
		view.nodes[node.ID] = node
		if node.Type == "service" {
			serviceID = node.ID
		}
	}
	if serviceID == "" {
		return statusGraphView{}, fmt.Errorf("status topology has no service node")
	}
	for edgeIndex, edge := range model.Orchestration.Edges {
		if _, ok := view.nodes[edge.From]; !ok {
			return statusGraphView{}, fmt.Errorf("status topology edge references missing node %q", edge.From)
		}
		if _, ok := view.nodes[edge.To]; !ok {
			return statusGraphView{}, fmt.Errorf("status topology edge references missing node %q", edge.To)
		}
		if edge.Primary {
			view.primaryNext[edge.From] = edge
			incomingPrimary[edge.To]++
			primaryNodes[edge.From] = true
			primaryNodes[edge.To] = true
			continue
		}
		view.sides[edge.From] = append(view.sides[edge.From], statusSideEdge{key: edgeIndex, edge: edge, other: edge.To})
		if edge.To != edge.From {
			view.sides[edge.To] = append(view.sides[edge.To], statusSideEdge{key: edgeIndex, edge: edge, other: edge.From})
		}
	}
	root := serviceID
	if len(primaryNodes) > 0 {
		root = ""
		for id := range primaryNodes {
			if incomingPrimary[id] == 0 {
				root = id
				break
			}
		}
		if root == "" {
			return statusGraphView{}, fmt.Errorf("status topology primary path has no root")
		}
	}
	seen := make(map[string]bool)
	for current := root; current != ""; {
		if seen[current] {
			return statusGraphView{}, fmt.Errorf("status topology primary path contains a cycle")
		}
		seen[current] = true
		view.primaryPath = append(view.primaryPath, current)
		edge, ok := view.primaryNext[current]
		if !ok {
			break
		}
		current = edge.To
	}
	if len(view.primaryPath) == 0 || view.primaryPath[len(view.primaryPath)-1] != serviceID {
		return statusGraphView{}, fmt.Errorf("status topology primary path does not end at service")
	}
	for anchor, edges := range view.sides {
		sort.SliceStable(edges, func(i, j int) bool {
			if edges[i].edge.Relation != edges[j].edge.Relation {
				return edges[i].edge.Relation < edges[j].edge.Relation
			}
			left, right := view.nodes[edges[i].other], view.nodes[edges[j].other]
			if left.Type != right.Type {
				return left.Type < right.Type
			}
			return left.ID < right.ID
		})
		view.sides[anchor] = edges
	}
	return view, nil
}

func renderStatusGraphWide(w io.Writer, graph statusGraphView, caps renderCapabilities, verbose bool) error {
	visitedNodes := make(map[string]bool, len(graph.nodes))
	renderedEdges := make(map[int]bool)
	for _, id := range graph.primaryPath {
		visitedNodes[id] = true
	}
	for _, id := range graph.primaryPath {
		node := graph.nodes[id]
		if err := writeStatusNodeWide(w, node, caps, verbose, ""); err != nil {
			return err
		}
		if err := renderStatusSideEdgesWide(w, graph, id, "    ", caps, verbose, visitedNodes, renderedEdges); err != nil {
			return err
		}
		if edge, ok := graph.primaryNext[id]; ok {
			if err := writeStatusWrapped(w, "    │ ", string(edge.Relation), caps.Width, caps); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(w, "    ▼"); err != nil {
				return err
			}
		}
	}
	for _, id := range sortedStatusNodeIDs(graph.nodes) {
		if visitedNodes[id] {
			continue
		}
		visitedNodes[id] = true
		if err := writeStatusWrapped(w, "    └── ", "detached ── "+statusNodeSummary(graph.nodes[id]), caps.Width, caps); err != nil {
			return err
		}
		if err := renderStatusSideEdgesWide(w, graph, id, "        ", caps, verbose, visitedNodes, renderedEdges); err != nil {
			return err
		}
	}
	return nil
}

func renderStatusGraphNarrow(w io.Writer, graph statusGraphView, caps renderCapabilities, verbose bool) error {
	visitedNodes := make(map[string]bool, len(graph.nodes))
	renderedEdges := make(map[int]bool)
	for _, id := range graph.primaryPath {
		visitedNodes[id] = true
	}
	for _, id := range graph.primaryPath {
		node := graph.nodes[id]
		if err := writeStatusWrapped(w, "", node.Name, caps.Width, caps); err != nil {
			return err
		}
		if err := writeStatusWrapped(w, "", statusNodeState(node), caps.Width, caps); err != nil {
			return err
		}
		if verbose {
			if err := writeStatusNodeDetails(w, node, caps, "  "); err != nil {
				return err
			}
		}
		if err := renderStatusSideEdgesNarrow(w, graph, id, "", caps, verbose, visitedNodes, renderedEdges); err != nil {
			return err
		}
		if edge, ok := graph.primaryNext[id]; ok {
			if err := writeStatusWrapped(w, "▼ ", string(edge.Relation), caps.Width, caps); err != nil {
				return err
			}
		}
	}
	for _, id := range sortedStatusNodeIDs(graph.nodes) {
		if visitedNodes[id] {
			continue
		}
		visitedNodes[id] = true
		if err := writeStatusWrapped(w, "└─ ", "detached", caps.Width, caps); err != nil {
			return err
		}
		if err := writeStatusWrapped(w, "   ", graph.nodes[id].Name, caps.Width, caps); err != nil {
			return err
		}
		if err := writeStatusWrapped(w, "   ", statusNodeState(graph.nodes[id]), caps.Width, caps); err != nil {
			return err
		}
		if err := renderStatusSideEdgesNarrow(w, graph, id, "   ", caps, verbose, visitedNodes, renderedEdges); err != nil {
			return err
		}
	}
	return nil
}

func renderStatusSideEdgesWide(w io.Writer, graph statusGraphView, id, indent string, caps renderCapabilities, verbose bool, visitedNodes map[string]bool, renderedEdges map[int]bool) error {
	edges := unrenderedStatusSideEdges(graph.sides[id], renderedEdges)
	for index, side := range edges {
		renderedEdges[side.key] = true
		branch := "├──"
		childIndent := indent + "│   "
		if index == len(edges)-1 {
			branch = "└──"
			childIndent = indent + "    "
		}
		other := graph.nodes[side.other]
		text := string(side.edge.Relation) + " ── "
		if visitedNodes[other.ID] {
			text += "(see " + other.Name + ")"
			if err := writeStatusWrapped(w, indent+branch+" ", text, caps.Width, caps); err != nil {
				return err
			}
			continue
		}
		visitedNodes[other.ID] = true
		if err := writeStatusWrapped(w, indent+branch+" ", text+statusNodeSummary(other), caps.Width, caps); err != nil {
			return err
		}
		if verbose {
			if err := writeStatusNodeDetails(w, other, caps, childIndent); err != nil {
				return err
			}
		}
		if err := renderStatusSideEdgesWide(w, graph, other.ID, childIndent, caps, verbose, visitedNodes, renderedEdges); err != nil {
			return err
		}
	}
	return nil
}

func renderStatusSideEdgesNarrow(w io.Writer, graph statusGraphView, id, indent string, caps renderCapabilities, verbose bool, visitedNodes map[string]bool, renderedEdges map[int]bool) error {
	edges := unrenderedStatusSideEdges(graph.sides[id], renderedEdges)
	for index, side := range edges {
		renderedEdges[side.key] = true
		branch := "├─"
		childIndent := indent + "│  "
		if index == len(edges)-1 {
			branch = "└─"
			childIndent = indent + "   "
		}
		other := graph.nodes[side.other]
		if err := writeStatusWrapped(w, indent+branch+" ", string(side.edge.Relation), caps.Width, caps); err != nil {
			return err
		}
		if visitedNodes[other.ID] {
			if err := writeStatusWrapped(w, childIndent, "(see "+other.Name+")", caps.Width, caps); err != nil {
				return err
			}
			continue
		}
		visitedNodes[other.ID] = true
		if err := writeStatusWrapped(w, childIndent, other.Name, caps.Width, caps); err != nil {
			return err
		}
		if err := writeStatusWrapped(w, childIndent, statusNodeState(other), caps.Width, caps); err != nil {
			return err
		}
		if verbose {
			if err := writeStatusNodeDetails(w, other, caps, childIndent+"  "); err != nil {
				return err
			}
		}
		if err := renderStatusSideEdgesNarrow(w, graph, other.ID, childIndent, caps, verbose, visitedNodes, renderedEdges); err != nil {
			return err
		}
	}
	return nil
}

func unrenderedStatusSideEdges(edges []statusSideEdge, rendered map[int]bool) []statusSideEdge {
	result := make([]statusSideEdge, 0, len(edges))
	for _, edge := range edges {
		if !rendered[edge.key] {
			result = append(result, edge)
		}
	}
	return result
}

func sortedStatusNodeIDs(nodes map[string]statusview.Node) []string {
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func writeStatusNodeWide(w io.Writer, node statusview.Node, caps renderCapabilities, verbose bool, prefix string) error {
	left := prefix + node.Name
	right := statusNodeState(node)
	space := caps.Width - visibleWidth(left) - visibleWidth(right)
	line := left + "  " + right
	if space >= 2 {
		line = left + strings.Repeat(" ", space) + right
	}
	if err := writeStatusWrapped(w, "", line, caps.Width, caps); err != nil {
		return err
	}
	if verbose {
		return writeStatusNodeDetails(w, node, caps, "  ")
	}
	return nil
}

func writeStatusNodeDetails(w io.Writer, node statusview.Node, caps renderCapabilities, prefix string) error {
	parts := make([]string, 0, 6)
	if node.Endpoint != "" {
		parts = append(parts, "endpoint="+node.Endpoint)
	}
	if node.BusOwner != "" {
		parts = append(parts, "owner="+node.BusOwner)
	}
	if node.CgroupPath != "" {
		parts = append(parts, "cgroup="+node.CgroupPath)
	}
	if node.ManagerPID > 0 {
		parts = append(parts, "manager_pid="+strconv.Itoa(node.ManagerPID))
	}
	if !node.ObservedAt.IsZero() {
		parts = append(parts, "observed="+formatStatusTimestamp(node.ObservedAt))
	}
	if len(parts) > 0 {
		if err := writeStatusWrapped(w, prefix, strings.Join(parts, " · "), caps.Width, caps); err != nil {
			return err
		}
	}
	for _, evidence := range node.Evidence {
		detail := "evidence " + string(evidence.Source) + "=" + string(evidence.Result) + " checked=" + formatStatusTimestamp(evidence.CheckedAt)
		if evidence.SourceObservedAt != nil {
			detail += " source_observed=" + formatStatusTimestamp(*evidence.SourceObservedAt)
		}
		if evidence.Detail != "" {
			detail += " · " + evidence.Detail
		}
		if err := writeStatusWrapped(w, prefix, detail, caps.Width, caps); err != nil {
			return err
		}
	}
	return nil
}

func writeStatusNodeDetailsPlain(w io.Writer, node statusview.Node, prefix string) error {
	parts := make([]string, 0, 6)
	if node.Endpoint != "" {
		parts = append(parts, "endpoint="+node.Endpoint)
	}
	if node.BusOwner != "" {
		parts = append(parts, "owner="+node.BusOwner)
	}
	if node.CgroupPath != "" {
		parts = append(parts, "cgroup="+node.CgroupPath)
	}
	if node.ManagerPID > 0 {
		parts = append(parts, "manager_pid="+strconv.Itoa(node.ManagerPID))
	}
	if !node.ObservedAt.IsZero() {
		parts = append(parts, "observed="+formatStatusTimestamp(node.ObservedAt))
	}
	if len(parts) > 0 {
		if _, err := fmt.Fprintln(w, prefix+strings.Join(parts, " | ")); err != nil {
			return err
		}
	}
	for _, evidence := range node.Evidence {
		detail := "evidence " + string(evidence.Source) + "=" + string(evidence.Result) + " checked=" + formatStatusTimestamp(evidence.CheckedAt)
		if evidence.SourceObservedAt != nil {
			detail += " source_observed=" + formatStatusTimestamp(*evidence.SourceObservedAt)
		}
		if evidence.Detail != "" {
			detail += " | " + evidence.Detail
		}
		if _, err := fmt.Fprintln(w, prefix+detail); err != nil {
			return err
		}
	}
	return nil
}

func statusNodeSummary(node statusview.Node) string {
	return node.Name + "  " + statusNodeState(node)
}

func statusNodeState(node statusview.Node) string {
	parts := []string{string(node.Health)}
	if node.State != "" && node.State != string(node.Health) {
		parts = append(parts, node.State)
	}
	if node.PID > 0 {
		parts = append(parts, "pid "+strconv.Itoa(node.PID))
	}
	if node.BusOwner != "" {
		parts = append(parts, "owner "+node.BusOwner)
	}
	if node.CgroupPath != "" {
		parts = append(parts, node.CgroupPath)
	}
	return strings.Join(parts, " · ")
}

func statusNodeStateASCII(node statusview.Node) string {
	return strings.ReplaceAll(statusNodeState(node), " · ", " | ")
}

func statusPlainName(name string) string {
	return strings.ReplaceAll(name, " · ", " - ")
}

func writeStatusSectionHeading(w io.Writer, heading string, caps renderCapabilities) error {
	if !caps.Color {
		_, err := fmt.Fprintln(w, heading)
		return err
	}
	width := caps.Width
	if width <= 0 {
		width = 80
	}
	line := heading
	if remaining := width - visibleWidth(heading) - 1; remaining > 0 {
		line += " " + strings.Repeat("┄", remaining)
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

func writeStatusWrapped(w io.Writer, prefix, text string, width int, caps renderCapabilities) error {
	return writeStatusWrappedStyled(w, prefix, text, width, caps, nil)
}

func writeStatusSummaryState(w io.Writer, text string, health statusview.Health, caps renderCapabilities) error {
	lines := wrapStatusText("", text, caps.Width)
	for index, line := range lines {
		if index == 0 {
			line = styleText("●", caps, statusHealthStyle(health)) + strings.TrimPrefix(line, "●")
		}
		line = styleStatusSemanticText(line, caps)
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

func statusHealthStyle(health statusview.Health) ansiStyle {
	switch health {
	case statusview.HealthFailed:
		return styleRed
	case statusview.HealthDegraded:
		return styleYellow
	case statusview.HealthUnknown:
		return styleGray
	default:
		return styleGreen
	}
}

func writeStatusWrappedStyled(w io.Writer, prefix, text string, width int, caps renderCapabilities, decorate func(string) string) error {
	lines := wrapStatusText(prefix, text, width)
	for _, line := range lines {
		if decorate != nil {
			line = decorate(line)
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

func wrapStatusText(prefix, text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	continuation := strings.Repeat(" ", visibleWidth(prefix))
	paragraphs := strings.Split(text, "\n")
	lines := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, prefix)
			continue
		}
		currentPrefix := prefix
		current := ""
		for len(words) > 0 {
			word := words[0]
			words = words[1:]
			available := width - visibleWidth(currentPrefix)
			if available < 1 {
				available = 1
			}
			candidate := word
			if current != "" {
				candidate = current + " " + word
			}
			if visibleWidth(candidate) <= available {
				current = candidate
				continue
			}
			if current != "" {
				lines = append(lines, currentPrefix+current)
				currentPrefix = continuation
				current = ""
				words = append([]string{word}, words...)
				continue
			}
			chunks := splitStatusWord(word, available)
			lines = append(lines, currentPrefix+chunks[0])
			currentPrefix = continuation
			if len(chunks) > 1 {
				words = append(chunks[1:], words...)
			}
		}
		if current != "" {
			lines = append(lines, currentPrefix+current)
		}
	}
	return lines
}

func splitStatusWord(word string, width int) []string {
	if width < 1 {
		width = 1
	}
	chunks := make([]string, 0, 1)
	for word != "" {
		used := 0
		end := 0
		for end < len(word) {
			r, size := utf8.DecodeRuneInString(word[end:])
			runeWidth := runeDisplayWidth(r)
			if end > 0 && used+runeWidth > width {
				break
			}
			used += runeWidth
			end += size
			if used >= width {
				break
			}
		}
		if end == 0 {
			_, size := utf8.DecodeRuneInString(word)
			end = size
		}
		chunks = append(chunks, word[:end])
		word = word[end:]
	}
	return chunks
}

func styleStatusSemanticText(text string, caps renderCapabilities) string {
	if !caps.Color {
		return text
	}
	styles := map[string]ansiStyle{
		"failed":       styleRed,
		"degraded":     styleYellow,
		"unknown":      styleGray,
		"healthy":      styleGreen,
		"deactivating": styleYellow,
		"activating":   styleYellow,
		"active":       styleGreen,
	}
	var output strings.Builder
	for start := 0; start < len(text); {
		if !isStatusWordByte(text[start]) {
			output.WriteByte(text[start])
			start++
			continue
		}
		end := start + 1
		for end < len(text) && isStatusWordByte(text[end]) {
			end++
		}
		word := text[start:end]
		if style, ok := styles[word]; ok {
			output.WriteString(styleText(word, caps, style))
		} else {
			output.WriteString(word)
		}
		start = end
	}
	return output.String()
}

func isStatusWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value == '_'
}

func compactStatusParts(parts ...string) string {
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			result = append(result, strings.TrimSpace(part))
		}
	}
	return strings.Join(result, " · ")
}

func formatStatusDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	duration := time.Duration(seconds) * time.Second
	hours := duration / time.Hour
	duration -= hours * time.Hour
	minutes := duration / time.Minute
	duration -= minutes * time.Minute
	secs := duration / time.Second
	parts := make([]string, 0, 3)
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if secs > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", secs))
	}
	return strings.Join(parts, " ")
}

func formatStatusTimestamp(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format("2006-01-02 15:04:05")
}

func hasStatusLogDiagnostic(diagnostics []statusview.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "logs_unavailable" {
			return true
		}
	}
	return false
}
