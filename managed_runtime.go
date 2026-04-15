package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

func managedRuntimeServiceDir(name string, mode managedServiceMode) string {
	managedName := managedServiceName(name, mode)
	return filepath.Join(config.ManagedRuntimeDir, managedName)
}

func notifydStatePath(name string, mode managedServiceMode) string {
	return filepath.Join(managedRuntimeServiceDir(name, mode), "state")
}

func managedNotifySocketPath(name string, mode managedServiceMode) string {
	return filepath.Join(managedRuntimeServiceDir(name, mode), "notify.sock")
}

func ensureManagedRuntimeDir(name string, mode managedServiceMode, unit *Unit) error {
	dir := managedRuntimeServiceDir(name, mode)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create managed runtime dir: %w", err)
	}
	if !config.IsRoot || unit == nil || (strings.TrimSpace(unit.User) == "" && strings.TrimSpace(unit.Group) == "") {
		return nil
	}
	uid, gid, err := lookupOwnership(unit.User, unit.Group)
	if err != nil {
		return err
	}
	if err := os.Chown(dir, uid, gid); err != nil {
		return fmt.Errorf("chown managed runtime dir: %w", err)
	}
	return nil
}

func lookupOwnership(owner string, group string) (int, int, error) {
	uid := -1
	gid := -1
	if strings.TrimSpace(owner) != "" {
		u, err := user.Lookup(owner)
		if err != nil {
			return -1, -1, fmt.Errorf("lookup runtime owner %q: %w", owner, err)
		}
		parsedUID, err := strconv.Atoi(u.Uid)
		if err != nil {
			return -1, -1, fmt.Errorf("parse runtime uid %q: %w", u.Uid, err)
		}
		uid = parsedUID
		if strings.TrimSpace(group) == "" {
			parsedGID, err := strconv.Atoi(u.Gid)
			if err != nil {
				return -1, -1, fmt.Errorf("parse runtime gid %q: %w", u.Gid, err)
			}
			gid = parsedGID
		}
	}
	if strings.TrimSpace(group) != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return -1, -1, fmt.Errorf("lookup runtime group %q: %w", group, err)
		}
		parsedGID, err := strconv.Atoi(g.Gid)
		if err != nil {
			return -1, -1, fmt.Errorf("parse runtime gid %q: %w", g.Gid, err)
		}
		gid = parsedGID
	}
	return uid, gid, nil
}

func ensureManagedRuntimeSocketOwnership(path string, owner string, group string) error {
	if !config.IsRoot || (strings.TrimSpace(owner) == "" && strings.TrimSpace(group) == "") {
		return nil
	}
	target := strings.TrimSpace(owner)
	if target == "" {
		target = ":" + strings.TrimSpace(group)
	} else if strings.TrimSpace(group) != "" {
		target = target + ":" + strings.TrimSpace(group)
	}
	if target == "" {
		return nil
	}
	if err := exec.Command("chown", target, path).Run(); err != nil {
		return fmt.Errorf("chown managed runtime socket: %w", err)
	}
	return nil
}
