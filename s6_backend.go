package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type s6PlanePaths struct {
	SourceRoot      string
	CompiledDir     string
	LiveDir         string
	BundleDir       string
	BundleContents  string
	DefaultContents string
}

func s6PathsForMode(mode string) s6PlanePaths {
	cleanMode := strings.TrimSpace(strings.ToLower(mode))
	if cleanMode == "user" {
		base := filepath.Join(runtimeDir(), "servicectl", "s6")
		sourceRoot := filepath.Join(base, "rc")
		bundleDir := filepath.Join(sourceRoot, s6BundleName())
		return s6PlanePaths{
			SourceRoot:      sourceRoot,
			CompiledDir:     filepath.Join(base, "compiled.servicectl"),
			LiveDir:         filepath.Join(base, "state"),
			BundleDir:       bundleDir,
			BundleContents:  filepath.Join(bundleDir, "contents"),
			DefaultContents: filepath.Join(sourceRoot, "default", "contents"),
		}
	}
	sourceRoot := "/s6/rc"
	bundleDir := filepath.Join(sourceRoot, s6BundleName())
	return s6PlanePaths{
		SourceRoot:      sourceRoot,
		CompiledDir:     "/run/s6/compiled.servicectl",
		LiveDir:         "/run/s6/state",
		BundleDir:       bundleDir,
		BundleContents:  filepath.Join(bundleDir, "contents"),
		DefaultContents: filepath.Join(sourceRoot, "default", "contents"),
	}
}

func servicectlBinaryPath() string {
	return userBinaryPath("servicectl")
}

func sysvisiondBinaryPath() string {
	return userBinaryPath("sysvisiond")
}

func sysPropertydBinaryPath() string {
	return userBinaryPath("sys-propertyd")
}

func sysOrchestrdBinaryPath() string {
	return userBinaryPath("sys-orchestrd")
}

func s6LiveEnabled() bool {
	value := strings.TrimSpace(os.Getenv("SERVICECTL_S6_LIVE"))
	return value == "1" || strings.EqualFold(value, "true")
}

func s6SourceRoot() string {
	return s6PathsForMode(config.Mode).SourceRoot
}

func s6BundleName() string {
	return "servicectl-enabled"
}

func s6SysvisiondServiceName() string {
	return "sysvisiond"
}

func s6ServicectlAPIServiceName() string {
	return "servicectl-api"
}

func s6SysPropertydServiceName() string {
	return "sys-propertyd"
}

func s6SysvisiondSourceDir() string {
	return filepath.Join(s6SourceRoot(), s6SysvisiondServiceName())
}

func s6ServicectlAPISourceDir() string {
	return filepath.Join(s6SourceRoot(), s6ServicectlAPIServiceName())
}

func s6SysPropertydSourceDir() string {
	return filepath.Join(s6SourceRoot(), s6SysPropertydServiceName())
}

func s6CompiledValidateDir() string {
	return s6PathsForMode(config.Mode).CompiledDir
}

func s6LiveDir() string {
	return s6PathsForMode(config.Mode).LiveDir
}

func s6OrchestrdServiceName(unitName string) string {
	clean := strings.TrimSuffix(resolveUnitAlias(unitName), ".service")
	return clean + "-orchestrd"
}

func s6OrchestrdSourceDir(unitName string) string {
	return filepath.Join(s6SourceRoot(), s6OrchestrdServiceName(unitName))
}

func s6ServicectlBundleDir() string {
	return s6PathsForMode(config.Mode).BundleDir
}

func s6BundleContentsPath() string {
	return s6PathsForMode(config.Mode).BundleContents
}

func s6DefaultContentsPath() string {
	return s6PathsForMode(config.Mode).DefaultContents
}

func s6Available() bool {
	for _, path := range []string{"/bin/s6-rc", "/bin/s6-rc-compile"} {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	if userMode() {
		return true
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
	entries := uniqueLinesPreserveOrder(string(defaultContents))
	entries = appendUniqueLinePreserveOrder(entries, s6BundleName())
	entries = appendUniqueLinePreserveOrder(entries, s6SysvisiondServiceName())
	entries = appendUniqueLinePreserveOrder(entries, s6SysPropertydServiceName())
	entries = appendUniqueLinePreserveOrder(entries, s6ServicectlAPIServiceName())
	if err := writeLineFile(s6DefaultContentsPath(), entries); err != nil {
		return err
	}
	if err := ensureServicectlAPISource(); err != nil {
		return err
	}
	if err := ensureSysPropertydSource(); err != nil {
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
	runScript := strings.Join([]string{"#!/usr/bin/execlineb -P", runLine, ""}, "\n")
	if err := os.WriteFile(filepath.Join(serviceDir, "run"), []byte(runScript), 0755); err != nil {
		return err
	}
	depsContent := s6ServicectlAPIServiceName() + "\n" + s6SysPropertydServiceName() + "\n"
	return os.WriteFile(filepath.Join(serviceDir, "dependencies"), []byte(depsContent), 0644)
}

func ensureSysPropertydSource() error {
	serviceDir := s6SysPropertydSourceDir()
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "type"), []byte("longrun\n"), 0644); err != nil {
		return err
	}
	runLine := sysPropertydBinaryPath()
	runScript := strings.Join([]string{"#!/usr/bin/execlineb -P", runLine, ""}, "\n")
	if err := os.WriteFile(filepath.Join(serviceDir, "run"), []byte(runScript), 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(serviceDir, "dependencies"), nil, 0644)
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
	entries, _ := os.ReadFile(s6BundleContentsPath())
	bundleEntries := uniqueLinesPreserveOrder(string(entries))
	serviceName := s6OrchestrdServiceName(unitName)
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
		if err := liveStartS6(s6ServicectlAPIServiceName()); err != nil {
			return err
		}
		if err := liveStartS6(s6SysPropertydServiceName()); err != nil {
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
	if err := os.RemoveAll(s6OrchestrdSourceDir(unitName)); err != nil {
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

func isEnabledWithS6(unitName string) bool {
	entries, err := os.ReadFile(s6BundleContentsPath())
	if err != nil {
		return false
	}
	return containsString(uniqueLinesPreserveOrder(string(entries)), s6OrchestrdServiceName(unitName))
}

func enabledStandaloneServicesFromS6Bundle() []string {
	entries, err := os.ReadFile(s6BundleContentsPath())
	if err != nil {
		return nil
	}
	services := make([]string, 0)
	for _, entry := range uniqueLinesPreserveOrder(string(entries)) {
		if !strings.HasSuffix(entry, "-orchestrd") || strings.HasPrefix(entry, "group-") {
			continue
		}
		service := strings.TrimSuffix(entry, "-orchestrd") + ".service"
		if normalized := normalizeServiceUnitName(service); normalized != "" {
			services = append(services, normalized)
		}
	}
	return uniqueLinesPreserveOrder(strings.Join(services, "\n"))
}

func enabledGroups() []string {
	groups, err := queryAllGroups()
	if err != nil {
		return nil
	}
	result := make([]string, 0)
	for _, group := range groups {
		if group.Enabled {
			result = append(result, strings.TrimSpace(group.Name))
		}
	}
	return uniqueLinesPreserveOrder(strings.Join(result, "\n"))
}

func enabledRootSetFromCurrentState() enabledRootSet {
	roots := enabledRootSet{Standalone: enabledStandaloneServicesFromS6Bundle(), Groups: map[string][]string{}}
	for _, groupName := range enabledGroups() {
		state, ok := queryGroupState(groupName)
		if !ok {
			continue
		}
		units := make([]string, 0, len(state.Units))
		for _, unit := range state.Units {
			if normalized := normalizeServiceUnitName(unit); normalized != "" {
				units = append(units, normalized)
			}
		}
		if len(units) > 0 {
			roots.Groups[groupName] = uniqueLinesPreserveOrder(strings.Join(units, "\n"))
		}
	}
	return roots
}

func lookupSystemdUnitForDAG(name string) (*Unit, error) {
	return parseSystemdUnit(strings.TrimSuffix(normalizeServiceUnitName(name), ".service"))
}

func ensureOwnerSourceDir(owner string) string {
	return filepath.Join(s6SourceRoot(), owner)
}

func refreshS6OrchestrdDependencies() error {
	roots := enabledRootSetFromCurrentState()
	graph, err := buildEnabledServiceDAG(roots, lookupSystemdUnitForDAG)
	if err != nil {
		return err
	}
	projected := graph.ProjectOrchestrdDependencies()
	owners := make(map[string]bool)
	for _, owner := range graph.ownerByUnit {
		if owner != "" {
			owners[owner] = true
		}
	}
	baseDeps := make([]string, 0, 2)
	if !userMode() {
		baseDeps = append(baseDeps, "dinit")
	}
	baseDeps = append(baseDeps, s6SysvisiondServiceName())
	for owner := range owners {
		serviceDir := ensureOwnerSourceDir(owner)
		if info, statErr := os.Stat(serviceDir); statErr != nil || !info.IsDir() {
			continue
		}
		deps := append([]string{}, baseDeps...)
		for dep := range projected[owner] {
			deps = append(deps, dep)
		}
		deps = uniqueSortedStrings(deps)
		depsContent := ""
		if len(deps) > 0 {
			depsContent = strings.Join(deps, "\n") + "\n"
		}
		if err := os.WriteFile(filepath.Join(serviceDir, "dependencies"), []byte(depsContent), 0644); err != nil {
			return err
		}
	}
	return nil
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

func appendUniqueLinePreserveOrder(entries []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return entries
	}
	for _, existing := range entries {
		if existing == value {
			return entries
		}
	}
	return append(entries, value)
}

func writeLineFile(path string, lines []string) error {
	if len(lines) == 0 {
		return os.WriteFile(path, nil, 0644)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
