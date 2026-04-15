package main

import (
	"fmt"
	"sort"
	"strings"
)

type enabledRootSet struct {
	Standalone []string
	Groups     map[string][]string
}

type serviceDAG struct {
	nodes       []string
	depsByNode  map[string]map[string]bool
	orderHint   map[string]int
	ownerByUnit map[string]string
}

func normalizeServiceUnitName(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, ".target") || strings.HasSuffix(value, ".socket") {
		return ""
	}
	value = strings.TrimSuffix(value, ".service")
	value = strings.TrimSuffix(resolveUnitAlias(value), ".service")
	if value == "" {
		return ""
	}
	return value + ".service"
}

func buildEnabledServiceDAG(roots enabledRootSet, lookup func(string) (*Unit, error)) (*serviceDAG, error) {
	g := &serviceDAG{
		nodes:       make([]string, 0),
		depsByNode:  make(map[string]map[string]bool),
		orderHint:   make(map[string]int),
		ownerByUnit: make(map[string]string),
	}
	seenNode := make(map[string]bool)
	queue := make([]string, 0)
	markNode := func(name string) {
		if name == "" || seenNode[name] {
			return
		}
		seenNode[name] = true
		g.nodes = append(g.nodes, name)
		queue = append(queue, name)
	}

	orderCounter := 0
	for _, raw := range roots.Standalone {
		service := normalizeServiceUnitName(raw)
		if service == "" {
			continue
		}
		if _, ok := g.orderHint[service]; !ok {
			g.orderHint[service] = orderCounter
			orderCounter++
		}
		g.ownerByUnit[service] = s6OrchestrdServiceName(service)
		markNode(service)
	}

	groupNames := make([]string, 0, len(roots.Groups))
	for name := range roots.Groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	for _, group := range groupNames {
		owner := s6GroupOrchestrdServiceName(group)
		for _, raw := range roots.Groups[group] {
			service := normalizeServiceUnitName(raw)
			if service == "" {
				continue
			}
			if _, ok := g.orderHint[service]; !ok {
				g.orderHint[service] = orderCounter
				orderCounter++
			}
			if _, hasStandaloneOwner := g.ownerByUnit[service]; !hasStandaloneOwner {
				g.ownerByUnit[service] = owner
			}
			markNode(service)
		}
	}

	for idx := 0; idx < len(queue); idx++ {
		name := queue[idx]
		unit, err := lookup(name)
		if err != nil {
			if isUnitNotFoundError(err) {
				continue
			}
			return nil, err
		}
		if unit == nil {
			continue
		}
		deps := looseDependencies(unit)
		if len(deps) == 0 {
			continue
		}
		if g.depsByNode[name] == nil {
			g.depsByNode[name] = make(map[string]bool)
		}
		for _, depRaw := range deps {
			dep := normalizeServiceUnitName(depRaw)
			if dep == "" || dep == name {
				continue
			}
			g.depsByNode[name][dep] = true
			if _, ok := g.orderHint[dep]; !ok {
				g.orderHint[dep] = orderCounter
				orderCounter++
			}
			markNode(dep)
		}
	}

	return g, nil
}

func isUnitNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(err.Error())), "not found")
}

func looseDependencies(unit *Unit) []string {
	deps := make([]string, 0, len(unit.Requires)+len(unit.Wants)+len(unit.After)+len(unit.BindsTo)+len(unit.PartOf))
	deps = append(deps, unit.Requires...)
	deps = append(deps, unit.Wants...)
	deps = append(deps, unit.After...)
	deps = append(deps, unit.BindsTo...)
	deps = append(deps, unit.PartOf...)
	return deps
}

func (g *serviceDAG) OwnerOf(unit string) string {
	return g.ownerByUnit[normalizeServiceUnitName(unit)]
}

func (g *serviceDAG) TopologicalServices() ([]string, error) {
	mark := make(map[string]uint8, len(g.nodes))
	result := make([]string, 0, len(g.nodes))
	nodes := append([]string{}, g.nodes...)
	sort.Slice(nodes, func(i, j int) bool {
		return compareByHint(g.orderHint, nodes[i], nodes[j]) < 0
	})
	var visit func(string) error
	visit = func(name string) error {
		switch mark[name] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("cycle detected at %s", name)
		}
		mark[name] = 1
		deps := make([]string, 0, len(g.depsByNode[name]))
		for dep := range g.depsByNode[name] {
			deps = append(deps, dep)
		}
		sort.Slice(deps, func(i, j int) bool {
			return compareByHint(g.orderHint, deps[i], deps[j]) < 0
		})
		for _, dep := range deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		mark[name] = 2
		result = append(result, name)
		return nil
	}
	for _, node := range nodes {
		if err := visit(node); err != nil {
			return nil, err
		}
	}
	return uniqueLinesPreserveOrder(strings.Join(result, "\n")), nil
}

func compareByHint(hint map[string]int, a string, b string) int {
	ha, okA := hint[a]
	hb, okB := hint[b]
	switch {
	case okA && okB && ha != hb:
		if ha < hb {
			return -1
		}
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (g *serviceDAG) ProjectOrchestrdDependencies() map[string]map[string]bool {
	result := make(map[string]map[string]bool)
	for _, owner := range g.ownerByUnit {
		if strings.TrimSpace(owner) == "" {
			continue
		}
		if result[owner] == nil {
			result[owner] = make(map[string]bool)
		}
	}
	for service, deps := range g.depsByNode {
		sourceOwner := g.ownerByUnit[service]
		if sourceOwner == "" {
			continue
		}
		if result[sourceOwner] == nil {
			result[sourceOwner] = make(map[string]bool)
		}
		for dep := range deps {
			depOwner := g.ownerByUnit[dep]
			if depOwner == "" || depOwner == sourceOwner {
				continue
			}
			result[sourceOwner][depOwner] = true
		}
	}
	return result
}

func uniqueLinesPreserveOrder(content string) []string {
	seen := make(map[string]bool)
	entries := make([]string, 0)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		entries = append(entries, line)
	}
	return entries
}
