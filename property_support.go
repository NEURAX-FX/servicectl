package main

import (
	"sort"
	"strings"
)

type virtualTarget struct {
	kind string
	name string
}

func parseVirtualTarget(raw string) (virtualTarget, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return virtualTarget{}, false
	}
	if strings.HasPrefix(value, "group:") {
		name := strings.TrimSpace(strings.TrimPrefix(value, "group:"))
		if name == "" {
			return virtualTarget{}, false
		}
		return virtualTarget{kind: "group", name: name}, true
	}
	if strings.HasSuffix(value, ".target") {
		return virtualTarget{kind: "target", name: value}, true
	}
	return virtualTarget{}, false
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
