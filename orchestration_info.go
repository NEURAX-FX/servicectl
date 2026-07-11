package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"servicectl/internal/visionapi"
)

type busMeta struct {
	connected bool
	errorText string
	available bool
}

func orchestratorStateFilePath(unitName string) string {
	name := strings.TrimSuffix(strings.TrimSpace(resolveUnitAlias(unitName)), ".service") + ".state"
	return filepath.Join(visionapi.RuntimeDir(userMode(), runtimeDir()), "orchestrd", name)
}

func orchestratorState(unitName string) map[string]string {
	return parseKeyValueFile(orchestratorStateFilePath(unitName))
}

func orchestratorPID(unitName string) string {
	args := []string{"-f", "sys-orchestrd"}
	out, err := exec.Command("pgrep", args...).Output()
	if err != nil {
		return ""
	}
	needle := strings.TrimSuffix(resolveUnitAlias(unitName), ".service") + ".service"
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid := line
		if _, err := strconv.Atoi(pid); err == nil {
			cmdline, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
			if err != nil {
				continue
			}
			text := strings.ReplaceAll(string(cmdline), "\x00", " ")
			if strings.Contains(text, needle) {
				return pid
			}
		}
	}
	return ""
}

func s6ContentsContain(path string, name string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

func currentBusMeta() busMeta {
	meta, ok := queryBusMetaViaSysvision()
	if !ok {
		return busMeta{available: false}
	}
	return busMeta{available: true, connected: meta.ServicectlEventsConnected, errorText: meta.ServicectlEventsError}
}

func currentBusState() string {
	meta := currentBusMeta()
	if !meta.available {
		return "offline"
	}
	if meta.connected {
		return "connected"
	}
	if strings.TrimSpace(meta.errorText) != "" {
		return "degraded"
	}
	return "offline"
}
