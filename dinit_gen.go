package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type managedServiceMode string

const (
	managedDirect  managedServiceMode = "direct"
	managedNotifyd managedServiceMode = "notifyd"
	managedSocketd managedServiceMode = "socketd"
)

func dinitArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\r\"\\") {
		return s
	}
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func managedServiceModeForUnit(unit *Unit, socketUnit *SocketUnit) managedServiceMode {
	if socketUnit != nil {
		if socketUnit.Accept {
			return managedDirect
		}
		return managedSocketd
	}
	if unit != nil && strings.EqualFold(unit.Type, "notify") {
		return managedNotifyd
	}
	return managedDirect
}

func managedServiceName(name string, mode managedServiceMode) string {
	cleanName := strings.TrimSuffix(name, ".service")
	switch mode {
	case managedNotifyd:
		return cleanName + "-notifyd"
	case managedSocketd:
		return cleanName + "-socketd"
	default:
		return cleanName
	}
}

func notifydStatePath(name string, mode managedServiceMode) string {
	managedName := managedServiceName(name, mode)
	return filepath.Join(config.DinitGenDir, managedName+".state")
}

func notifydBinaryPath() string {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "sys-notifyd")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
	}
	if candidate, err := exec.LookPath("sys-notifyd"); err == nil {
		return candidate
	}
	return "sys-notifyd"
}

func logdBinaryPath() string {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "sys-logd")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
	}
	if candidate, err := exec.LookPath("sys-logd"); err == nil {
		return candidate
	}
	return "sys-logd"
}

func shouldManageWithNotifyd(unit *Unit, socketUnit *SocketUnit) bool {
	return managedServiceModeForUnit(unit, socketUnit) != managedDirect
}

func dinitNameForUnit(unitName string) string {
	cleanName := resolveUnitAlias(strings.TrimSuffix(unitName, ".service"))
	unit, err := parseSystemdUnit(cleanName)
	if err != nil {
		return cleanName
	}
	socketUnit, socketErr := parseOptionalSocketUnit(cleanName)
	if socketErr == nil {
		return managedServiceName(cleanName, managedServiceModeForUnit(unit, socketUnit))
	}
	return managedServiceName(cleanName, managedServiceModeForUnit(unit, nil))
}

func resolvedDependencyServiceName(dep string) (string, bool) {
	cleanName := strings.TrimSuffix(strings.TrimSpace(dep), ".service")
	if !includeDependencyName(cleanName) {
		return "", false
	}
	cleanName = resolveUnitAlias(cleanName)
	if _, err := parseSystemdUnit(cleanName); err == nil {
		return dinitNameForUnit(cleanName), true
	}
	if isDinitServiceKnown(cleanName) {
		return cleanName, true
	}
	if mapped := dinitNameForUnit(cleanName); mapped != cleanName && isDinitServiceKnown(mapped) {
		return mapped, true
	}
	return "", false
}

func loggerServiceName(serviceName string) string {
	return serviceName + "-log"
}

func journalIdentifier(serviceName string) string {
	cleanName := strings.TrimSuffix(strings.TrimSpace(serviceName), ".service")
	if cleanName == "" {
		cleanName = "service"
	}
	return "servicectl[" + cleanName + "]"
}

func generateLoggerDinit(serviceName string, logicalName string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Log consumer for %s\n", serviceName))
	sb.WriteString("type = process\n")
	sb.WriteString(fmt.Sprintf("consumer-of = %s\n", serviceName))
	sb.WriteString(fmt.Sprintf("command = %s -service %s\n", dinitArg(logdBinaryPath()), dinitArg(strings.TrimSuffix(logicalName, ".service"))))
	sb.WriteString("restart = false\n")
	return sb.String()
}

func normalizeSocketAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if _, err := strconv.Atoi(value); err == nil {
		return ":" + value
	}
	return value
}

func socketListenArgs(socketUnit *SocketUnit) []string {
	if socketUnit == nil {
		return nil
	}
	args := make([]string, 0, len(socketUnit.ListenStreams)+len(socketUnit.ListenDgrams))
	for _, addr := range socketUnit.ListenStreams {
		network := "tcp"
		if strings.HasPrefix(addr, "/") {
			network = "unix"
		}
		args = append(args, "-listen", dinitArg(network+":"+normalizeSocketAddress(addr)))
	}
	for _, addr := range socketUnit.ListenDgrams {
		network := "udp"
		if strings.HasPrefix(addr, "/") {
			network = "unixgram"
		}
		args = append(args, "-listen", dinitArg(network+":"+normalizeSocketAddress(addr)))
	}
	if socketUnit.SocketMode != "" {
		args = append(args, "-socket-mode", dinitArg(socketUnit.SocketMode))
	}
	for _, fdName := range socketUnit.FDNames {
		args = append(args, "-fdname", dinitArg(fdName))
	}
	if socketUnit.SocketUser != "" {
		args = append(args, "-socket-user", dinitArg(socketUnit.SocketUser))
	}
	if socketUnit.SocketGroup != "" {
		args = append(args, "-socket-group", dinitArg(socketUnit.SocketGroup))
	}
	return args
}

func normalizeTimeoutValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "infinity") {
		return ""
	}
	if _, err := strconv.Atoi(raw); err == nil {
		return raw + "s"
	}
	return raw
}

func directoryMode(raw string) os.FileMode {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0755
	}
	value, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return 0755
	}
	return os.FileMode(value)
}

func chownPath(path string, owner string, group string) {
	if !config.IsRoot || (owner == "" && group == "") {
		return
	}
	target := owner
	if target == "" {
		target = ":" + group
	} else if group != "" {
		target = owner + ":" + group
	}
	if target != "" {
		exec.Command("chown", target, path).Run()
	}
}

func managedDirectoryBase(kind string) string {
	if config.IsRoot && !userMode() {
		switch kind {
		case "runtime":
			return "/run"
		case "state":
			return "/var/lib"
		case "logs":
			return "/var/log"
		}
		return "/"
	}
	home := os.Getenv("HOME")
	switch kind {
	case "runtime":
		if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
			return base
		}
		return filepath.Join(home, ".local", "run")
	case "state":
		if base := os.Getenv("XDG_STATE_HOME"); base != "" {
			return base
		}
		return filepath.Join(home, ".local", "state")
	case "logs":
		if base := os.Getenv("XDG_STATE_HOME"); base != "" {
			return filepath.Join(base, "log")
		}
		return filepath.Join(home, ".local", "state", "log")
	default:
		return home
	}
}

func ensureManagedDirectories(entries []string, kind string, rawMode string, owner string, group string) {
	if len(entries) == 0 {
		return
	}
	base := managedDirectoryBase(kind)
	mode := directoryMode(rawMode)
	owner = strings.TrimSpace(owner)
	group = strings.TrimSpace(group)
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		path := filepath.Join(base, strings.TrimLeft(entry, "/"))
		_ = os.MkdirAll(path, mode)
		_ = os.Chmod(path, mode)
		chownPath(path, owner, group)
	}
}

func ensureServiceDirectories(u *Unit) {
	ensureManagedDirectories(u.RuntimeDirectory, "runtime", u.RuntimeDirMode, u.User, u.Group)
	ensureManagedDirectories(u.StateDirectory, "state", u.StateDirMode, u.User, u.Group)
	ensureManagedDirectories(u.LogsDirectory, "logs", u.LogsDirMode, u.User, u.Group)
}

func (u *Unit) GenerateDinit() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Generated from %s.service\n", u.Name))
	if u.Description != "" {
		sb.WriteString(fmt.Sprintf("# %s\n", u.Description))
	}
	sysType := strings.ToLower(u.Type)
	dType := "process"
	switch sysType {
	case "forking":
		dType = "bgprocess"
	case "oneshot":
		dType = "scripted"
	}
	sb.WriteString(fmt.Sprintf("type = %s\n", dType))
	if dType == "bgprocess" && u.PIDFile != "" {
		sb.WriteString(fmt.Sprintf("pid-file = %s\n", u.PIDFile))
	}
	if dType != "scripted" {
		sb.WriteString("smooth-recovery = true\n")
	}
	allEnvs := mergedServiceEnv(u)
	finalCmd := compileExecStart(u, allEnvs)
	envPrefixMap := make(map[string]string, len(allEnvs))
	for k, v := range allEnvs {
		envPrefixMap[k] = v
	}
	sb.WriteString(fmt.Sprintf("command = %s%s\n", buildEnvPrefix(envPrefixMap), finalCmd))
	if u.ExecStop != "" {
		stopCmd := compileExecStop(u, allEnvs)
		sb.WriteString(fmt.Sprintf("stop-command = %s\n", stopCmd))
	}
	if u.User != "" {
		sb.WriteString(fmt.Sprintf("run-as = %s\n", u.User))
	}
	if u.WorkingDirectory != "" {
		sb.WriteString(fmt.Sprintf("working-dir = %s\n", u.WorkingDirectory))
	}
	seen := make(map[string]bool)
	for _, d := range hardStartDependencies(u) {
		resolved, ok := resolvedManagedDependencyServiceName(d)
		if ok && resolved != u.Name && !seen[resolved] {
			sb.WriteString(fmt.Sprintf("depends-on = %s\n", resolved))
			seen[resolved] = true
		}
	}
	sb.WriteString(fmt.Sprintf("depends-on = %s\n", loggerServiceName(u.Name)))
	sb.WriteString("log-type = pipe\n")
	return sb.String()
}

func (u *Unit) GenerateNotifydDinit(mode managedServiceMode, socketUnit *SocketUnit) string {
	var sb strings.Builder
	managedName := managedServiceName(u.Name, mode)
	sb.WriteString(fmt.Sprintf("# Generated from %s.service\n", u.Name))
	if mode == managedSocketd && socketUnit != nil {
		sb.WriteString(fmt.Sprintf("# Socket activation via %s.socket\n", socketUnit.Name))
	}
	if u.Description != "" {
		sb.WriteString(fmt.Sprintf("# %s\n", u.Description))
	}
	sb.WriteString("type = process\n")
	if u.User != "" {
		sb.WriteString(fmt.Sprintf("run-as = %s\n", u.User))
	}
	if u.WorkingDirectory != "" {
		sb.WriteString(fmt.Sprintf("working-dir = %s\n", u.WorkingDirectory))
	}
	seen := make(map[string]bool)
	for _, d := range hardStartDependencies(u) {
		resolved, ok := resolvedManagedDependencyServiceName(d)
		if ok && resolved != managedName && !seen[resolved] {
			sb.WriteString(fmt.Sprintf("depends-on = %s\n", resolved))
			seen[resolved] = true
		}
	}
	sb.WriteString(fmt.Sprintf("depends-on = %s\n", loggerServiceName(managedName)))
	backendEnv := mergedServiceEnv(u)
	backendCmd := compileExecStart(u, backendEnv)
	backendCmd = buildEnvPrefix(backendEnv) + backendCmd
	args := []string{notifydBinaryPath(), "-service", dinitArg(u.Name), "-service-type", dinitArg(strings.ToLower(u.Type)), "-command", dinitArg(backendCmd), "-state-file", dinitArg(notifydStatePath(u.Name, mode))}
	if userMode() {
		args = append(args, "-user")
	}
	if stopCmd := compileExecStop(u, backendEnv); stopCmd != "" {
		args = append(args, "-stop-command", dinitArg(stopCmd))
	}
	if strings.EqualFold(u.Type, "notify") {
		notifyPath := filepath.Join(config.DinitGenDir, managedName+".notify.sock")
		args = append(args, "-notify", "-notify-path", dinitArg(notifyPath), "-ready-timeout", "30s")
	}
	if timeout := normalizeTimeoutValue(u.TimeoutStartSec); timeout != "" {
		args = append(args, "-ready-timeout", dinitArg(timeout))
	}
	if timeout := normalizeTimeoutValue(u.TimeoutStopSec); timeout != "" {
		args = append(args, "-stop-timeout", dinitArg(timeout))
	}
	if sig := normalizeSignalName(u.KillSignal); sig != "" {
		args = append(args, "-kill-signal", dinitArg(sig))
	}
	if mode == managedSocketd {
		args = append(args, socketListenArgs(socketUnit)...)
	}
	if mode != managedSocketd {
		args = append(args, "-start-now")
	}
	sb.WriteString(fmt.Sprintf("command = %s\n", strings.Join(args, " ")))
	sb.WriteString("smooth-recovery = true\n")
	sb.WriteString("log-type = pipe\n")
	return sb.String()
}

type installOptions struct {
	freshLoadOnChange map[string]bool
}

func installGeneratedService(serviceName string, content []byte, opts installOptions) bool {
	genPath := filepath.Join(config.DinitGenDir, serviceName)
	changed := true
	if existing, readErr := os.ReadFile(genPath); readErr == nil {
		changed = string(existing) != string(content)
	}
	if changed {
		if err := os.WriteFile(genPath, content, 0644); err != nil {
			fmt.Printf("Error writing dinit file for %s: %v\n", serviceName, err)
			return false
		}
	}
	linkPath := filepath.Join(config.DinitServiceDir, serviceName)
	linkChanged := true
	if currentTarget, linkErr := os.Readlink(linkPath); linkErr == nil {
		linkChanged = currentTarget != genPath
	}
	if linkChanged {
		_ = os.Remove(linkPath)
		if err := os.Symlink(genPath, linkPath); err != nil {
			fmt.Printf("Error linking dinit file for %s: %v\n", serviceName, err)
			return false
		}
	}
	if isDinitServiceKnown(serviceName) && (changed || linkChanged) {
		if opts.freshLoadOnChange[serviceName] {
			runDinitctl("unload", "--ignore-unstarted", serviceName)
			return true
		}
		runDinitctl("reload", serviceName)
	}
	return true
}

func recursiveInstall(unitName string, visited map[string]bool, opts installOptions) {
	cleanName := strings.TrimSuffix(unitName, ".service")
	if visited[cleanName] {
		return
	}
	visited[cleanName] = true
	unit, err := parseSystemdUnit(cleanName)
	if err != nil {
		return
	}
	socketUnit, socketErr := parseOptionalSocketUnit(cleanName)
	if socketErr != nil {
		fmt.Printf("Error parsing socket for %s: %v\n", cleanName, socketErr)
		return
	}
	mode := managedServiceModeForUnit(unit, socketUnit)
	serviceName := managedServiceName(cleanName, mode)
	knownToDinit := isDinitServiceKnown(serviceName)
	for _, d := range hardStartDependencies(unit) {
		cleanDep := strings.TrimSuffix(resolveUnitAlias(d), ".service")
		if externalManagedStateFunc(cleanDep) {
			if !installExternalManagedPlaceholder(cleanDep, opts) {
				return
			}
			continue
		}
		if _, ok := resolvedDependencyServiceName(d); ok {
			recursiveInstall(cleanDep, visited, opts)
		}
	}
	_ = os.MkdirAll(config.DinitGenDir, 0755)
	_ = os.MkdirAll(config.DinitServiceDir, 0755)
	if unit.PIDFile != "" {
		pDir := filepath.Dir(unit.PIDFile)
		if pDir != "/" && pDir != "." {
			_ = os.MkdirAll(pDir, 0755)
			if config.IsRoot && unit.User != "" {
				exec.Command("chown", unit.User, pDir).Run()
			}
		}
	}
	ensureServiceDirectories(unit)
	var content []byte
	if mode != managedDirect {
		content = []byte(unit.GenerateNotifydDinit(mode, socketUnit))
	} else {
		content = []byte(unit.GenerateDinit())
	}
	if !installGeneratedService(serviceName, content, opts) {
		return
	}
	loggerName := loggerServiceName(serviceName)
	if !installGeneratedService(loggerName, []byte(generateLoggerDinit(serviceName, cleanName)), opts) {
		return
	}
	if knownToDinit && !isDinitServiceKnown(serviceName) {
		runDinitctl("reload", serviceName)
	}
}
