package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"servicectl/internal/util"
)

func s6GroupOrchestrdServiceName(group string) string {
	return "group-" + util.SanitizeS6Name(group, "group") + "-orchestrd"
}

func s6GroupOrchestrdSourceDir(group string) string {
	return filepath.Join(s6SourceRoot(), s6GroupOrchestrdServiceName(group))
}

func enableGroupWithS6(group string) error {
	if !s6AvailableFunc() {
		return fmt.Errorf("s6 backend is not available")
	}
	if err := ensureS6Bundle(); err != nil {
		return err
	}
	serviceDir := s6GroupOrchestrdSourceDir(group)
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "type"), []byte("longrun\n"), 0644); err != nil {
		return err
	}
	runLine := s6OrchestrdRunPrefix()
	runLine += " --group " + strings.TrimSpace(group)
	runScript := strings.Join([]string{"#!/usr/bin/execlineb -P", runLine, ""}, "\n")
	if err := os.WriteFile(filepath.Join(serviceDir, "run"), []byte(runScript), 0755); err != nil {
		return err
	}
	if err := writeS6OrchestrdDependencies(serviceDir, s6OrchestrdBaseDependencies()); err != nil {
		return err
	}
	entries, _ := os.ReadFile(s6BundleContentsPath())
	bundleEntries := uniqueLinesPreserveOrder(string(entries))
	serviceName := s6GroupOrchestrdServiceName(group)
	bundleEntries = appendUniqueLinePreserveOrder(bundleEntries, serviceName)
	if err := writeLineFile(s6BundleContentsPath(), bundleEntries); err != nil {
		return err
	}
	if err := refreshS6OrchestrdDependencies(); err != nil {
		return err
	}
	if err := validateS6Sources(); err != nil {
		return err
	}
	if s6LiveEnabled() {
		if err := liveUpdateS6(); err != nil {
			return err
		}
		for _, name := range []string{s6ServicectlAPIServiceName(), s6SysPropertydServiceName(), s6SysvisiondServiceName(), serviceName} {
			if err := liveStartS6(name); err != nil {
				return err
			}
		}
	}
	return nil
}

func disableGroupWithS6(group string) error {
	if !s6AvailableFunc() {
		return fmt.Errorf("s6 backend is not available")
	}
	serviceName := s6GroupOrchestrdServiceName(group)
	if s6LiveEnabled() {
		if err := liveStopS6(serviceName); err != nil {
			return err
		}
	}
	entries, _ := os.ReadFile(s6BundleContentsPath())
	bundleEntries := uniqueLinesPreserveOrder(string(entries))
	filtered := make([]string, 0, len(bundleEntries))
	for _, entry := range bundleEntries {
		if entry != serviceName {
			filtered = append(filtered, entry)
		}
	}
	if err := writeLineFile(s6BundleContentsPath(), filtered); err != nil {
		return err
	}
	if err := os.RemoveAll(s6GroupOrchestrdSourceDir(group)); err != nil {
		return err
	}
	if err := refreshS6OrchestrdDependencies(); err != nil {
		return err
	}
	if err := validateS6Sources(); err != nil {
		return err
	}
	if s6LiveEnabled() {
		if err := liveUpdateS6(); err != nil {
			return err
		}
	}
	return nil
}

func isGroupEnabledWithS6(group string) bool {
	entries, err := os.ReadFile(s6BundleContentsPath())
	if err != nil {
		return false
	}
	return containsString(uniqueLinesPreserveOrder(string(entries)), s6GroupOrchestrdServiceName(group))
}
