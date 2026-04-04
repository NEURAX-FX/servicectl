package main

import (
	"os"
	"sort"
	"strings"
)

func includeDependencyName(dep string) bool {
	clean := strings.TrimSpace(dep)
	if clean == "" {
		return false
	}
	if strings.HasSuffix(clean, ".target") || strings.HasSuffix(clean, ".socket") {
		return false
	}
	return true
}

func startDependencies(u *Unit) []string {
	deps := make([]string, 0, len(u.Requires)+len(u.Wants)+len(u.After)+len(u.BindsTo)+len(u.PartOf))
	seen := make(map[string]bool)
	all := make([]string, 0, len(u.Requires)+len(u.Wants)+len(u.After)+len(u.BindsTo)+len(u.PartOf))
	all = append(all, u.Requires...)
	all = append(all, u.Wants...)
	all = append(all, u.After...)
	all = append(all, u.BindsTo...)
	all = append(all, u.PartOf...)
	for _, dep := range all {
		clean := strings.TrimSpace(dep)
		if !includeDependencyName(clean) || seen[clean] {
			continue
		}
		seen[clean] = true
		deps = append(deps, clean)
	}
	return deps
}

func formatList(values []string) string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			filtered = append(filtered, value)
		}
	}
	if len(filtered) == 0 {
		return "-"
	}
	return strings.Join(filtered, ", ")
}

func normalizeServiceName(name string) string {
	clean := strings.TrimSuffix(strings.TrimSpace(name), ".service")
	if clean == "" {
		return ""
	}
	return strings.TrimSuffix(resolveUnitAlias(clean), ".service")
}

func listServiceUnitNames() []string {
	seen := make(map[string]bool)
	units := make([]string, 0)
	for _, dir := range config.SystemdPaths {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".service") {
				continue
			}
			name := normalizeServiceName(entry.Name())
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			units = append(units, name)
		}
	}
	sort.Strings(units)
	return units
}

func reverseRestartDependencies(u *Unit) []string {
	deps := make([]string, 0, len(u.Requires)+len(u.Wants)+len(u.After)+len(u.BindsTo)+len(u.PartOf))
	seen := make(map[string]bool)
	all := make([]string, 0, len(u.Requires)+len(u.Wants)+len(u.After)+len(u.BindsTo)+len(u.PartOf))
	all = append(all, u.Requires...)
	all = append(all, u.Wants...)
	all = append(all, u.After...)
	all = append(all, u.BindsTo...)
	all = append(all, u.PartOf...)
	for _, dep := range all {
		name := normalizeServiceName(dep)
		if name == "" || !includeDependencyName(name) || seen[name] {
			continue
		}
		seen[name] = true
		deps = append(deps, name)
	}
	return deps
}

func reverseRestartClosure(unitName string) []string {
	target := normalizeServiceName(unitName)
	dependentsByUnit := make(map[string][]string)
	for _, candidate := range listServiceUnitNames() {
		if candidate == target {
			continue
		}
		unit, err := parseSystemdUnit(candidate)
		if err != nil {
			continue
		}
		for _, dep := range reverseRestartDependencies(unit) {
			dependentsByUnit[dep] = append(dependentsByUnit[dep], candidate)
		}
	}
	for key := range dependentsByUnit {
		sort.Strings(dependentsByUnit[key])
	}
	visited := make(map[string]bool)
	order := make([]string, 0)
	var visit func(string)
	visit = func(name string) {
		for _, dependent := range dependentsByUnit[name] {
			if visited[dependent] || !isUnitStarted(dependent) {
				continue
			}
			visited[dependent] = true
			visit(dependent)
			order = append(order, dependent)
		}
	}
	visit(target)
	return order
}

func reverseStrings(values []string) []string {
	result := make([]string, len(values))
	for i := range values {
		result[len(values)-1-i] = values[i]
	}
	return result
}
