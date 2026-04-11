package main

import (
	"fmt"
	"os"
	"strings"
)

func externalManagedPlaceholderName(unitName string) string {
	cleanName := strings.TrimSuffix(strings.TrimSpace(resolveUnitAlias(unitName)), ".service")
	if cleanName == "" {
		return "external-managed"
	}
	return "external-managed-" + cleanName
}

func externalManagedDependencyServiceName(unitName string, external bool) string {
	if external {
		return externalManagedPlaceholderName(unitName)
	}
	return strings.TrimSuffix(strings.TrimSpace(resolveUnitAlias(unitName)), ".service")
}

func resolvedManagedDependencyServiceName(dep string) (string, bool) {
	cleanName := strings.TrimSuffix(strings.TrimSpace(dep), ".service")
	if !includeDependencyName(cleanName) {
		return "", false
	}
	if externalManagedStateFunc(cleanName) {
		return externalManagedPlaceholderName(cleanName), true
	}
	return resolvedDependencyServiceName(dep)
}

func generateExternalManagedPlaceholderDinit(unitName string) string {
	cleanName := strings.TrimSuffix(strings.TrimSpace(resolveUnitAlias(unitName)), ".service")
	return fmt.Sprintf("# External-managed placeholder for %s.service\ntype = internal\n", cleanName)
}

func installExternalManagedPlaceholder(unitName string, opts installOptions) bool {
	_ = os.MkdirAll(config.DinitGenDir, 0755)
	_ = os.MkdirAll(config.DinitServiceDir, 0755)
	serviceName := externalManagedPlaceholderName(unitName)
	content := []byte(generateExternalManagedPlaceholderDinit(unitName))
	if !installGeneratedService(serviceName, content, opts) {
		return false
	}
	return true
}
