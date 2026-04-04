package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ansiStyle string

const (
	styleReset   ansiStyle = "\033[0m"
	styleDim     ansiStyle = "\033[2m"
	styleBold    ansiStyle = "\033[1m"
	styleRed     ansiStyle = "\033[31m"
	styleGreen   ansiStyle = "\033[32m"
	styleYellow  ansiStyle = "\033[33m"
	styleBlue    ansiStyle = "\033[34m"
	styleMagenta ansiStyle = "\033[35m"
	styleCyan    ansiStyle = "\033[36m"
	styleGray    ansiStyle = "\033[90m"
)

func colorsEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if term := strings.TrimSpace(os.Getenv("TERM")); term == "" || term == "dumb" {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func colorize(text string, styles ...ansiStyle) string {
	if !colorsEnabled() || text == "" || len(styles) == 0 {
		return text
	}
	var sb strings.Builder
	for _, style := range styles {
		sb.WriteString(string(style))
	}
	sb.WriteString(text)
	sb.WriteString(string(styleReset))
	return sb.String()
}

func dim(text string) string {
	return colorize(text, styleDim)
}

func sectionHeader(title string) string {
	return colorize(title, styleBold, styleCyan)
}

func statusColor(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "failed"):
		return colorize(text, styleRed)
	case strings.Contains(lower, "active") || strings.Contains(lower, "running"):
		return colorize(text, styleGreen)
	case strings.Contains(lower, "activating") || strings.Contains(lower, "deactivating") || strings.Contains(lower, "starting") || strings.Contains(lower, "stopping"):
		return colorize(text, styleYellow)
	case strings.Contains(lower, "inactive") || strings.Contains(lower, "dead") || strings.Contains(lower, "idle") || strings.Contains(lower, "unknown"):
		return colorize(text, styleGray)
	default:
		return text
	}
}

func unitBullet(activeLine string) string {
	lower := strings.ToLower(activeLine)
	switch {
	case strings.Contains(lower, "failed"):
		return colorize("●", styleRed)
	case strings.Contains(lower, "active") || strings.Contains(lower, "running"):
		return colorize("●", styleGreen)
	case strings.Contains(lower, "activating") || strings.Contains(lower, "deactivating"):
		return colorize("●", styleYellow)
	default:
		return colorize("●", styleGray)
	}
}

func emphasisValue(label string, value string) string {
	switch label {
	case "Failure":
		return colorize(value, styleRed)
	case "Env Warning":
		return colorize(value, styleYellow)
	case "Mode":
		if value == "user" {
			return colorize(value, styleMagenta)
		}
		return colorize(value, styleBlue)
	case "Managed By":
		return colorize(value, styleCyan)
	case "State", "Active", "Child", "Phase", "Status":
		return statusColor(value)
	default:
		return value
	}
}

func printKV(label string, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	formatted := emphasisValue(label, value)
	fmt.Printf("%-14s %s\n", dim(label), formatted)
}

func printStatusKV(label string, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	formatted := emphasisValue(label, value)
	fmt.Printf("%11s %s\n", dim(label+":"), formatted)
}

func printSection(title string, fields [][2]string) {
	printed := false
	for _, field := range fields {
		if strings.TrimSpace(field[1]) != "" {
			if !printed {
				fmt.Println(sectionHeader(title))
				printed = true
			}
			printKV(field[0], field[1])
		}
	}
	if printed {
		fmt.Println()
	}
}

func formatStartTime(pid string) string {
	startedAt := processStartTime(pid)
	if startedAt == "" {
		return ""
	}
	layouts := []string{
		time.ANSIC,
		"Mon Jan _2 15:04:05 2006",
		"Mon 2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, startedAt); err == nil {
			return ts.Format("2006-01-02 15:04:05 MST")
		}
	}
	return startedAt
}

func oneLineError(prefix string, err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return prefix
	}
	return prefix + ": " + strings.ReplaceAll(text, "\n", "; ")
}

func statusValue(values map[string]string, key string) string {
	if values == nil {
		return ""
	}
	return strings.TrimSpace(values[key])
}

func mapValue(values map[string]string, key string) string {
	if values == nil {
		return ""
	}
	return strings.TrimSpace(values[key])
}

func emptyDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func boolWord(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func socketSource(socketUnit *SocketUnit) string {
	if socketUnit == nil {
		return ""
	}
	return socketUnit.SourcePath
}

func socketFDNames(socketUnit *SocketUnit) []string {
	if socketUnit == nil {
		return nil
	}
	return socketUnit.FDNames
}

func socketListenValue(socketUnit *SocketUnit) string {
	if socketUnit == nil {
		return ""
	}
	return formatList(append(append([]string{}, socketUnit.ListenStreams...), socketUnit.ListenDgrams...))
}

func notifySocketPath(unitName string, unit *Unit, socketUnit *SocketUnit) string {
	if unit == nil || !shouldManageWithNotifyd(unit, socketUnit) {
		return ""
	}
	return filepath.Join(config.DinitGenDir, managedServiceName(unitName)+".notify.sock")
}

func managedStateFilePath(unitName string, unit *Unit, socketUnit *SocketUnit) string {
	if unit == nil || !shouldManageWithNotifyd(unit, socketUnit) {
		return ""
	}
	return notifydStatePath(unitName)
}

func printEnvironmentSection(unit *Unit) {
	if userMode() {
		env := userSessionEnvDefaults()
		printSection("Environment", [][2]string{{"Env HOME", env["HOME"]}, {"Env XDG RT", env["XDG_RUNTIME_DIR"]}, {"Env XDG CFG", env["XDG_CONFIG_HOME"]}, {"Env XDG ST", env["XDG_STATE_HOME"]}, {"Env XDG CA", env["XDG_CACHE_HOME"]}, {"Env DBUS", firstNonEmpty(env["DBUS_SESSION_BUS_ADDRESS"], "-")}})
		printUserEnvDiagnostics()
		return
	}
	printSection("Environment", [][2]string{{"Manager Scope", "system"}, {"HOME", firstNonEmpty(os.Getenv("HOME"), "-")}, {"PATH", firstNonEmpty(os.Getenv("PATH"), "-")}})
	_ = unit
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
