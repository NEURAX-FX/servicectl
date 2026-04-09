package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type startOptions struct {
	force bool
}

func cleanupSocketActivationState(unitName string, socketUnit *SocketUnit, opts startOptions) error {
	paths := unixSocketListenPaths(socketUnit)
	if len(paths) == 0 {
		return nil
	}
	holders := staleSocketHolderPIDs(unitName, socketUnit)
	rules := loadSocketHolderPolicyRules()
	if len(holders) == 0 {
		for _, path := range paths {
			_ = os.Remove(path)
			_ = os.Remove(path + ".lock")
		}
		return nil
	}
	remaining := subtractAcceptableExternalHolders(unitName, socketUnit, holders, rules)
	if len(remaining) == 0 && !opts.force {
		return nil
	}
	return cleanupStaleSocketHoldersAndPaths(unitName, paths, remainingOrOriginal(remaining, holders, opts.force), opts)
}

func subtractAcceptableExternalHolders(unitName string, socketUnit *SocketUnit, holders []int, rules map[string]socketHolderPolicyRule) []int {
	rule, ok := rules[unitName]
	if !ok || !rule.AllowAdaptiveStartup {
		return holders
	}
	acceptable := acceptableExternalHolderPIDs(rule.Provider, socketUnit)
	if len(acceptable) == 0 {
		return holders
	}
	filtered := make([]int, 0, len(holders))
	for _, pid := range holders {
		if !acceptable[pid] {
			filtered = append(filtered, pid)
		}
	}
	return filtered
}

func acceptableExternalHolderPIDs(provider string, socketUnit *SocketUnit) map[int]bool {
	switch provider {
	case "dbus":
		return dbusAcceptableHolderPIDs(socketUnit)
	default:
		return map[int]bool{}
	}
}

func remainingOrOriginal(remaining []int, holders []int, force bool) []int {
	if force {
		return holders
	}
	return remaining
}

func cleanupStaleSocketHoldersAndPaths(unitName string, paths []string, holders []int, opts startOptions) error {
	if len(holders) > 0 {
		if !opts.force && !staleSocketHolderCleanupEnabled() {
			return fmt.Errorf("refusing to kill stale socket holders for %s: %s (set SERVICECTL_KILL_STALE_SOCKET_HOLDERS=1 or pass --force to force cleanup)", unitName, formatPIDList(holders))
		}
		fmt.Printf("%s for %s: %s\n", colorize("Cleaning stale socket holders", styleYellow), unitName, formatPIDList(holders))
		for _, pid := range holders {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		waitForPIDsToExit(holders, 1500*time.Millisecond)
		for _, pid := range holders {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		waitForPIDsToExit(holders, 1500*time.Millisecond)
		remaining := make([]int, 0, len(holders))
		for _, pid := range holders {
			if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err == nil {
				remaining = append(remaining, pid)
			}
		}
		if len(remaining) > 0 {
			return fmt.Errorf("stale socket holders for %s still running after cleanup: %s", unitName, formatPIDList(remaining))
		}
	}
	for _, path := range paths {
		_ = os.Remove(path)
		_ = os.Remove(path + ".lock")
	}
	return nil
}
