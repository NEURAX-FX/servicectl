package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEffectiveEnabledFromFlags(t *testing.T) {
	tests := []struct {
		name     string
		managed  bool
		external bool
		want     bool
	}{
		{name: "neither managed", managed: false, external: false, want: false},
		{name: "internally managed", managed: true, external: false, want: true},
		{name: "externally managed", managed: false, external: true, want: true},
		{name: "both managed modes", managed: true, external: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveEnabledFromFlags(tt.managed, tt.external); got != tt.want {
				t.Fatalf("effectiveEnabledFromFlags(%v, %v) = %v, want %v", tt.managed, tt.external, got, tt.want)
			}
		})
	}
}

func TestManagementStateFromFlags(t *testing.T) {
	tests := []struct {
		name     string
		managed  bool
		external bool
		want     string
	}{
		{name: "unmanaged", managed: false, external: false, want: "unmanaged"},
		{name: "internal", managed: true, external: false, want: "internal"},
		{name: "external", managed: false, external: true, want: "external"},
		{name: "internal and external", managed: true, external: true, want: "internal+external"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := managementStateFromFlags(tt.managed, tt.external); got != tt.want {
				t.Fatalf("managementStateFromFlags(%v, %v) = %q, want %q", tt.managed, tt.external, got, tt.want)
			}
		})
	}
}

func TestExternalManagedValueEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "numeric true", value: "1", want: true},
		{name: "word true", value: "true", want: true},
		{name: "yes with whitespace", value: " yes ", want: true},
		{name: "off", value: "off", want: false},
		{name: "zero", value: "0", want: false},
		{name: "empty", value: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := externalManagedValueEnabled(tt.value); got != tt.want {
				t.Fatalf("externalManagedValueEnabled(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestExternalManagedPlaceholderName(t *testing.T) {
	if got := externalManagedPlaceholderName("dbus.service"); got != "external-managed-dbus" {
		t.Fatalf("externalManagedPlaceholderName() = %q, want %q", got, "external-managed-dbus")
	}
}

func TestExternalManagedDependencyServiceName(t *testing.T) {
	if got := externalManagedDependencyServiceName("dbus", false); got != "dbus" {
		t.Fatalf("externalManagedDependencyServiceName(non-external) = %q, want %q", got, "dbus")
	}
	if got := externalManagedDependencyServiceName("dbus", true); got != "external-managed-dbus" {
		t.Fatalf("externalManagedDependencyServiceName(external) = %q, want %q", got, "external-managed-dbus")
	}
}

func TestGenerateExternalManagedPlaceholderDinit(t *testing.T) {
	content := generateExternalManagedPlaceholderDinit("dbus")
	checks := []string{
		"# External-managed placeholder for dbus.service",
		"type = internal",
	}
	for _, check := range checks {
		if !containsLine(content, check) {
			t.Fatalf("generateExternalManagedPlaceholderDinit() missing %q in %q", check, content)
		}
	}
}

func TestGenerateDinitUsesExternalManagedPlaceholderDependency(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()
	oldExternalManagedStateFunc := externalManagedStateFunc
	defer func() { externalManagedStateFunc = oldExternalManagedStateFunc }()
	externalManagedStateFunc = func(unitName string) bool {
		return strings.TrimSuffix(unitName, ".service") == "dbus"
	}

	tempDir := t.TempDir()
	config = Config{
		Mode:            "system",
		SystemdPaths:    []string{tempDir},
		DinitServiceDir: filepath.Join(tempDir, "dinit-service"),
		DinitGenDir:     filepath.Join(tempDir, "dinit-gen"),
	}

	if err := os.WriteFile(filepath.Join(tempDir, "consumer.service"), []byte("[Unit]\nRequires=dbus.service\n[Service]\nExecStart=/bin/true\n"), 0644); err != nil {
		t.Fatalf("write consumer unit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "dbus.service"), []byte("[Service]\nExecStart=/bin/true\n"), 0644); err != nil {
		t.Fatalf("write dbus unit: %v", err)
	}
	if err := os.MkdirAll(config.DinitServiceDir, 0755); err != nil {
		t.Fatalf("mkdir dinit service dir: %v", err)
	}
	if err := os.MkdirAll(config.DinitGenDir, 0755); err != nil {
		t.Fatalf("mkdir dinit gen dir: %v", err)
	}

	unit, err := parseSystemdUnit("consumer")
	if err != nil {
		t.Fatalf("parse consumer unit: %v", err)
	}

	content := unit.GenerateDinit()
	if !strings.Contains(content, "depends-on = external-managed-dbus") {
		t.Fatalf("GenerateDinit() missing placeholder dependency: %q", content)
	}
	if strings.Contains(content, "depends-on = dbus\n") {
		t.Fatalf("GenerateDinit() still references real dbus dependency: %q", content)
	}
}

func TestRecursiveInstallUsesExternalManagedPlaceholder(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()
	oldExternalManagedStateFunc := externalManagedStateFunc
	defer func() { externalManagedStateFunc = oldExternalManagedStateFunc }()
	externalManagedStateFunc = func(unitName string) bool {
		return strings.TrimSuffix(unitName, ".service") == "dbus"
	}

	tempDir := t.TempDir()
	config = Config{
		Mode:            "system",
		SystemdPaths:    []string{tempDir},
		DinitServiceDir: filepath.Join(tempDir, "dinit-service"),
		DinitGenDir:     filepath.Join(tempDir, "dinit-gen"),
	}

	if err := os.WriteFile(filepath.Join(tempDir, "consumer.service"), []byte("[Unit]\nRequires=dbus.service\n[Service]\nExecStart=/bin/true\n"), 0644); err != nil {
		t.Fatalf("write consumer unit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "dbus.service"), []byte("[Service]\nExecStart=/bin/true\n"), 0644); err != nil {
		t.Fatalf("write dbus unit: %v", err)
	}

	recursiveInstall("consumer", map[string]bool{}, installOptions{})

	placeholderPath := filepath.Join(config.DinitGenDir, externalManagedPlaceholderName("dbus"))
	if _, err := os.Stat(placeholderPath); err != nil {
		t.Fatalf("placeholder service not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.DinitGenDir, "dbus")); err == nil {
		t.Fatalf("real dbus dinit service should not be installed for external-managed dependency")
	}
}

func containsLine(content string, line string) bool {
	for _, candidate := range []string{line + "\n", line} {
		if content == candidate {
			return true
		}
	}
	return len(content) >= len(line) && (content == line || containsSubstring(content, line+"\n") || containsSubstring(content, "\n"+line+"\n") || containsSubstring(content, "\n"+line))
}

func containsSubstring(content string, needle string) bool {
	for i := 0; i+len(needle) <= len(content); i++ {
		if content[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
