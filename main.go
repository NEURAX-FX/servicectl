package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"servicectl/internal/visionapi"
)

func printHelp() {
	fmt.Println(`servicectl - systemd-like control for dinit with s6 orchestration

Usage:
  servicectl [--user] <command> [unit...]
  servicectl [--user] logs [-f] [-n lines] <unit...>
  servicectl [--user] kill <unit...> [signal]
  servicectl [--user] dinit <args...>

Core commands:
  start         Install/generated dependencies as needed, then start unit(s)
  stop          Stop unit(s)
  restart       Restart unit(s)
  reload        Reload unit(s)
  kill          Send a signal to unit(s); default SIGTERM
  status        Show systemctl-like status output
  show          Show detailed diagnostic view
  list          List discovered units
  logs          Show logs for unit(s)
  is-active     Exit 0 if unit is active
  is-failed     Exit 0 if unit is failed

S6 orchestration commands:
  enable        Generate and register <unit>-orchestrd in the shared s6 graph
  disable       Remove orchestrd registration and stop the unit best-effort
  is-enabled    Report whether the unit has an s6 orchestrd registration

Low-level / internal:
  dinit         Raw passthrough to dinit/dinitctl
  serve-api     Start the local servicectl query/event API socket
  help          Show this help

Modes:
  default       System mode; runtime paths live under /run/servicectl and /s6/rc
  --user        User mode; runtime sockets live under /run/user/<uid>/servicectl

Local API sockets:
  servicectl    /run/servicectl/servicectl.sock
  events        /run/servicectl/servicectl-events.sock
  sysvisiond    /run/servicectl/sysvision/sysvisiond.sock
  user mode     same layout under /run/user/<uid>/servicectl/

Notes:
  enable/disable/is-enabled require the s6 backend to be available
  serve-api and sysvisiond are intended for local tooling and orchestrators
  use 'servicectl dinit ...' when you want raw dinit output`)
}

func parseDinitStatus(output string) map[string]string {
	status := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		status[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return status
}

func dinitctlOutput(args ...string) (string, int, error) {
	cmd := dinitctlCommand(args...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err == nil {
		return text, 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return text, exitErr.ExitCode(), err
	}
	return text, 1, err
}

func dinitStatus(unitName string) map[string]string {
	out, err := dinitctlCommand("status", unitName).CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil
	}
	return parseDinitStatus(string(out))
}

func parseKeyValueFile(path string) map[string]string {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		values[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return values
}

func formatActiveLine(state string) string {
	switch {
	case strings.EqualFold(strings.TrimSpace(state), "NOT LOADED"):
		return "inactive (backend not loaded)"
	case state == "STARTED":
		return "active (running)"
	case strings.HasPrefix(state, "STOPPED (terminated; exited - status "):
		return "failed"
	case strings.HasPrefix(state, "STOPPED"):
		return "inactive (dead)"
	default:
		return strings.ToLower(state)
	}
}

func parseExitStatus(state string) string {
	if !strings.HasPrefix(state, "STOPPED (terminated; exited - status ") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(state, "STOPPED (terminated; exited - status "), ")")
}

func processStartTime(pid string) string {
	if pid == "" {
		return ""
	}
	if _, err := strconv.Atoi(pid); err != nil {
		return ""
	}
	out, err := exec.Command("ps", "-p", pid, "-o", "lstart=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func socketManagedActiveLine(state string, runtimePhase string, runtimeChildState string) string {
	base := formatActiveLine(state)
	if base != "active (running)" {
		return base
	}
	switch runtimeChildState {
	case "idle":
		return "active (socket manager running, backend idle)"
	case "starting":
		return "active (socket manager running, backend starting)"
	case "running":
		return "active (socket manager running, backend running)"
	case "stopping":
		return "deactivating (socket manager running, backend stopping)"
	}
	if runtimePhase == "ready" {
		return "active (socket manager running)"
	}
	return base
}

func notifyManagedActiveLine(state string, runtimeChildState string) string {
	base := formatActiveLine(state)
	if base != "active (running)" {
		return base
	}
	switch runtimeChildState {
	case "starting":
		return "activating (notify manager running, backend starting)"
	case "running":
		return "active (notify manager running, backend running)"
	case "stopping":
		return "deactivating (notify manager running, backend stopping)"
	default:
		return base
	}
}

func printStatus(unitName string) {
	if snapshot, ok := queryUnitSnapshotViaSysvision(unitName); ok {
		printStatusFromSnapshot(unitName, snapshot)
		return
	}
	unit, err := parseSystemdUnit(unitName)
	socketUnit, _ := parseOptionalSocketUnit(unitName)
	mode := managedDirect
	dinitName := unitName
	runtimeState := map[string]string(nil)
	if err == nil {
		mode = managedServiceModeForUnit(unit, socketUnit)
		dinitName = managedServiceName(unitName, mode)
		if mode != managedDirect {
			runtimeState = parseKeyValueFile(notifydStatePath(unitName, mode))
		}
	}
	out, statusErr := dinitctlCommand("status", dinitName).CombinedOutput()
	if err != nil && statusErr != nil {
		fmt.Printf("Unit %s.service could not be found.\n", unitName)
		return
	}
	status := parseDinitStatus(string(out))

	description := unitName
	loadedPath := filepath.Join(config.DinitGenDir, dinitName)
	managerPID := status["Process ID"]
	mainPID := managerPID
	runtimeMainPID := ""
	runtimePhase := ""
	runtimeChildState := ""
	runtimeStatus := ""
	runtimeFailure := ""
	runtimeSocketCount := 0
	if err == nil {
		if unit.Description != "" {
			description = unit.Description
		}
		if unit.SourcePath != "" {
			loadedPath = unit.SourcePath
		}
	}

	state := status["State"]
	if state == "" {
		if statusErr != nil {
			state = "NOT LOADED"
		} else {
			state = "unknown"
		}
	}
	if runtimeState != nil {
		runtimeMainPID = runtimeState["main_pid"]
		runtimePhase = runtimeState["phase"]
		runtimeChildState = runtimeState["child_state"]
		runtimeStatus = runtimeState["status"]
		runtimeFailure = runtimeState["failure"]
		runtimeSocketCount, _ = strconv.Atoi(runtimeState["socket_count"])
		if runtimeMainPID != "" && runtimeMainPID != "0" {
			mainPID = runtimeMainPID
		} else if runtimeSocketCount > 0 {
			mainPID = ""
		}
	}
	startedAt := formatStartTime(mainPID)
	exitStatus := parseExitStatus(state)
	activeLine := formatActiveLine(state)
	if runtimeState != nil {
		if runtimeSocketCount > 0 {
			activeLine = socketManagedActiveLine(state, runtimePhase, runtimeChildState)
		} else {
			activeLine = notifyManagedActiveLine(state, runtimeChildState)
		}
	}

	fmt.Printf("%s %s - %s\n", unitBullet(activeLine), colorize(unitName+".service", styleBold), description)
	printStatusKV("Loaded", "loaded ("+loadedPath+")")
	if startedAt != "" && state == "STARTED" {
		printStatusKV("Active", activeLine+" since "+startedAt)
	} else {
		printStatusKV("Active", activeLine)
	}
	if exitStatus != "" {
		printStatusKV("Result", "exit-code (status="+exitStatus+")")
	}
	if runtimeState != nil && managerPID != "" && managerPID != "0" {
		printStatusKV("Manager PID", managerPID)
	}
	if mainPID != "" && mainPID != "0" {
		printStatusKV("Main PID", mainPID)
	}
	if activation := status["Activation"]; activation != "" {
		printStatusKV("Activation", activation)
	}
	if runtimePhase != "" {
		printStatusKV("Phase", runtimePhase)
	}
	if runtimeChildState != "" {
		printStatusKV("Child", runtimeChildState)
	}
	if runtimeStatus != "" {
		printStatusKV("Status", runtimeStatus)
	}
	if runtimeFailure != "" {
		printStatusKV("Failure", runtimeFailure)
	}
}

func printStatusFromSnapshot(unitName string, snapshot visionapi.UnitSnapshot) {
	description := unitName
	if snapshot.Description != "" {
		description = snapshot.Description
	}
	loadedPath := snapshot.SourcePath
	if loadedPath == "" {
		loadedPath = filepath.Join(config.DinitGenDir, snapshot.DinitName)
	}
	managerPID := strings.TrimSpace(snapshot.ManagerPID)
	mainPID := strings.TrimSpace(snapshot.MainPID)
	state := strings.TrimSpace(snapshot.State)
	if state == "" {
		if dinitStatus(snapshot.DinitName) == nil {
			state = "NOT LOADED"
		} else {
			state = "unknown"
		}
	}
	runtimePhase := strings.TrimSpace(snapshot.Phase)
	runtimeChildState := strings.TrimSpace(snapshot.ChildState)
	runtimeStatus := strings.TrimSpace(snapshot.Status)
	runtimeFailure := strings.TrimSpace(snapshot.Failure)
	orchName := s6OrchestrdServiceName(unitName)
	orchPID := orchestratorPID(unitName)
	enabledState := "disabled"
	if isEnabledWithS6(unitName) {
		enabledState = "enabled"
	}
	busState := currentBusState()
	activeLine := formatActiveLine(state)
	if snapshot.ManagedBy == "sys-notifyd" {
		if snapshot.NotifySocket != "" {
			activeLine = socketManagedActiveLine(state, runtimePhase, runtimeChildState)
		} else {
			activeLine = notifyManagedActiveLine(state, runtimeChildState)
		}
	}
	startedAt := formatStartTime(mainPID)
	exitStatus := parseExitStatus(state)

	fmt.Printf("%s %s - %s\n", unitBullet(activeLine), colorize(unitName+".service", styleBold), description)
	printStatusKV("Loaded", "loaded ("+loadedPath+")")
	if startedAt != "" && state == "STARTED" {
		printStatusKV("Active", activeLine+" since "+startedAt)
	} else {
		printStatusKV("Active", activeLine)
	}
	if exitStatus != "" {
		printStatusKV("Result", "exit-code (status="+exitStatus+")")
	}
	printStatusKV("Enabled", enabledState)
	if snapshot.ManagedBy == "sys-notifyd" && managerPID != "" && managerPID != "0" {
		printStatusKV("Manager PID", managerPID)
	}
	if mainPID != "" && mainPID != "0" {
		printStatusKV("Main PID", mainPID)
	}
	printStatusKV("Orchestrator", orchName)
	if orchPID != "" {
		printStatusKV("Orch PID", orchPID)
	}
	printStatusKV("Bus", busState)
	if snapshot.Activation != "" {
		printStatusKV("Activation", snapshot.Activation)
	}
	if runtimePhase != "" {
		printStatusKV("Phase", runtimePhase)
	}
	if runtimeChildState != "" {
		printStatusKV("Child", runtimeChildState)
	}
	if runtimeStatus != "" {
		printStatusKV("Status", runtimeStatus)
	}
	if runtimeFailure != "" {
		printStatusKV("Failure", runtimeFailure)
	}
}

func currentMainPIDForUnit(unitName string) string {
	unit, err := parseSystemdUnit(unitName)
	socketUnit, _ := parseOptionalSocketUnit(unitName)
	if err == nil {
		mode := managedServiceModeForUnit(unit, socketUnit)
		if mode != managedDirect {
			runtimeState := parseKeyValueFile(notifydStatePath(unitName, mode))
			if runtimeState != nil {
				if mainPID := strings.TrimSpace(runtimeState["main_pid"]); mainPID != "" && mainPID != "0" {
					return mainPID
				}
			}
		}
	}
	dinitName := backendServiceNameForUnit(unitName)
	status := dinitStatus(dinitName)
	if status == nil {
		return ""
	}
	pid := strings.TrimSpace(status["Process ID"])
	if pid == "0" {
		return ""
	}
	return pid
}

func backendServiceNameForUnit(unitName string) string {
	cleanName := strings.TrimSuffix(strings.TrimSpace(resolveUnitAlias(unitName)), ".service")
	unit, err := parseSystemdUnit(cleanName)
	if err != nil {
		return dinitNameForUnit(cleanName)
	}
	socketUnit, _ := parseOptionalSocketUnit(cleanName)
	return managedServiceName(cleanName, managedServiceModeForUnit(unit, socketUnit))
}

func shouldSkipFailedDependency(unitName string) bool {
	unit, err := parseSystemdUnit(unitName)
	if err != nil {
		return false
	}
	if !strings.EqualFold(unit.Type, "oneshot") || !unit.RemainAfterExit {
		return false
	}
	status := dinitStatus(backendServiceNameForUnit(unitName))
	if status == nil {
		return false
	}
	state := strings.TrimSpace(status["State"])
	return strings.HasPrefix(state, "STOPPED")
}

func stopUnit(unitName string) int {
	if !runDinitctl("stop", backendServiceNameForUnit(unitName)) {
		return 1
	}
	return 0
}

func addTrackedPID(tracked map[int]bool, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		return
	}
	tracked[pid] = true
}

func trackedUnitPIDs(unitName string) map[int]bool {
	tracked := map[int]bool{os.Getpid(): true}
	if status := dinitStatus(backendServiceNameForUnit(unitName)); status != nil {
		addTrackedPID(tracked, status["Process ID"])
	}
	if unit, err := parseSystemdUnit(unitName); err == nil {
		socketUnit, _ := parseOptionalSocketUnit(unitName)
		mode := managedServiceModeForUnit(unit, socketUnit)
		if mode != managedDirect {
			if runtimeState := parseKeyValueFile(notifydStatePath(unitName, mode)); runtimeState != nil {
				addTrackedPID(tracked, runtimeState["main_pid"])
			}
		}
	}
	addTrackedPID(tracked, currentMainPIDForUnit(unitName))
	return tracked
}

func unixSocketListenPaths(socketUnit *SocketUnit) []string {
	if socketUnit == nil {
		return nil
	}
	seen := make(map[string]bool)
	paths := make([]string, 0, len(socketUnit.ListenStreams)+len(socketUnit.ListenDgrams))
	for _, value := range append(append([]string{}, socketUnit.ListenStreams...), socketUnit.ListenDgrams...) {
		value = strings.TrimSpace(value)
		if value == "" || !strings.HasPrefix(value, "/") || seen[value] {
			continue
		}
		seen[value] = true
		paths = append(paths, value)
	}
	return paths
}

func socketInodesForPaths(paths []string) map[uint64]bool {
	inodes := make(map[uint64]bool)
	content, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return inodes
	}
	pathSet := make(map[string]bool, len(paths))
	for _, path := range paths {
		pathSet[path] = true
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		path := fields[len(fields)-1]
		if !pathSet[path] {
			continue
		}
		inode, err := strconv.ParseUint(fields[6], 10, 64)
		if err != nil || inode == 0 {
			continue
		}
		inodes[inode] = true
	}
	return inodes
}

func processReferencesAnyPath(pid int, paths []string, socketInodes map[uint64]bool) bool {
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err != nil {
			continue
		}
		target = strings.TrimSuffix(target, " (deleted)")
		for _, path := range paths {
			if target == path {
				return true
			}
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			inodeText := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			if inode, err := strconv.ParseUint(inodeText, 10, 64); err == nil && socketInodes[inode] {
				return true
			}
		}
	}
	return false
}

func staleSocketHolderPIDs(unitName string, socketUnit *SocketUnit) []int {
	paths := unixSocketListenPaths(socketUnit)
	if len(paths) == 0 {
		return nil
	}
	probePaths := make([]string, 0, len(paths)*2)
	for _, path := range paths {
		probePaths = append(probePaths, path, path+".lock")
	}
	socketInodes := socketInodesForPaths(paths)
	tracked := trackedUnitPIDs(unitName)
	holders := make([]int, 0)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return holders
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || tracked[pid] {
			continue
		}
		if processReferencesAnyPath(pid, probePaths, socketInodes) {
			holders = append(holders, pid)
		}
	}
	sort.Ints(holders)
	return holders
}

func waitForPIDsToExit(pids []int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		remaining := false
		for _, pid := range pids {
			if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err == nil {
				remaining = true
				break
			}
		}
		if !remaining {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func formatPIDList(pids []int) string {
	parts := make([]string, 0, len(pids))
	for _, pid := range pids {
		parts = append(parts, strconv.Itoa(pid))
	}
	return strings.Join(parts, ", ")
}

func staleSocketHolderCleanupEnabled() bool {
	value := strings.TrimSpace(os.Getenv("SERVICECTL_KILL_STALE_SOCKET_HOLDERS"))
	return value == "1" || strings.EqualFold(value, "true")
}

func cleanupStaleSocketArtifacts(unitName string, socketUnit *SocketUnit) error {
	paths := unixSocketListenPaths(socketUnit)
	if len(paths) == 0 {
		return nil
	}
	holders := staleSocketHolderPIDs(unitName, socketUnit)
	if len(holders) > 0 {
		if !staleSocketHolderCleanupEnabled() {
			return fmt.Errorf("refusing to kill stale socket holders for %s: %s (set SERVICECTL_KILL_STALE_SOCKET_HOLDERS=1 to force cleanup)", unitName, formatPIDList(holders))
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

func reloadUnit(unitName string) int {
	unit, err := parseSystemdUnit(unitName)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	socketUnit, _ := parseOptionalSocketUnit(unitName)
	allEnvs := mergedServiceEnv(unit)
	mainPID := currentMainPIDForUnit(unitName)
	needsMainPID := strings.Contains(unit.ExecReload, "$MAINPID") || strings.Contains(unit.ExecReload, "${MAINPID}")
	if mainPID == "" && managedServiceModeForUnit(unit, socketUnit) != managedDirect && strings.Contains(unit.ExecReload, "kill") {
		dinitName := backendServiceNameForUnit(unitName)
		status := dinitStatus(dinitName)
		if status != nil {
			fmt.Printf("ExecReload for %s.service requires runtime PID context, but no runtime main process is available. Reloading dinit service description only.\n", unitName)
			return runDinitctlWithStatus("reload", dinitName)
		}
		fmt.Printf("ExecReload for %s.service requires runtime PID context, but no runtime main process is available. Use restart instead.\n", unitName)
		return 1
	}
	if needsMainPID && mainPID == "" {
		dinitName := backendServiceNameForUnit(unitName)
		status := dinitStatus(dinitName)
		if status != nil {
			fmt.Printf("ExecReload for %s.service requires MAINPID, but no runtime main process is available. Reloading dinit service description only.\n", unitName)
			return runDinitctlWithStatus("reload", dinitName)
		}
		fmt.Printf("ExecReload for %s.service requires MAINPID, but no runtime main process is available. Use restart instead.\n", unitName)
		return 1
	}
	if mainPID != "" {
		allEnvs["MAINPID"] = mainPID
	}
	reloadCmd := compileExecReload(unit, allEnvs)
	if reloadCmd != "" {
		cmd := exec.Command("/bin/sh", "-c", reloadCmd)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = os.Environ()
		for k, v := range allEnvs {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		if mainPID != "" {
			cmd.Env = append(cmd.Env, "MAINPID="+mainPID)
		}
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode()
			}
			fmt.Printf("reload failed: %v\n", err)
			return 1
		}
		return 0
	}
	dinitName := backendServiceNameForUnit(unitName)
	status := dinitStatus(dinitName)
	if status != nil {
		fmt.Printf("No ExecReload for %s.service; reloading dinit service description only. Use restart for runtime changes.\n", unitName)
		return runDinitctlWithStatus("reload", dinitName)
	}
	fmt.Printf("No ExecReload for %s.service. Use restart to apply changes.\n", unitName)
	return 1
}

func isUnitStarted(unitName string) bool {
	cmd := dinitctlCommand("is-started", backendServiceNameForUnit(unitName))
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

func stopBackendIfKnown(unitName string) int {
	backendName := backendServiceNameForUnit(unitName)
	if !isDinitServiceKnown(backendName) {
		return 0
	}
	return runDinitctlWithStatus("stop", backendName)
}

func restartUnit(unitName string) int {
	if _, err := parseSystemdUnit(unitName); err != nil {
		fmt.Println(err)
		return 1
	}
	dependents := reverseRestartClosure(unitName)
	if len(dependents) > 0 {
		fmt.Printf("Stopping dependents: %s\n", strings.Join(dependents, ", "))
		for _, dependent := range dependents {
			if code := stopBackendIfKnown(dependent); code != 0 {
				return code
			}
		}
	}
	if code := stopBackendIfKnown(unitName); code != 0 {
		return code
	}
	fmt.Printf("Restarting target: %s\n", unitName)
	recursiveInstall(unitName, make(map[string]bool), installOptionsForUnit(unitName, true))
	if !recursiveStart(unitName, make(map[string]bool)) {
		return 1
	}
	if len(dependents) > 0 {
		restore := reverseStrings(dependents)
		fmt.Printf("Restoring dependents: %s\n", strings.Join(restore, ", "))
		for _, dependent := range restore {
			recursiveInstall(dependent, make(map[string]bool), installOptionsForUnit(dependent, true))
			if !recursiveStart(dependent, make(map[string]bool)) {
				return 1
			}
		}
	}
	return 0
}

func currentUserSessionEnv() map[string]string {
	env := map[string]string{}
	for _, key := range []string{"HOME", "USER", "LOGNAME", "PATH", "SHELL", "TMPDIR", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "DBUS_SESSION_BUS_ADDRESS"} {
		env[key] = os.Getenv(key)
	}
	return env
}

func printUserEnvDiagnostics() {
	managed := userSessionEnvDefaults()
	current := currentUserSessionEnv()
	mismatchKeys := []string{}
	fields := make([][2]string, 0, 6)
	for _, item := range []struct {
		label string
		key   string
	}{
		{"Caller HOME", "HOME"},
		{"Caller XDG RT", "XDG_RUNTIME_DIR"},
		{"Caller XDG CFG", "XDG_CONFIG_HOME"},
		{"Caller XDG ST", "XDG_STATE_HOME"},
		{"Caller XDG CA", "XDG_CACHE_HOME"},
		{"Caller DBUS", "DBUS_SESSION_BUS_ADDRESS"},
	} {
		value := current[item.key]
		if value == "" {
			value = "-"
		}
		fields = append(fields, [2]string{item.label, value})
		if current[item.key] != managed[item.key] {
			mismatchKeys = append(mismatchKeys, item.key)
		}
	}
	printSection("Caller Environment", fields)
	if len(mismatchKeys) > 0 {
		printSection("Diagnostics", [][2]string{{"Env Warning", "calling environment differs from managed service environment: " + strings.Join(mismatchKeys, ", ")}})
	}
}

func showUnit(unitName string) int {
	if snapshot, ok := queryUnitSnapshotViaSysvision(unitName); ok {
		return showUnitFromSnapshot(unitName, snapshot)
	}
	unit, err := parseSystemdUnit(unitName)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	socketUnit, _ := parseOptionalSocketUnit(unitName)
	dinitName := backendServiceNameForUnit(unitName)
	loggerName := loggerServiceName(dinitName)
	status := dinitStatus(dinitName)
	runtimeState := map[string]string(nil)
	managedBy := "dinit"
	managementMode := managedServiceModeForUnit(unit, socketUnit)
	if managementMode != managedDirect {
		managedBy = "sys-notifyd"
		runtimeState = parseKeyValueFile(notifydStatePath(unitName, managementMode))
	}
	allEnvs := mergedServiceEnv(unit)
	backendCmd := compileExecStart(unit, allEnvs)
	reloadCmd := compileExecReload(unit, allEnvs)
	stopCmd := compileExecStop(unit, allEnvs)

	fmt.Printf("%s %s\n\n", colorize("servicectl show", styleBold, styleCyan), colorize(unitName+".service", styleBold))
	printSection("Unit", [][2]string{{"Name", unitName + ".service"}, {"Mode", config.Mode}, {"Description", unit.Description}, {"Source", unit.SourcePath}, {"Managed By", managedBy}, {"Activation Model", string(managementMode)}, {"Dinit", dinitName}, {"Logger", loggerName}, {"Log Tag", journalIdentifier(unitName)}})
	mainPID := ""
	managerRuntimePID := ""
	if runtimeState != nil {
		mainPID = runtimeState["main_pid"]
		managerRuntimePID = statusValue(status, "Process ID")
	}
	if mainPID == "" && status != nil {
		mainPID = status["Process ID"]
	}
	printSection("Runtime", [][2]string{{"State", statusValue(status, "State")}, {"Activation", statusValue(status, "Activation")}, {"Process ID", statusValue(status, "Process ID")}, {"Manager PID", managerRuntimePID}, {"Main PID", mainPID}, {"Phase", mapValue(runtimeState, "phase")}, {"Child", mapValue(runtimeState, "child_state")}, {"Status", mapValue(runtimeState, "status")}, {"Failure", mapValue(runtimeState, "failure")}, {"Notify Sock", notifySocketPath(unitName, unit, socketUnit)}, {"State File", managedStateFilePath(unitName, unit, socketUnit)}})
	printSection("Exec", [][2]string{{"Service Type", unit.Type}, {"ExecStart", backendCmd}, {"ExecReload", reloadCmd}, {"ExecStop", stopCmd}, {"WorkingDir", unit.WorkingDirectory}, {"User", unit.User}, {"Group", unit.Group}})
	printSection("Sockets", [][2]string{{"Socket Unit", socketSource(socketUnit)}, {"Listen", socketListenValue(socketUnit)}, {"FD Names", formatList(socketFDNames(socketUnit))}})
	printEnvironmentSection(unit)
	printSection("Dependencies", [][2]string{{"Requires", formatList(unit.Requires)}, {"Wants", formatList(unit.Wants)}, {"After", formatList(unit.After)}, {"BindsTo", formatList(unit.BindsTo)}, {"PartOf", formatList(unit.PartOf)}})
	printSection("Directories", [][2]string{{"RuntimeDir", formatList(unit.RuntimeDirectory)}, {"StateDir", formatList(unit.StateDirectory)}, {"LogsDir", formatList(unit.LogsDirectory)}})
	return 0
}

func showUnitFromSnapshot(unitName string, snapshot visionapi.UnitSnapshot) int {
	unit, err := parseSystemdUnit(unitName)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	socketUnit, _ := parseOptionalSocketUnit(unitName)
	allEnvs := mergedServiceEnv(unit)
	backendCmd := compileExecStart(unit, allEnvs)
	reloadCmd := compileExecReload(unit, allEnvs)
	stopCmd := compileExecStop(unit, allEnvs)
	orchName := s6OrchestrdServiceName(unitName)
	orchState := orchestratorState(unitName)
	orchPID := orchestratorPID(unitName)
	enabledState := "disabled"
	if isEnabledWithS6(unitName) {
		enabledState = "enabled"
	}
	busMeta := currentBusMeta()
	busConnected := "offline"
	if busMeta.available {
		if busMeta.connected {
			busConnected = "connected"
		} else {
			busConnected = "degraded"
		}
	}

	fmt.Printf("%s %s\n\n", colorize("servicectl show", styleBold, styleCyan), colorize(unitName+".service", styleBold))
	managementMode := managedServiceModeForUnit(unit, socketUnit)
	printSection("Unit", [][2]string{{"Name", unitName + ".service"}, {"Mode", config.Mode}, {"Description", unit.Description}, {"Source", unit.SourcePath}, {"Managed By", snapshot.ManagedBy}, {"Activation Model", string(managementMode)}, {"Dinit", snapshot.DinitName}, {"Logger", snapshot.LoggerName}, {"Log Tag", journalIdentifier(unitName)}})
	printSection("Runtime", [][2]string{{"State", emptyDash(snapshot.State)}, {"Activation", emptyDash(snapshot.Activation)}, {"Process ID", emptyDash(snapshot.ProcessID)}, {"Manager PID", emptyDash(snapshot.ManagerPID)}, {"Main PID", emptyDash(snapshot.MainPID)}, {"Phase", emptyDash(snapshot.Phase)}, {"Child", emptyDash(snapshot.ChildState)}, {"Status", emptyDash(snapshot.Status)}, {"Failure", emptyDash(snapshot.Failure)}, {"Notify Sock", emptyDash(snapshot.NotifySocket)}, {"State File", emptyDash(snapshot.StateFile)}})
	printSection("Orchestration", [][2]string{{"Enabled", enabledState}, {"Orchestrator", orchName}, {"Orchestrator Scope", config.Mode}, {"Orchestrator State", emptyDash(mapValue(orchState, "state"))}, {"Orchestrator PID", emptyDash(orchPID)}, {"Orchestrator State File", orchestratorStateFilePath(unitName)}, {"Last Orchestration Event", emptyDash(mapValue(orchState, "state"))}, {"Last Orchestration Reason", emptyDash(mapValue(orchState, "reason"))}})
	printSection("S6", [][2]string{{"S6 Service", orchName}, {"S6 Bundle", s6BundleName()}, {"S6 Source Root", s6SourceRoot()}, {"S6 Source Dir", s6OrchestrdSourceDir(unitName)}, {"In Default Bundle", boolWord(s6ContentsContain(s6DefaultContentsPath(), s6BundleName()) && s6ContentsContain(s6DefaultContentsPath(), s6SysvisiondServiceName()) && s6ContentsContain(s6DefaultContentsPath(), s6ServicectlAPIServiceName()))}, {"In Enable Bundle", boolWord(s6ContentsContain(s6BundleContentsPath(), orchName))}})
	printSection("Control Plane", [][2]string{{"Servicectl API Socket", visionapi.ServicectlSocketPath(userMode(), runtimeDir())}, {"Servicectl Events Socket", visionapi.ServicectlEventsSocketPath(userMode(), runtimeDir())}, {"Sysvisiond Socket", visionapi.SysvisionSocketPath(userMode(), runtimeDir())}, {"Sysvisiond Ingress", visionapi.SysvisionIngressSocketPath(userMode(), runtimeDir())}, {"Bus Connected", busConnected}, {"Bus Error", emptyDash(busMeta.errorText)}})
	printSection("Exec", [][2]string{{"Service Type", unit.Type}, {"ExecStart", backendCmd}, {"ExecReload", reloadCmd}, {"ExecStop", stopCmd}, {"WorkingDir", unit.WorkingDirectory}, {"User", unit.User}, {"Group", unit.Group}})
	printSection("Sockets", [][2]string{{"Socket Unit", socketSource(socketUnit)}, {"Listen", socketListenValue(socketUnit)}, {"FD Names", formatList(socketFDNames(socketUnit))}})
	printEnvironmentSection(unit)
	printSection("Dependencies", [][2]string{{"Requires", formatList(unit.Requires)}, {"Wants", formatList(unit.Wants)}, {"After", formatList(unit.After)}, {"BindsTo", formatList(unit.BindsTo)}, {"PartOf", formatList(unit.PartOf)}})
	printSection("Directories", [][2]string{{"RuntimeDir", formatList(unit.RuntimeDirectory)}, {"StateDir", formatList(unit.StateDirectory)}, {"LogsDir", formatList(unit.LogsDirectory)}})
	return 0
}

func recursiveStart(unitName string, visited map[string]bool) bool {
	cleanName := strings.TrimSuffix(unitName, ".service")
	if visited[cleanName] {
		return true
	}
	visited[cleanName] = true

	unit, err := parseSystemdUnit(cleanName)
	if err != nil {
		return false
	}
	socketUnit, _ := parseOptionalSocketUnit(cleanName)

	for _, d := range hardStartDependencies(unit) {
		if _, ok := resolvedDependencyServiceName(d); ok {
			recursiveInstall(strings.TrimSuffix(resolveUnitAlias(d), ".service"), make(map[string]bool), installOptionsForUnit(strings.TrimSuffix(resolveUnitAlias(d), ".service"), false))
			if shouldSkipFailedDependency(d) {
				fmt.Printf("Skipping failed auxiliary dependency: %s\n", strings.TrimSuffix(resolveUnitAlias(d), ".service"))
				continue
			}
			if !recursiveStart(strings.TrimSuffix(resolveUnitAlias(d), ".service"), visited) {
				return false
			}
		}
	}

	for _, d := range degradableStartDependencies(unit) {
		if _, ok := resolvedDependencyServiceName(d); ok {
			depName := strings.TrimSuffix(resolveUnitAlias(d), ".service")
			recursiveInstall(depName, make(map[string]bool), installOptionsForUnit(depName, false))
			if shouldSkipFailedDependency(d) {
				fmt.Printf("Skipping failed degradable dependency: %s\n", depName)
				continue
			}
			if !recursiveStart(depName, visited) {
				fmt.Printf("warning: degradable dependency %s failed during preflight; continuing with %s\n", depName, cleanName)
			}
		}
	}

	mode := managedServiceModeForUnit(unit, socketUnit)
	if mode != managedDirect {
		if !isUnitStarted(cleanName) {
			if mode == managedSocketd {
				if err := cleanupStaleSocketArtifacts(cleanName, socketUnit); err != nil {
					fmt.Println(err)
					return false
				}
			}
			if !runDinitctl("start", managedServiceName(cleanName, mode)) {
				return false
			}
		}
	} else if !isUnitStarted(cleanName) {
		if !runDinitctl("start", cleanName) {
			return false
		}
	}
	return true
}

func runDinitctl(args ...string) bool {
	out, _, err := dinitctlOutput(args...)
	if err != nil {
		if out != "" {
			fmt.Println(out)
		} else {
			fmt.Println(oneLineError("dinitctl failed", err))
		}
		return false
	}
	return true
}

func runDinitctlWithStatus(args ...string) int {
	out, code, err := dinitctlOutput(args...)
	if err == nil {
		return 0
	}
	if out != "" {
		fmt.Println(out)
	} else {
		fmt.Println(oneLineError("dinitctl failed", err))
	}
	if code != 0 {
		return code
	}
	return 1
}

func dinitctlCommand(args ...string) *exec.Cmd {
	if userMode() {
		args = append([]string{"--user"}, args...)
	}
	return exec.Command("dinitctl", args...)
}

func isDinitServiceKnown(name string) bool {
	cmd := dinitctlCommand("status", name)
	return cmd.Run() == nil
}

func printList() int {
	out, code, err := dinitctlOutput("list")
	if err != nil {
		if out != "" {
			fmt.Println(out)
		} else {
			fmt.Println(oneLineError("list failed", err))
		}
		return code
	}
	if out != "" {
		fmt.Println(out)
	}
	return 0
}

func isActive(unitName string) int {
	if isUnitStarted(unitName) {
		fmt.Println(statusColor("active"))
		return 0
	}
	fmt.Println(statusColor("inactive"))
	return 1
}

func isFailed(unitName string) int {
	out, code, err := dinitctlOutput("is-failed", backendServiceNameForUnit(unitName))
	if err == nil {
		fmt.Println(statusColor("failed"))
		return 0
	}
	if out != "" && !strings.Contains(strings.ToLower(out), "not loaded") {
		fmt.Println(out)
		return code
	}
	fmt.Println(statusColor("ok"))
	return 1
}

func killUnit(unitName string, signalName string) int {
	signalName = strings.TrimSpace(signalName)
	if signalName == "" {
		signalName = "TERM"
	}
	if code := runDinitctlWithStatus("signal", signalName, backendServiceNameForUnit(unitName)); code != 0 {
		return code
	}
	fmt.Printf("%s %s (%s)\n", colorize("Sent signal", styleCyan), unitName, signalName)
	return 0
}

func dinitPassthrough(args []string) int {
	if len(args) == 0 {
		fmt.Println("Usage: servicectl [--user] dinit <args...>")
		return 1
	}
	cmd := dinitctlCommand(args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 1
}

func installOptionsForUnit(unitName string, freshLoad bool) installOptions {
	serviceName := dinitNameForUnit(unitName)
	loggerName := loggerServiceName(serviceName)
	targets := make(map[string]bool)
	if freshLoad {
		targets[serviceName] = true
		targets[loggerName] = true
	}
	return installOptions{freshLoadOnChange: targets}
}

func ensureUserModeReady() error {
	if !userMode() {
		return nil
	}
	if strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")) == "" {
		return fmt.Errorf("user mode requires XDG_RUNTIME_DIR to be set in the calling environment")
	}
	if err := dinitctlCommand("list").Run(); err == nil {
		syncUserDinitEnvironment()
		return nil
	}
	if err := startUserDinitInstance(); err != nil {
		return err
	}
	for range 20 {
		if err := dinitctlCommand("list").Run(); err == nil {
			syncUserDinitEnvironment()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("failed to start dinit user instance")
}

func ensureMinimalUserBootService() error {
	if !userMode() {
		return nil
	}
	if err := os.MkdirAll(config.DinitServiceDir, 0755); err != nil {
		return fmt.Errorf("create user dinit service directory: %w", err)
	}
	bootPath := filepath.Join(config.DinitServiceDir, "boot")
	if _, err := os.Stat(bootPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat user boot service: %w", err)
	}
	if err := os.WriteFile(bootPath, []byte("type = internal\n"), 0644); err != nil {
		return fmt.Errorf("write user boot service: %w", err)
	}
	return nil
}

func startUserDinitInstance() error {
	if err := ensureMinimalUserBootService(); err != nil {
		return err
	}
	if err := os.MkdirAll(config.DinitGenDir, 0755); err != nil {
		return fmt.Errorf("create user generated dinit directory: %w", err)
	}
	logPath := filepath.Join(runtimeDir(), "dinit-user.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open user dinit log file: %w", err)
	}
	defer logFile.Close()

	args := []string{"--user", "--container", "-b", "/", "-d", config.DinitServiceDir, "-d", config.DinitGenDir, "boot"}
	cmd := exec.Command("dinit", args...)
	cmd.Env = environmentWithOverrides(os.Environ(), userSessionEnvDefaults())
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start dinit user instance: %w", err)
	}
	return nil
}

func printLogs(unitName string, follow bool, lines string) {
	if follow {
		followSyslog(unitName)
		return
	}
	args := []string{"--no-pager"}
	if userMode() {
		args = append(args, "--user")
	}
	if strings.TrimSpace(lines) != "" {
		args = append(args, "-n", strings.TrimSpace(lines))
	}
	cmd := exec.Command("journalctl", args...)
	out, err := cmd.CombinedOutput()
	filtered := filterLogLines(string(out), journalIdentifier(unitName))
	if err == nil && len(filtered) > 0 {
		for _, line := range filtered {
			fmt.Println(line)
		}
		return
	}
	printSyslogFallback(unitName, lines)
}

func followSyslog(unitName string) {
	identifier := journalIdentifier(unitName)
	paths := []string{"/var/log/messages", "/var/log/syslog", "/var/log/daemon.log", "/var/log/user.log"}
	offsets := make(map[string]int64, len(paths))
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil {
			offsets[path] = info.Size()
		}
	}
	for {
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			offset := offsets[path]
			if offset > info.Size() {
				offset = 0
			}
			if offset == info.Size() {
				offsets[path] = offset
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				f.Close()
				continue
			}
			content, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				continue
			}
			offsets[path] = info.Size()
			for _, line := range strings.Split(string(content), "\n") {
				line = strings.TrimRight(line, "\r\n")
				if line == "" {
					continue
				}
				if strings.Contains(line, identifier) {
					fmt.Println(line)
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func filterLogLines(content string, identifier string) []string {
	matches := make([]string, 0)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if strings.Contains(line, identifier) {
			matches = append(matches, line)
		}
	}
	return matches
}

func printSyslogFallback(unitName string, lines string) {
	limit := 50
	if strings.TrimSpace(lines) != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(lines)); err == nil && n > 0 {
			limit = n
		}
	}
	identifier := journalIdentifier(unitName)
	paths := []string{"/var/log/messages", "/var/log/syslog", "/var/log/daemon.log", "/var/log/user.log"}
	matches := make([]string, 0)
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			if strings.Contains(line, identifier) {
				matches = append(matches, line)
			}
		}
	}
	if len(matches) == 0 {
		fmt.Println("-- No entries --")
		return
	}
	if len(matches) > limit {
		matches = matches[len(matches)-limit:]
	}
	for _, line := range matches {
		fmt.Println(line)
	}
}

func main() {
	args := os.Args[1:]
	if len(args) == 1 {
		switch args[0] {
		case "help", "-h", "--help":
			printHelp()
			return
		}
	}
	userFlag := false
	for len(args) > 0 {
		switch args[0] {
		case "--user":
			userFlag = true
			args = args[1:]
		default:
			goto parsedFlags
		}
	}

parsedFlags:
	config = buildConfig(userFlag)

	if len(args) < 1 {
		printHelp()
		return
	}
	if len(args) == 1 {
		switch args[0] {
		case "help", "-h", "--help":
			printHelp()
			return
		}
	}
	if len(args) == 1 && args[0] != "list" && args[0] != "serve-api" {
		printHelp()
		return
	}

	action, targets := args[0], args[1:]
	if action == "serve-api" {
		os.Exit(servicectlAPIServer())
	}
	logsFollow := false
	logsLines := ""
	killSignal := "TERM"
	if action == "logs" {
		filtered := make([]string, 0, len(targets))
		for i := 0; i < len(targets); i++ {
			t := targets[i]
			switch t {
			case "-f":
				logsFollow = true
			case "-n":
				if i+1 >= len(targets) {
					fmt.Println("logs requires a value after -n")
					return
				}
				i++
				logsLines = targets[i]
			default:
				filtered = append(filtered, t)
			}
		}
		targets = filtered
		if len(targets) == 0 {
			fmt.Println("Usage: servicectl [--user] logs [-f] [-n lines] [unit...]")
			return
		}
	}
	if action == "kill" {
		if len(targets) == 0 {
			fmt.Println("Usage: servicectl [--user] kill <unit...> [signal]")
			return
		}
		if len(targets) > 1 {
			last := strings.TrimSpace(targets[len(targets)-1])
			if last != "" && !strings.Contains(last, ".service") {
				upper := strings.ToUpper(strings.TrimPrefix(last, "SIG"))
				switch upper {
				case "TERM", "HUP", "INT", "QUIT", "KILL", "USR1", "USR2":
					killSignal = upper
					targets = targets[:len(targets)-1]
				}
			}
		}
	}

	if action != "logs" && action != "dinit" {
		if err := ensureUserModeReady(); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	}
	if action == "dinit" {
		if userMode() {
			if err := ensureUserModeReady(); err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
		}
		os.Exit(dinitPassthrough(targets))
	}
	if action == "list" {
		os.Exit(printList())
	}
	if action == "enable" || action == "disable" || action == "is-enabled" {
		if !s6Available() {
			fmt.Println("s6 backend is not available")
			os.Exit(1)
		}
	}

	exitCode := 0
	for _, t := range targets {
		cleanName := strings.TrimSuffix(t, ".service")

		switch action {
		case "start":
			if _, err := parseSystemdUnit(cleanName); err != nil {
				fmt.Println(err)
				publishServicectlCommandEvent(action, cleanName, "error")
				exitCode = 1
				continue
			}
			recursiveInstall(cleanName, make(map[string]bool), installOptionsForUnit(cleanName, false))
			if !recursiveStart(cleanName, make(map[string]bool)) {
				publishServicectlCommandEvent(action, cleanName, "error")
				exitCode = 1
				continue
			}
			fmt.Printf("%s %s\n", colorize("Started", styleGreen), cleanName)
			publishServicectlCommandEvent(action, cleanName, "ok")
		case "stop":
			if !runDinitctl("stop", backendServiceNameForUnit(cleanName)) {
				publishServicectlCommandEvent(action, cleanName, "error")
				exitCode = 1
				continue
			}
			fmt.Printf("%s %s\n", colorize("Stopped", styleYellow), cleanName)
			publishServicectlCommandEvent(action, cleanName, "ok")
		case "status":
			printStatus(cleanName)
		case "logs":
			printLogs(cleanName, logsFollow, logsLines)
		case "list":
			exitCode = printList()
		case "is-active":
			if code := isActive(cleanName); code != 0 {
				exitCode = code
			}
		case "is-failed":
			if code := isFailed(cleanName); code != 0 {
				exitCode = code
			}
		case "kill":
			if code := killUnit(cleanName, killSignal); code != 0 {
				publishServicectlCommandEvent(action, cleanName, "error")
				exitCode = code
			} else {
				publishServicectlCommandEvent(action, cleanName, "ok")
			}
		case "reload":
			if code := reloadUnit(cleanName); code != 0 {
				publishServicectlCommandEvent(action, cleanName, "error")
				exitCode = code
				continue
			}
			fmt.Printf("%s %s\n", colorize("Reloaded", styleCyan), cleanName)
			publishServicectlCommandEvent(action, cleanName, "ok")
		case "show":
			if code := showUnit(cleanName); code != 0 {
				exitCode = code
			}
		case "restart":
			if code := restartUnit(cleanName); code != 0 {
				publishServicectlCommandEvent(action, cleanName, "error")
				exitCode = code
				continue
			}
			fmt.Printf("%s %s\n", colorize("Restarted", styleGreen), cleanName)
			publishServicectlCommandEvent(action, cleanName, "ok")
		case "enable":
			if _, err := parseSystemdUnit(cleanName); err != nil {
				fmt.Println(err)
				exitCode = 1
				continue
			}
			if err := enableWithS6(cleanName); err != nil {
				fmt.Println(oneLineError("enable unit with s6", err))
				exitCode = 1
				continue
			}
			fmt.Printf("Enabled %s\n", cleanName)
		case "disable":
			if err := disableWithS6(cleanName); err != nil {
				fmt.Println(oneLineError("disable unit with s6", err))
				exitCode = 1
				continue
			}
			_ = stopUnit(cleanName)
			fmt.Printf("Disabled %s\n", cleanName)
		case "is-enabled":
			if isEnabledWithS6(cleanName) {
				fmt.Println("enabled")
			} else {
				fmt.Println("disabled")
				exitCode = 1
			}
		default:
			fmt.Printf("Unknown command: %s\n\n", action)
			printHelp()
			os.Exit(1)
		}
	}
	os.Exit(exitCode)
}
