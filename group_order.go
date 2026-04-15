package main

import "fmt"

func resolveGroupStartOrder(group string) ([]string, error) {
	state, ok := queryGroupState(group)
	if !ok {
		return nil, fmt.Errorf("group %s not found", group)
	}
	groupSet := make(map[string]bool, len(state.Units))
	for _, raw := range state.Units {
		if normalized := normalizeServiceUnitName(raw); normalized != "" {
			groupSet[normalized] = true
		}
	}
	roots := enabledRootSetFromCurrentState()
	graph, err := buildEnabledServiceDAG(roots, lookupSystemdUnitForDAG)
	if err != nil {
		return nil, err
	}
	order, err := graph.TopologicalServices()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(groupSet))
	seen := make(map[string]bool, len(groupSet))
	for _, name := range order {
		if groupSet[name] && !seen[name] {
			result = append(result, name)
			seen[name] = true
		}
	}
	for _, raw := range state.Units {
		normalized := normalizeServiceUnitName(raw)
		if normalized == "" || seen[normalized] {
			continue
		}
		result = append(result, normalized)
		seen[normalized] = true
	}
	return result, nil
}
