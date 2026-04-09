package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type socketHolderPolicyRule struct {
	AllowAdaptiveStartup bool
	Provider             string
}

func loadSocketHolderPolicyRules() map[string]socketHolderPolicyRule {
	rules := make(map[string]socketHolderPolicyRule)
	for _, dir := range socketHolderPolicyDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
				continue
			}
			unitName := strings.TrimSuffix(entry.Name(), ".conf")
			rule, ok := parseSocketHolderPolicyFile(filepath.Join(dir, entry.Name()))
			if !ok {
				continue
			}
			rules[unitName] = rule
		}
	}
	return rules
}

func socketHolderPolicyDirs() []string {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if userMode() {
		return []string{filepath.Join(home, ".config/servicectl/socket-holders.d"), "/usr/lib/servicectl/socket-holders.d"}
	}
	return []string{"/etc/servicectl/socket-holders.d", "/usr/lib/servicectl/socket-holders.d"}
}

func parseSocketHolderPolicyFile(path string) (socketHolderPolicyRule, bool) {
	file, err := os.Open(path)
	if err != nil {
		return socketHolderPolicyRule{}, false
	}
	defer file.Close()

	rule := socketHolderPolicyRule{}
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 || section != "SocketHolders" {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "AllowAdaptiveStartup":
			rule.AllowAdaptiveStartup = parseSocketHolderBool(value)
		case "Provider":
			rule.Provider = strings.ToLower(strings.TrimSpace(value))
		}
	}
	if err := scanner.Err(); err != nil {
		return socketHolderPolicyRule{}, false
	}
	if !rule.AllowAdaptiveStartup || rule.Provider == "" {
		return socketHolderPolicyRule{}, false
	}
	return rule, true
}

func parseSocketHolderBool(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
