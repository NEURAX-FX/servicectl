package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Unit struct {
	Name             string
	SourcePath       string
	Description      string
	Type             string
	ExecStart        []string
	ExecStop         string
	ExecReload       string
	PIDFile          string
	User             string
	Group            string
	WorkingDirectory string
	RuntimeDirectory []string
	RuntimeDirMode   string
	StateDirectory   []string
	StateDirMode     string
	LogsDirectory    []string
	LogsDirMode      string
	Environment      []string
	EnvironmentFile  []string
	TimeoutStartSec  string
	TimeoutStopSec   string
	KillSignal       string
	Requires         []string
	Wants            []string
	After            []string
	BindsTo          []string
	PartOf           []string
	CmdExtraAppend   []string
}

type SocketUnit struct {
	Name          string
	SourcePath    string
	Description   string
	Service       string
	ListenStreams []string
	ListenDgrams  []string
	FDNames       []string
	SocketMode    string
	SocketUser    string
	SocketGroup   string
}

var (
	reSection       = regexp.MustCompile(`^\[(.*)\]$`)
	reKeyValue      = regexp.MustCompile(`^([^=]+)=(.*)$`)
	reSpaces        = regexp.MustCompile(`\s+`)
	rePlainVar      = regexp.MustCompile(`\$[A-Za-z_][A-Za-z0-9_]*`)
	reBracedVar     = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)
	reEnvAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

func expandSpecifiers(value string, unitName string) string {
	if value == "" || !strings.Contains(value, "%") {
		return value
	}
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '%' || i+1 >= len(value) {
			b.WriteByte(value[i])
			continue
		}
		i++
		switch value[i] {
		case '%':
			b.WriteByte('%')
		case 't':
			b.WriteString(runtimeDir())
		case 'h':
			b.WriteString(os.Getenv("HOME"))
		case 'u':
			b.WriteString(currentUsername())
		case 'U':
			b.WriteString(currentUID())
		case 'n':
			b.WriteString(unitName)
		case 'N':
			b.WriteString(strings.TrimSuffix(unitName, filepath.Ext(unitName)))
		default:
			b.WriteByte('%')
			b.WriteByte(value[i])
		}
	}
	return b.String()
}

func resolveUnitAlias(name string) string {
	cleanName := strings.TrimSuffix(strings.TrimSpace(name), ".service")
	if cleanName == "pipewire-session-manager" {
		if _, err := parseSystemdUnit("wireplumber"); err == nil {
			return "wireplumber"
		}
	}
	return cleanName
}

func cleanExecCmd(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	for len(cmd) > 0 && (cmd[0] == '-' || cmd[0] == '@' || cmd[0] == '+' || cmd[0] == '!') {
		cmd = cmd[1:]
	}
	return strings.TrimSpace(cmd)
}

func parseSystemdUnit(name string) (*Unit, error) {
	target := resolveUnitAlias(name)
	if !strings.HasSuffix(target, ".service") {
		target += ".service"
	}

	var unitPath string
	for _, p := range config.SystemdPaths {
		full := filepath.Join(p, target)
		if _, err := os.Stat(full); err == nil {
			unitPath = full
			break
		}
	}
	if unitPath == "" {
		return nil, fmt.Errorf("unit %s not found", name)
	}

	unit := &Unit{Name: strings.TrimSuffix(target, ".service")}
	unit.SourcePath = unitPath
	currentSection := ""

	lines, err := readUnitLines(unitPath)
	if err != nil {
		return nil, err
	}

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if m := reSection.FindStringSubmatch(line); len(m) > 1 {
			currentSection = m[1]
			continue
		}

		if m := reKeyValue.FindStringSubmatch(line); len(m) > 2 {
			k, v := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])

			switch currentSection {
			case "Unit":
				switch k {
				case "Description":
					unit.Description = v
				case "Requires":
					unit.Requires = append(unit.Requires, strings.Fields(v)...)
				case "Wants":
					unit.Wants = append(unit.Wants, strings.Fields(v)...)
				case "After":
					unit.After = append(unit.After, strings.Fields(v)...)
				case "BindsTo":
					unit.BindsTo = append(unit.BindsTo, strings.Fields(v)...)
				case "PartOf":
					unit.PartOf = append(unit.PartOf, strings.Fields(v)...)
				}
			case "Service":
				switch k {
				case "Type":
					unit.Type = v
				case "ExecStart":
					if v == "" {
						unit.ExecStart = nil
					} else {
						unit.ExecStart = append(unit.ExecStart, cleanExecCmd(expandSpecifiers(v, target)))
					}
				case "ExecStop":
					unit.ExecStop = cleanExecCmd(expandSpecifiers(v, target))
				case "ExecReload":
					unit.ExecReload = cleanExecCmd(expandSpecifiers(v, target))
				case "PIDFile":
					unit.PIDFile = expandSpecifiers(v, target)
				case "User":
					unit.User = v
				case "Group":
					unit.Group = v
				case "WorkingDirectory":
					unit.WorkingDirectory = expandSpecifiers(v, target)
				case "RuntimeDirectory":
					unit.RuntimeDirectory = append(unit.RuntimeDirectory, strings.Fields(v)...)
				case "RuntimeDirectoryMode":
					unit.RuntimeDirMode = v
				case "StateDirectory":
					unit.StateDirectory = append(unit.StateDirectory, strings.Fields(v)...)
				case "StateDirectoryMode":
					unit.StateDirMode = v
				case "LogsDirectory":
					unit.LogsDirectory = append(unit.LogsDirectory, strings.Fields(v)...)
				case "LogsDirectoryMode":
					unit.LogsDirMode = v
				case "Environment":
					unit.Environment = append(unit.Environment, strings.Trim(v, `"`))
				case "EnvironmentFile":
					unit.EnvironmentFile = append(unit.EnvironmentFile, expandSpecifiers(strings.TrimPrefix(v, "-"), target))
				case "TimeoutStartSec":
					unit.TimeoutStartSec = v
				case "TimeoutStopSec":
					unit.TimeoutStopSec = v
				case "KillSignal":
					unit.KillSignal = v
				}
			}
		}
	}

	applyUnitExtra(unit)

	return unit, nil
}

func applyUnitExtra(unit *Unit) {
	if config.UnitExtraDir == "" {
		return
	}

	extraPath := filepath.Join(config.UnitExtraDir, unit.Name+".conf")
	lines, err := readUnitLines(extraPath)
	if err != nil {
		return
	}
	currentSection := ""

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if m := reSection.FindStringSubmatch(line); len(m) > 1 {
			currentSection = m[1]
			continue
		}

		if currentSection != "Cmd Extra" {
			continue
		}

		if m := reKeyValue.FindStringSubmatch(line); len(m) > 2 {
			k, v := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
			if k == "Append" && v != "" {
				unit.CmdExtraAppend = append(unit.CmdExtraAppend, v)
			}
		}
	}
}

func parseSocketUnit(name string) (*SocketUnit, error) {
	target := resolveUnitAlias(name)
	if strings.HasSuffix(target, ".service") {
		target = strings.TrimSuffix(target, ".service")
	}
	if !strings.HasSuffix(target, ".socket") {
		target += ".socket"
	}

	var unitPath string
	for _, p := range config.SystemdPaths {
		full := filepath.Join(p, target)
		if _, err := os.Stat(full); err == nil {
			unitPath = full
			break
		}
	}
	if unitPath == "" {
		return nil, fmt.Errorf("socket unit %s not found", name)
	}

	socketUnit := &SocketUnit{Name: strings.TrimSuffix(target, ".socket"), SourcePath: unitPath}
	currentSection := ""
	lines, err := readUnitLines(unitPath)
	if err != nil {
		return nil, err
	}

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if m := reSection.FindStringSubmatch(line); len(m) > 1 {
			currentSection = m[1]
			continue
		}

		if m := reKeyValue.FindStringSubmatch(line); len(m) > 2 {
			k, v := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])

			switch currentSection {
			case "Unit":
				switch k {
				case "Description":
					socketUnit.Description = v
				}
			case "Socket":
				switch k {
				case "ListenStream":
					if v != "" {
						socketUnit.ListenStreams = append(socketUnit.ListenStreams, expandSpecifiers(v, target))
					}
				case "ListenDatagram":
					if v != "" {
						socketUnit.ListenDgrams = append(socketUnit.ListenDgrams, expandSpecifiers(v, target))
					}
				case "FileDescriptorName":
					if v != "" {
						socketUnit.FDNames = append(socketUnit.FDNames, v)
					}
				case "SocketMode":
					socketUnit.SocketMode = v
				case "SocketUser":
					socketUnit.SocketUser = v
				case "SocketGroup":
					socketUnit.SocketGroup = v
				case "Service":
					socketUnit.Service = strings.TrimSuffix(v, ".service")
				}
			}
		}
	}

	if socketUnit.Service == "" {
		socketUnit.Service = socketUnit.Name
	}
	return socketUnit, nil
}

func readUnitLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	rawLines := strings.Split(content, "\n")
	lines := make([]string, 0, len(rawLines))
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		lines = append(lines, current.String())
		current.Reset()
	}
	for _, raw := range rawLines {
		trimmed := strings.TrimRight(raw, " \t")
		continued := strings.HasSuffix(trimmed, "\\")
		trimmed = strings.TrimSuffix(trimmed, "\\")
		trimmed = strings.TrimRight(trimmed, " \t")
		if current.Len() > 0 {
			current.WriteByte(' ')
		}
		current.WriteString(trimmed)
		if !continued {
			flush()
		}
	}
	flush()
	return lines, nil
}

func parseOptionalSocketUnit(name string) (*SocketUnit, error) {
	socketUnit, err := parseSocketUnit(name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, err
	}
	return socketUnit, nil
}

func loadEnvFiles(paths []string) map[string]string {
	envs := make(map[string]string)
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				envs[parts[0]] = strings.Trim(parts[1], `"'\t `)
			}
		}
		f.Close()
	}
	return envs
}

func applyEnv(cmd string, envs map[string]string) string {
	keys := make([]string, 0, len(envs))
	for k := range envs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j])
	})

	for _, k := range keys {
		v := envs[k]
		cmd = strings.ReplaceAll(cmd, "${"+k+"}", v)
		cmd = strings.ReplaceAll(cmd, "$"+k, v)
	}

	cmd = reBracedVar.ReplaceAllString(cmd, "")
	cmd = rePlainVar.ReplaceAllString(cmd, "")
	return strings.TrimSpace(cmd)
}

func sanitizeExec(_ string, cmd string) string {
	cmd = strings.TrimSpace(cmd)
	cmd = reSpaces.ReplaceAllString(cmd, " ")
	cmd = strings.TrimSpace(cmd)
	cmd = reSpaces.ReplaceAllString(cmd, " ")
	return strings.TrimSpace(cmd)
}

func appendCmdExtra(cmd string, extras []string) string {
	cmd = strings.TrimSpace(cmd)
	for _, extra := range extras {
		extra = strings.TrimSpace(extra)
		if extra == "" {
			continue
		}
		if cmd == "" {
			cmd = extra
			continue
		}
		cmd += " " + extra
	}
	return strings.TrimSpace(cmd)
}

func buildEnvPrefix(envs map[string]string) string {
	if len(envs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(envs))
	for k := range envs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	envList := make([]string, 0, len(keys))
	for _, k := range keys {
		envList = append(envList, fmt.Sprintf("%s=%s", k, envs[k]))
	}
	return "/usr/bin/env " + strings.Join(envList, " ") + " "
}

func joinExecStarts(cmds []string) string {
	switch len(cmds) {
	case 0:
		return "/usr/bin/true"
	case 1:
		return cmds[0]
	default:
		return fmt.Sprintf("/bin/sh -c '%s'", strings.Join(cmds, " && "))
	}
}

func parseInlineEnv(line string) map[string]string {
	envs := make(map[string]string)
	fields := strings.Fields(line)
	for _, f := range fields {
		if reEnvAssignment.MatchString(f) {
			parts := strings.SplitN(f, "=", 2)
			if len(parts) == 2 {
				envs[parts[0]] = strings.Trim(parts[1], `"'\t `)
			}
		}
	}
	return envs
}

func mergedServiceEnv(u *Unit) map[string]string {
	allEnvs := make(map[string]string)
	if userMode() {
		for k, v := range userSessionEnvDefaults() {
			allEnvs[k] = v
		}
	}
	for k, v := range loadEnvFiles(u.EnvironmentFile) {
		allEnvs[k] = v
	}
	for _, envLine := range u.Environment {
		for k, v := range parseInlineEnv(envLine) {
			allEnvs[k] = v
		}
	}
	delete(allEnvs, "NOTIFY_SOCKET")
	delete(allEnvs, "LISTEN_FDS")
	delete(allEnvs, "LISTEN_PID")
	return allEnvs
}

func compileExecStart(u *Unit, envs map[string]string) string {
	execStarts := make([]string, len(u.ExecStart))
	copy(execStarts, u.ExecStart)
	if len(execStarts) > 0 && len(u.CmdExtraAppend) > 0 {
		execStarts[0] = appendCmdExtra(execStarts[0], u.CmdExtraAppend)
	}
	finalCmd := joinExecStarts(execStarts)
	finalCmd = applyEnv(finalCmd, envs)
	return sanitizeExec(u.Name, finalCmd)
}

func compileExecStop(u *Unit, envs map[string]string) string {
	if u.ExecStop == "" {
		return ""
	}
	stopCmd := applyEnv(u.ExecStop, envs)
	return sanitizeExec(u.Name, stopCmd)
}

func compileExecReload(u *Unit, envs map[string]string) string {
	if u.ExecReload == "" {
		return ""
	}
	reloadCmd := applyEnv(u.ExecReload, envs)
	return sanitizeExec(u.Name, reloadCmd)
}

func normalizeSignalName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.TrimPrefix(strings.ToUpper(raw), "SIG")
	return raw
}
