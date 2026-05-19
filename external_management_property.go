package main

import (
	"strings"

	"servicectl/internal/util"
)

func externalManagedPropertyKey(unitName string) string {
	return "persist.external-managed." + strings.TrimSuffix(strings.TrimSpace(resolveUnitAlias(unitName)), ".service")
}

func isExternallyManaged(unitName string) bool {
	value, ok := propertyValue(externalManagedPropertyKey(unitName))
	if !ok {
		return false
	}
	return util.ExternalManagedValueEnabled(value)
}

func setExternallyManaged(unitName string, enabled bool) error {
	value := "0"
	if enabled {
		value = "1"
	}
	return propertySet(externalManagedPropertyKey(unitName), value, true)
}
