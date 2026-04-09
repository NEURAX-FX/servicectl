package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func dbusAcceptableHolderPIDs(socketUnit *SocketUnit) map[int]bool {
	result := make(map[int]bool)
	paths := unixSocketListenPaths(socketUnit)
	if len(paths) == 0 || !anyLiveUnixSocketPath(paths) {
		return result
	}
	pid := dbusOwnerPID()
	if pid <= 0 || !pidExists(pid) {
		return result
	}
	result[pid] = true
	return result
}

func anyLiveUnixSocketPath(paths []string) bool {
	content, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		for _, path := range paths {
			if strings.Contains(line, path) {
				return true
			}
		}
	}
	return false
}

func dbusOwnerPID() int {
	cmd := exec.Command("busctl", "--system", "status", "org.freedesktop.DBus")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PID=") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PID=")))
		if err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

func pidExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}
