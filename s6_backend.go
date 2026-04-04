package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func siblingBinaryPath(name string) string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(self), name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func servicectlBinaryPath() string {
	if candidate := siblingBinaryPath("servicectl"); candidate != "" {
		return candidate
	}
	if candidate, err := exec.LookPath("servicectl"); err == nil {
		return candidate
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		candidate := filepath.Join(home, ".local", "bin", "servicectl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		candidate = filepath.Join(home, "servicectl", "servicectl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "servicectl"
}

func sysvisiondBinaryPath() string {
	if candidate := siblingBinaryPath("sysvisiond"); candidate != "" {
		return candidate
	}
	if candidate, err := exec.LookPath("sysvisiond"); err == nil {
		return candidate
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		candidate := filepath.Join(home, ".local", "bin", "sysvisiond")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		candidate = filepath.Join(home, "servicectl", "sysvisiond")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "sysvisiond"
}

func sysOrchestrdBinaryPath() string {
	if candidate := siblingBinaryPath("sys-orchestrd"); candidate != "" {
		return candidate
	}
	if candidate, err := exec.LookPath("sys-orchestrd"); err == nil {
		return candidate
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		candidate := filepath.Join(home, ".local", "bin", "sys-orchestrd")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		candidate = filepath.Join(home, "servicectl", "sys-orchestrd")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "sys-orchestrd"
}

func s6LiveEnabled() bool {
	value := strings.TrimSpace(os.Getenv("SERVICECTL_S6_LIVE"))
	return value == "1" || strings.EqualFold(value, "true")
}

func userBackendRoot() string {
	return filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
}

func s6SourceRoot() string {
	if userMode() {
		return filepath.Join(userBackendRoot(), "s6", "rc")
	}
	return "/s6/rc"
}

func s6BundleName() string {
	if userMode() {
		return "servicectl-user-enabled"
	}
	return "servicectl-enabled"
}

func s6SysvisiondServiceName() string {
	if userMode() {
		return "sysvisiond-user"
	}
	return "sysvisiond"
}

func s6ServicectlAPIServiceName() string {
	if userMode() {
		return "servicectl-user-api"
	}
	return "servicectl-api"
}

func s6SysvisiondSourceDir() string {
	return filepath.Join(s6SourceRoot(), s6SysvisiondServiceName())
}

func s6ServicectlAPISourceDir() string {
	return filepath.Join(s6SourceRoot(), s6ServicectlAPIServiceName())
}

func s6CompiledValidateDir() string {
	if userMode() {
		return filepath.Join(userBackendRoot(), "s6", "compiled.servicectl-user")
	}
	return "/run/s6/compiled.servicectl"
}

func s6LiveDir() string {
	if userMode() {
		return filepath.Join(userBackendRoot(), "s6", "state")
	}
	return "/run/s6/state"
}

func s6OrchestrdServiceName(unitName string) string {
	clean := strings.TrimSuffix(resolveUnitAlias(unitName), ".service")
	if userMode() {
		return clean + "-user-orchestrd"
	}
	return clean + "-orchestrd"
}

func s6OrchestrdSourceDir(unitName string) string {
	return filepath.Join(s6SourceRoot(), s6OrchestrdServiceName(unitName))
}

func s6ServicectlBundleDir() string {
	return filepath.Join(s6SourceRoot(), s6BundleName())
}

func s6BundleContentsPath() string {
	return filepath.Join(s6ServicectlBundleDir(), "contents")
}

func s6DefaultContentsPath() string {
	return filepath.Join(s6SourceRoot(), "default", "contents")
}

func s6Available() bool {
	for _, path := range []string{"/bin/s6-rc", "/bin/s6-rc-compile"} {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	if userMode() {
		return strings.TrimSpace(userBackendRoot()) != ""
	}
	info, err := os.Stat(s6SourceRoot())
	return err == nil && info.IsDir()
}

func ensureS6Bundle() error {
	if err := os.MkdirAll(s6SourceRoot(), 0755); err != nil {
		return err
	}
	defaultDir := filepath.Join(s6SourceRoot(), "default")
	if err := os.MkdirAll(defaultDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(defaultDir, "type"), []byte("bundle\n"), 0644); err != nil {
		return err
	}
	if _, err := os.Stat(s6DefaultContentsPath()); err != nil {
		if err := os.WriteFile(s6DefaultContentsPath(), nil, 0644); err != nil {
			return err
		}
	}
	bundleDir := s6ServicectlBundleDir()
	if err := os.MkdirAll(bundleDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "type"), []byte("bundle\n"), 0644); err != nil {
		return err
	}
	if _, err := os.Stat(s6BundleContentsPath()); err != nil {
		if err := os.WriteFile(s6BundleContentsPath(), nil, 0644); err != nil {
			return err
		}
	}
	defaultContents, _ := os.ReadFile(s6DefaultContentsPath())
	entries := uniqueSortedLines(string(defaultContents))
	if !containsString(entries, s6BundleName()) {
		entries = append(entries, s6BundleName())
		sort.Strings(entries)
		if err := os.WriteFile(s6DefaultContentsPath(), []byte(strings.Join(entries, "\n")+"\n"), 0644); err != nil {
			return err
		}
	}
	if !containsString(entries, s6SysvisiondServiceName()) {
		entries = append(entries, s6SysvisiondServiceName())
		sort.Strings(entries)
		if err := os.WriteFile(s6DefaultContentsPath(), []byte(strings.Join(entries, "\n")+"\n"), 0644); err != nil {
			return err
		}
	}
	if !containsString(entries, s6ServicectlAPIServiceName()) {
		entries = append(entries, s6ServicectlAPIServiceName())
		sort.Strings(entries)
		if err := os.WriteFile(s6DefaultContentsPath(), []byte(strings.Join(entries, "\n")+"\n"), 0644); err != nil {
			return err
		}
	}
	if err := ensureServicectlAPISource(); err != nil {
		return err
	}
	if err := ensureSysvisiondSource(); err != nil {
		return err
	}
	return nil
}

func ensureServicectlAPISource() error {
	serviceDir := s6ServicectlAPISourceDir()
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "type"), []byte("longrun\n"), 0644); err != nil {
		return err
	}
	runLine := servicectlBinaryPath()
	if userMode() {
		runLine += " --user"
	}
	runLine += " serve-api"
	runScript := strings.Join([]string{"#!/usr/bin/execlineb -P", runLine, ""}, "\n")
	if err := os.WriteFile(filepath.Join(serviceDir, "run"), []byte(runScript), 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(serviceDir, "dependencies"), nil, 0644)
}

func ensureSysvisiondSource() error {
	serviceDir := s6SysvisiondSourceDir()
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "type"), []byte("longrun\n"), 0644); err != nil {
		return err
	}
	runLine := sysvisiondBinaryPath()
	if userMode() {
		runLine += " --user"
	}
	runScript := strings.Join([]string{"#!/usr/bin/execlineb -P", runLine, ""}, "\n")
	if err := os.WriteFile(filepath.Join(serviceDir, "run"), []byte(runScript), 0755); err != nil {
		return err
	}
	depsContent := s6ServicectlAPIServiceName() + "\n"
	return os.WriteFile(filepath.Join(serviceDir, "dependencies"), []byte(depsContent), 0644)
}

func enableWithS6(unitName string) error {
	if !s6Available() {
		return fmt.Errorf("s6 backend is not available")
	}
	if err := ensureS6Bundle(); err != nil {
		return err
	}
	serviceDir := s6OrchestrdSourceDir(unitName)
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "type"), []byte("longrun\n"), 0644); err != nil {
		return err
	}
	runLine := sysOrchestrdBinaryPath()
	if userMode() {
		runLine += " --user"
	}
	runLine += " --unit " + strings.TrimSuffix(resolveUnitAlias(unitName), ".service") + ".service"
	runScript := strings.Join([]string{"#!/usr/bin/execlineb -P", runLine, ""}, "\n")
	if err := os.WriteFile(filepath.Join(serviceDir, "run"), []byte(runScript), 0755); err != nil {
		return err
	}
	deps := []string{}
	if !userMode() {
		deps = append(deps, "dinit")
	}
	deps = append(deps, s6SysvisiondServiceName())
	depsContent := ""
	if len(deps) > 0 {
		depsContent = strings.Join(deps, "\n") + "\n"
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "dependencies"), []byte(depsContent), 0644); err != nil {
		return err
	}
	entries, _ := os.ReadFile(s6BundleContentsPath())
	bundleEntries := uniqueSortedLines(string(entries))
	serviceName := s6OrchestrdServiceName(unitName)
	if !containsString(bundleEntries, serviceName) {
		bundleEntries = append(bundleEntries, serviceName)
		sort.Strings(bundleEntries)
		if err := os.WriteFile(s6BundleContentsPath(), []byte(strings.Join(bundleEntries, "\n")+"\n"), 0644); err != nil {
			return err
		}
	}
	if err := validateS6Sources(); err != nil {
		return err
	}
	if s6LiveEnabled() {
		if err := liveUpdateS6(); err != nil {
			return err
		}
		if err := liveStartS6(s6ServicectlAPIServiceName()); err != nil {
			return err
		}
		if err := liveStartS6(s6SysvisiondServiceName()); err != nil {
			return err
		}
		if err := liveStartS6(serviceName); err != nil {
			return err
		}
	}
	return nil
}

func disableWithS6(unitName string) error {
	if !s6Available() {
		return fmt.Errorf("s6 backend is not available")
	}
	serviceName := s6OrchestrdServiceName(unitName)
	if s6LiveEnabled() {
		if err := liveStopS6(serviceName); err != nil {
			return err
		}
	}
	entries, _ := os.ReadFile(s6BundleContentsPath())
	bundleEntries := uniqueSortedLines(string(entries))
	filtered := make([]string, 0, len(bundleEntries))
	for _, entry := range bundleEntries {
		if entry != serviceName {
			filtered = append(filtered, entry)
		}
	}
	content := ""
	if len(filtered) > 0 {
		content = strings.Join(filtered, "\n") + "\n"
	}
	if err := os.WriteFile(s6BundleContentsPath(), []byte(content), 0644); err != nil {
		return err
	}
	if err := os.RemoveAll(s6OrchestrdSourceDir(unitName)); err != nil {
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

func isEnabledWithS6(unitName string) bool {
	entries, err := os.ReadFile(s6BundleContentsPath())
	if err != nil {
		return false
	}
	return containsString(uniqueSortedLines(string(entries)), s6OrchestrdServiceName(unitName))
}

func validateS6Sources() error {
	compiled := s6CompiledValidateDir()
	_ = os.RemoveAll(compiled)
	if err := os.MkdirAll(filepath.Dir(compiled), 0755); err != nil {
		return err
	}
	text, code, err := commandOutput("/bin/s6-rc-compile", compiled, s6SourceRoot())
	if err == nil && code == 0 {
		return nil
	}
	return fmt.Errorf("s6-rc-compile failed: %s", strings.TrimSpace(text))
}

func liveUpdateS6() error {
	if _, err := os.Stat(s6LiveDir()); err != nil {
		return err
	}
	text, code, err := commandOutput("/bin/s6-rc-update", "-l", s6LiveDir(), s6CompiledValidateDir())
	if err == nil && code == 0 {
		return nil
	}
	return fmt.Errorf("s6-rc-update failed: %s", strings.TrimSpace(text))
}

func liveStartS6(service string) error {
	if _, err := os.Stat(s6LiveDir()); err != nil {
		return err
	}
	text, code, err := commandOutput("/bin/s6-rc", "-l", s6LiveDir(), "-u", "change", service)
	if err == nil && code == 0 {
		return nil
	}
	return fmt.Errorf("s6-rc start failed: %s", strings.TrimSpace(text))
}

func liveStopS6(service string) error {
	if _, err := os.Stat(s6LiveDir()); err != nil {
		return err
	}
	text, code, err := commandOutput("/bin/s6-rc", "-l", s6LiveDir(), "-d", "change", service)
	if err == nil && code == 0 {
		return nil
	}
	return fmt.Errorf("s6-rc stop failed: %s", strings.TrimSpace(text))
}

func commandOutput(name string, args ...string) (string, int, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err == nil {
		return text, 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return text, exitErr.ExitCode(), err
	}
	return text, 1, err
}

func uniqueSortedLines(content string) []string {
	seen := make(map[string]bool)
	entries := make([]string, 0)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		entries = append(entries, line)
	}
	sort.Strings(entries)
	return entries
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
