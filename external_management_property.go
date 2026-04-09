package main

import "strings"

func externalManagedPropertyKey(unitName string) string {
	return "persist.external-managed." + strings.TrimSuffix(strings.TrimSpace(resolveUnitAlias(unitName)), ".service")
}

func isExternallyManaged(unitName string) bool {
	value, ok := propertyValue(externalManagedPropertyKey(unitName))
	if !ok {
		return false
	}
	return externalManagedValueEnabled(value)
}

func setExternallyManaged(unitName string, enabled bool) error {
	value := "0"
	if enabled {
		value = "1"
	}
	return propertySet(externalManagedPropertyKey(unitName), value, true)
}

func externalManagedValueEnabled(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
