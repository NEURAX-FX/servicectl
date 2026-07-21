package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

const systemDinitSocketPath = "/run/dinitctl"

var dinitProcessIDPattern = regexp.MustCompile(`(?m)^\s*Process ID:\s*([0-9]+)\s*$`)

type peerCredentials struct {
	PID int
	UID uint32
	GID uint32
}

type workerRoute struct {
	Scope         string
	Unit          string
	LoggerService string
}

type routeDependencies struct {
	cmdline          func(int) ([]string, error)
	environment      func(int) (map[string]string, error)
	executable       func(int) (string, error)
	executableSecure func(string) error
	pathSecure       func(string, uint32) error
	dinitPID         func(string, string) (int, error)
}

func defaultRouteDependencies() routeDependencies {
	return routeDependencies{
		cmdline:          readProcCmdline,
		environment:      readProcEnvironment,
		executable:       readSecureProcExecutable,
		executableSecure: validateApprovedSysLogdPath,
		pathSecure:       validatePeerOwnedPath,
		dinitPID:         queryDinitServicePID,
	}
}

func parseWorkerRoute(args []string) (workerRoute, error) {
	if len(args) == 0 {
		return workerRoute{}, fmt.Errorf("worker command line is empty")
	}
	flags := flag.NewFlagSet("sys-logd-worker-route", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var worker bool
	var scope, unit, loggerService string
	flags.BoolVar(&worker, "worker", false, "")
	flags.StringVar(&scope, "scope", "", "")
	flags.StringVar(&unit, "unit", "", "")
	flags.StringVar(&loggerService, "logger-service", "", "")
	flags.String("service", "", "")
	flags.String("spill-dir", "", "")
	flags.String("socket", "", "")
	if err := flags.Parse(args[1:]); err != nil {
		return workerRoute{}, err
	}
	if !worker || flags.NArg() != 0 {
		return workerRoute{}, fmt.Errorf("process is not a sys-logd worker")
	}
	if scope != "system" && scope != "user" {
		return workerRoute{}, fmt.Errorf("invalid worker scope %q", scope)
	}
	canonicalUnit, err := canonicalJournalUnit(unit)
	if err != nil {
		return workerRoute{}, err
	}
	if !validDinitServiceName(loggerService) {
		return workerRoute{}, fmt.Errorf("invalid logger service name %q", loggerService)
	}
	return workerRoute{Scope: scope, Unit: canonicalUnit, LoggerService: loggerService}, nil
}

func resolvePeerRoute(peer peerCredentials, deps routeDependencies) (logRoute, error) {
	if peer.PID <= 0 {
		return logRoute{}, fmt.Errorf("invalid peer PID %d", peer.PID)
	}
	if deps.cmdline == nil || deps.environment == nil || deps.executable == nil ||
		deps.executableSecure == nil || deps.pathSecure == nil || deps.dinitPID == nil {
		return logRoute{}, fmt.Errorf("route dependencies are incomplete")
	}
	executable, err := deps.executable(peer.PID)
	if err != nil {
		return logRoute{}, fmt.Errorf("read peer executable: %w", err)
	}
	if err := deps.executableSecure(executable); err != nil {
		return logRoute{}, err
	}
	args, err := deps.cmdline(peer.PID)
	if err != nil {
		return logRoute{}, fmt.Errorf("read peer command line: %w", err)
	}
	worker, err := parseWorkerRoute(args)
	if err != nil {
		return logRoute{}, err
	}

	dinitSocket := systemDinitSocketPath
	if worker.Scope == "user" {
		environment, err := deps.environment(peer.PID)
		if err != nil {
			return logRoute{}, fmt.Errorf("read peer environment: %w", err)
		}
		runtimeDir := filepath.Clean(strings.TrimSpace(environment["XDG_RUNTIME_DIR"]))
		if runtimeDir == "." || runtimeDir == "/" || !filepath.IsAbs(runtimeDir) {
			return logRoute{}, fmt.Errorf("peer XDG_RUNTIME_DIR %q is invalid", environment["XDG_RUNTIME_DIR"])
		}
		if err := deps.pathSecure(runtimeDir, peer.UID); err != nil {
			return logRoute{}, fmt.Errorf("unsafe peer runtime directory: %w", err)
		}
		dinitSocket = filepath.Join(runtimeDir, "dinitctl")
		if err := deps.pathSecure(dinitSocket, peer.UID); err != nil {
			return logRoute{}, fmt.Errorf("unsafe peer dinit socket: %w", err)
		}
	}
	managerPID, err := deps.dinitPID(dinitSocket, worker.LoggerService)
	if err != nil {
		return logRoute{}, fmt.Errorf("query logger service %s: %w", worker.LoggerService, err)
	}
	if managerPID != peer.PID {
		return logRoute{}, fmt.Errorf("logger service %s PID is %d, peer PID is %d", worker.LoggerService, managerPID, peer.PID)
	}
	return logRoute{
		Scope:         worker.Scope,
		Unit:          worker.Unit,
		Identifier:    "servicectl[" + strings.TrimSuffix(worker.Unit, ".service") + "]",
		LoggerService: worker.LoggerService,
		UID:           peer.UID,
		GID:           peer.GID,
		PID:           peer.PID,
	}, nil
}

func validDinitServiceName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("_.@:+-", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func readProcCmdline(pid int) ([]string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(data, []byte{0})
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) != 0 {
			args = append(args, string(part))
		}
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("process %d command line is empty", pid)
	}
	return args, nil
}

func readProcEnvironment(pid int) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil, err
	}
	environment := make(map[string]string)
	for _, entry := range bytes.Split(data, []byte{0}) {
		name, value, ok := strings.Cut(string(entry), "=")
		if ok && name != "" {
			environment[name] = value
		}
	}
	return environment, nil
}

func readSecureProcExecutable(pid int) (string, error) {
	procPath := filepath.Join("/proc", strconv.Itoa(pid), "exe")
	target, err := os.Readlink(procPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(procPath)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("peer executable stat has unexpected type")
	}
	if stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
		return "", fmt.Errorf("peer executable is not root-owned and immutable to non-root users")
	}
	return strings.TrimSuffix(target, " (deleted)"), nil
}

func validateApprovedSysLogdPath(path string) error {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	for _, approved := range []string{
		"/usr/bin/sys-logd",
		"/usr/local/bin/sys-logd",
		"/root/.local/bin/sys-logd",
	} {
		if cleanPath == approved {
			return nil
		}
	}
	return fmt.Errorf("peer executable %q is not an approved sys-logd path", path)
}

func validatePeerOwnedPath(path string, uid uint32) error {
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) || cleanPath == string(filepath.Separator) {
		return fmt.Errorf("%s is not absolute", path)
	}
	current := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(cleanPath, current), string(filepath.Separator))
	var info os.FileInfo
	for index, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		var err error
		info, err = os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s contains symbolic link component %s", path, current)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("%s component %s is not a directory", path, current)
		}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s stat has unexpected type", path)
	}
	if stat.Uid != uid {
		return fmt.Errorf("%s is owned by UID %d, want %d", path, stat.Uid, uid)
	}
	if info.IsDir() && info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("%s is group or world writable", path)
	}
	return nil
}

func queryDinitServicePID(socketPath, service string) (int, error) {
	output, err := exec.Command("dinitctl", "--socket-path", socketPath, "status", service).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("dinitctl status failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	match := dinitProcessIDPattern.FindSubmatch(output)
	if len(match) != 2 {
		return 0, fmt.Errorf("dinitctl status did not report a process ID")
	}
	pid, err := strconv.Atoi(string(match[1]))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid dinit process ID %q", match[1])
	}
	return pid, nil
}
