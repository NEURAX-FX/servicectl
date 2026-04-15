package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	lookPathFunc             = exec.LookPath
	siblingExecutableDirFunc = executableDir
	systemHelperBinaryDirs   = []string{"/usr/local/bin", "/usr/bin", "/bin"}
)

func executableDir() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(self)
}

func existingBinaryInDir(dir string, name string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	candidate := filepath.Join(dir, name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func siblingBinaryPath(name string) string {
	return existingBinaryInDir(siblingExecutableDirFunc(), name)
}

func userBinaryPath(name string) string {
	if candidate := siblingBinaryPath(name); candidate != "" {
		return candidate
	}
	if candidate, err := lookPathFunc(name); err == nil {
		return candidate
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		if candidate := existingBinaryInDir(filepath.Join(home, ".local", "bin"), name); candidate != "" {
			return candidate
		}
		candidate := filepath.Join(home, "servicectl", name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return name
}

func systemHelperBinaryPath(name string) string {
	for _, dir := range systemHelperBinaryDirs {
		if candidate := existingBinaryInDir(dir, name); candidate != "" {
			return candidate
		}
	}
	if candidate := siblingBinaryPath(name); candidate != "" {
		return candidate
	}
	if candidate, err := lookPathFunc(name); err == nil {
		return candidate
	}
	return name
}
