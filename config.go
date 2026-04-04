package main

import (
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
)

type Config struct {
	Mode            string
	SystemdPaths    []string
	DinitServiceDir string
	DinitGenDir     string
	UnitExtraDir    string
	IsRoot          bool
}

var (
	config      Config
	currentUser *user.User
)

func init() {
	currentUser, _ = user.Current()
	config = buildConfig(false)
}

func buildConfig(userMode bool) Config {
	home := os.Getenv("HOME")
	xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR")
	cfg := Config{
		IsRoot: currentUser != nil && currentUser.Uid == "0",
	}
	if userMode {
		cfg.Mode = "user"
		cfg.SystemdPaths = []string{
			filepath.Join(home, ".config/systemd/user/"),
			"/usr/lib/systemd/user/",
		}
		cfg.DinitServiceDir = filepath.Join(home, ".config/dinit.d")
		cfg.DinitGenDir = filepath.Join(xdgRuntimeDir, "dinit.d/generated")
		cfg.UnitExtraDir = filepath.Join(home, ".config/servicectl/unitextra")
		return cfg
	}
	cfg.Mode = "system"
	cfg.SystemdPaths = []string{
		"/etc/systemd/system/",
		"/usr/lib/systemd/system/",
		"/lib/systemd/system/",
	}
	cfg.DinitServiceDir = "/etc/dinit.d"
	cfg.DinitGenDir = "/run/dinit.d/generated"
	cfg.UnitExtraDir = filepath.Join(home, ".config/servicectl/unitextra")
	return cfg
}

func userMode() bool {
	return config.Mode == "user"
}

func currentUsername() string {
	if currentUser != nil && currentUser.Username != "" {
		return currentUser.Username
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return "user"
}

func currentUID() string {
	if currentUser != nil && currentUser.Uid != "" {
		return currentUser.Uid
	}
	return "0"
}

func runtimeDir() string {
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		return base
	}
	if userMode() {
		return filepath.Join(os.Getenv("HOME"), ".local", "run")
	}
	return "/run"
}

func currentShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

func userSessionEnvDefaults() map[string]string {
	home := os.Getenv("HOME")
	if home == "" && currentUser != nil {
		home = currentUser.HomeDir
	}
	env := map[string]string{
		"HOME":            home,
		"USER":            currentUsername(),
		"LOGNAME":         currentUsername(),
		"PATH":            os.Getenv("PATH"),
		"SHELL":           currentShell(),
		"TMPDIR":          "/tmp",
		"XDG_RUNTIME_DIR": runtimeDir(),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_STATE_HOME":  filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
	}
	for _, key := range []string{"HOME", "USER", "LOGNAME", "PATH", "SHELL", "TMPDIR", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	if value := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); value != "" {
		env["DBUS_SESSION_BUS_ADDRESS"] = value
	}
	return env
}

func environmentWithOverrides(base []string, overrides map[string]string) []string {
	envMap := make(map[string]string)
	for _, entry := range base {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		envMap[parts[0]] = parts[1]
	}
	for key, value := range overrides {
		envMap[key] = value
	}
	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+envMap[key])
	}
	return result
}

func syncUserDinitEnvironment() {
	if !userMode() {
		return
	}
	env := userSessionEnvDefaults()
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := []string{"setenv"}
	for _, key := range keys {
		args = append(args, key+"="+env[key])
	}
	cmd := dinitctlCommand(args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}
