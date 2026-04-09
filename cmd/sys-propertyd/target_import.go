package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"servicectl/internal/visionapi"
)

type targetDefinition struct {
	Name     string
	Requires []string
	Wants    []string
}

func systemdUnitDirs(mode string) []string {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if strings.EqualFold(strings.TrimSpace(mode), visionapi.ModeUser) {
		return []string{filepath.Join(home, ".config/systemd/user"), "/usr/lib/systemd/user"}
	}
	return []string{"/etc/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"}
}

func importSystemdTargets(unitDirs []string) (map[string]groupDefinition, map[string]string, error) {
	defs, err := loadTargetDefinitions(unitDirs)
	if err != nil {
		return nil, nil, err
	}
	groups := map[string]groupDefinition{}
	targets := map[string]string{}
	for _, name := range sortedTargetNames(defs) {
		units := resolveTargetServices(name, defs, map[string]bool{})
		if len(units) == 0 {
			continue
		}
		groupName := defaultGroupNameForTarget(name)
		groups[groupName] = groupDefinition{Name: groupName, Units: units, Targets: []string{name}}
		targets[name] = groupName
	}
	return groups, targets, nil
}

func loadTargetDefinitions(unitDirs []string) (map[string]targetDefinition, error) {
	defs := map[string]targetDefinition{}
	for _, dir := range unitDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			full := filepath.Join(dir, name)
			if entry.IsDir() {
				if strings.HasSuffix(name, ".target.wants") || strings.HasSuffix(name, ".target.requires") {
					if err := mergeTargetDependencyDir(full, defs); err != nil {
						return nil, err
					}
				}
				continue
			}
			if !strings.HasSuffix(name, ".target") {
				continue
			}
			def, err := parseTargetUnitFile(full)
			if err != nil {
				return nil, err
			}
			mergeTargetDefinition(defs, def)
		}
	}
	return defs, nil
}

func parseTargetUnitFile(path string) (targetDefinition, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return targetDefinition{}, err
	}
	def := targetDefinition{Name: filepath.Base(path)}
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		if section != "Unit" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		values := strings.Fields(strings.TrimSpace(parts[1]))
		switch key {
		case "Requires":
			def.Requires = append(def.Requires, values...)
		case "Wants":
			def.Wants = append(def.Wants, values...)
		}
	}
	if err := scanner.Err(); err != nil {
		return targetDefinition{}, err
	}
	def.Requires = uniqueSortedStrings(def.Requires)
	def.Wants = uniqueSortedStrings(def.Wants)
	return def, nil
}

func mergeTargetDependencyDir(path string, defs map[string]targetDefinition) error {
	base := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".wants"), ".requires")
	if !strings.HasSuffix(base, ".target") {
		return nil
	}
	def := defs[base]
	def.Name = base
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" {
			continue
		}
		if strings.HasSuffix(filepath.Base(path), ".requires") {
			def.Requires = append(def.Requires, name)
		} else {
			def.Wants = append(def.Wants, name)
		}
	}
	def.Requires = uniqueSortedStrings(def.Requires)
	def.Wants = uniqueSortedStrings(def.Wants)
	defs[base] = def
	return nil
}

func mergeTargetDefinition(defs map[string]targetDefinition, def targetDefinition) {
	current := defs[def.Name]
	if current.Name == "" {
		current.Name = def.Name
	}
	current.Requires = uniqueSortedStrings(append(current.Requires, def.Requires...))
	current.Wants = uniqueSortedStrings(append(current.Wants, def.Wants...))
	defs[def.Name] = current
}

func resolveTargetServices(name string, defs map[string]targetDefinition, visited map[string]bool) []string {
	clean := strings.TrimSpace(name)
	if clean == "" || visited[clean] {
		return nil
	}
	visited[clean] = true
	def, ok := defs[clean]
	if !ok {
		return nil
	}
	units := make([]string, 0, len(def.Requires)+len(def.Wants))
	for _, dep := range append(append([]string{}, def.Requires...), def.Wants...) {
		dep = strings.TrimSpace(dep)
		switch {
		case strings.HasSuffix(dep, ".service"):
			units = append(units, dep)
		case strings.HasSuffix(dep, ".target"):
			units = append(units, resolveTargetServices(dep, defs, visited)...)
		}
	}
	return uniqueSortedStrings(units)
}

func defaultGroupNameForTarget(name string) string {
	clean := strings.TrimSpace(strings.TrimSuffix(name, ".target"))
	return clean
}

func sortedTargetNames(defs map[string]targetDefinition) []string {
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func overrideGroups(base map[string]groupDefinition, overrides map[string]groupDefinition) map[string]groupDefinition {
	merged := map[string]groupDefinition{}
	for name, def := range base {
		merged[name] = def
	}
	for name, def := range overrides {
		merged[name] = def
	}
	return merged
}

func overrideTargets(base map[string]string, overrides map[string]string) map[string]string {
	merged := map[string]string{}
	for name, group := range base {
		merged[name] = group
	}
	for name, group := range overrides {
		merged[name] = group
	}
	return merged
}

func validateTargetGroups(groups map[string]groupDefinition, targets map[string]string) error {
	for target, group := range targets {
		if _, ok := groups[group]; ok {
			continue
		}
		return fmt.Errorf("target %s references unknown group %s", target, group)
	}
	return nil
}
