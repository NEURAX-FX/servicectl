package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"servicectl/internal/statusview"
	"servicectl/internal/visionapi"
)

type displayMode int

const (
	displayModeAuto displayMode = iota
	displayModeTerminal
	displayModePlain
	displayModeJSON
)

// statusJSONErrorV2 is the stable JSON envelope for status command failures.
type statusJSONErrorV2 struct {
	SchemaVersion int                     `json:"schema_version"`
	Error         statusJSONErrorDetailV2 `json:"error"`
}

type statusJSONErrorDetailV2 struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Unit    string `json:"unit,omitempty"`
}

func renderStatusJSONV2(w io.Writer, model statusview.Model) error {
	return json.NewEncoder(w).Encode(model)
}

func renderStatusJSONErrorV2(w io.Writer, code, message, unit string) error {
	return json.NewEncoder(w).Encode(statusJSONErrorV2{
		SchemaVersion: statusview.SchemaVersion,
		Error: statusJSONErrorDetailV2{
			Code:    code,
			Message: message,
			Unit:    unit,
		},
	})
}

type displayOptions struct {
	mode    displayMode
	all     bool
	verbose bool
}

type statusModelCollector func(context.Context, string, string, uint32) (statusview.Model, error)

type statusDisplayInvocation struct {
	Unit    string
	Options displayOptions
}

type displayInvocation struct {
	Handled bool
	List    *displayOptions
	Status  *statusDisplayInvocation
}

func parseDisplayInvocation(action string, args []string, group string) (displayInvocation, error) {
	if strings.TrimSpace(group) != "" {
		return displayInvocation{}, nil
	}
	switch action {
	case "list":
		options, err := parseListDisplayArgs(args)
		if err != nil {
			return displayInvocation{}, err
		}
		return displayInvocation{Handled: true, List: &options}, nil
	case "status":
		unit, options, err := parseStatusDisplayArgs(args)
		if err != nil {
			return displayInvocation{}, err
		}
		return displayInvocation{Handled: true, Status: &statusDisplayInvocation{Unit: unit, Options: options}}, nil
	default:
		return displayInvocation{}, nil
	}
}

type listUnitView struct {
	Unit         string `json:"unit"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	RuntimeState string `json:"runtime_state"`
	EnabledState string `json:"enabled_state"`
	Type         string `json:"type"`
	MainPID      int    `json:"main_pid,omitempty"`
	Backend      string `json:"backend,omitempty"`
	Role         string `json:"role,omitempty"`
	Internal     bool   `json:"internal"`
}

type listView struct {
	SchemaVersion int            `json:"schema_version"`
	Mode          string         `json:"mode"`
	GeneratedAt   string         `json:"generated_at"`
	Units         []listUnitView `json:"units"`
}

type renderCapabilities struct {
	Width int
	Color bool
}

type displayRuntime struct {
	TTY   bool
	Width int
	Color bool
}

type dinitListRow struct {
	Name         string
	RuntimeState string
	PID          int
}

type displayDataSource struct {
	queryUnits         func() (visionapi.UnitsResponse, bool)
	propertyLists      func() (visionapi.UnitListsResponse, error)
	buildSnapshot      func(Config, string) (visionapi.UnitSnapshot, error)
	parseUnit          func(string) (*Unit, error)
	enabled            func(string) bool
	dinitList          func(...string) (string, int, error)
	orchestratorExists func(string) bool
	orchestratorPID    func(string) string
	s6Services         func() []dinitListRow
}

func defaultDisplayDataSource() displayDataSource {
	return displayDataSource{
		queryUnits:    queryUnitSnapshotsViaSysvision,
		propertyLists: propertyUnitLists,
		buildSnapshot: buildUnitSnapshot,
		parseUnit:     parseSystemdUnit,
		enabled:       isEffectivelyEnabled,
		dinitList:     dinitctlOutput,
		orchestratorExists: func(name string) bool {
			return s6OwnerMatchesCurrentMode(s6OrchestrdSourceDir(name))
		},
		orchestratorPID: orchestratorPID,
		s6Services:      liveS6InternalServices,
	}
}

func resolveDisplayMode(explicit displayMode, tty bool) displayMode {
	if explicit != displayModeAuto {
		return explicit
	}
	if tty {
		return displayModeTerminal
	}
	return displayModePlain
}

func canonicalRuntimeState(snapshot visionapi.UnitSnapshot) string {
	failure := strings.ToLower(strings.TrimSpace(snapshot.Failure))
	lifecycle := strings.ToLower(strings.TrimSpace(snapshot.Lifecycle))
	state := strings.ToUpper(strings.TrimSpace(snapshot.State))
	phase := strings.ToLower(strings.TrimSpace(snapshot.Phase))
	child := strings.ToLower(strings.TrimSpace(snapshot.ChildState))
	if failure != "" || lifecycle == "failed" || strings.Contains(state, "EXITED - STATUS") {
		return "failed"
	}
	if lifecycle == "activating" || phase == "starting" || child == "starting" {
		return "activating"
	}
	if lifecycle == "deactivating" || phase == "stopping" || child == "stopping" {
		return "deactivating"
	}
	if lifecycle == "active" || lifecycle == "running" || state == "STARTED" {
		return "active"
	}
	if lifecycle == "inactive" || lifecycle == "stopped" || strings.HasPrefix(state, "STOPPED") || state == "NOT LOADED" {
		return "inactive"
	}
	return "unknown"
}

func buildListUnitView(snapshot visionapi.UnitSnapshot, unit *Unit, enabled bool) listUnitView {
	name := strings.TrimSuffix(strings.TrimSpace(snapshot.Name), ".service")
	if name == "" && unit != nil {
		name = strings.TrimSuffix(strings.TrimSpace(unit.Name), ".service")
	}
	typeName := "service"
	description := strings.TrimSpace(snapshot.Description)
	if unit != nil {
		if strings.TrimSpace(unit.Type) != "" {
			typeName = strings.ToLower(strings.TrimSpace(unit.Type))
		}
		if strings.TrimSpace(unit.Description) != "" {
			description = strings.TrimSpace(unit.Description)
		}
	}
	mainPID, _ := strconv.Atoi(strings.TrimSpace(snapshot.MainPID))
	enabledState := "disabled"
	if enabled {
		enabledState = "enabled"
	}
	return listUnitView{
		Unit:         name + ".service",
		Name:         name,
		Description:  description,
		RuntimeState: canonicalRuntimeState(snapshot),
		EnabledState: enabledState,
		Type:         typeName,
		MainPID:      mainPID,
		Backend:      strings.TrimSpace(snapshot.ManagedBy),
	}
}

func sortListUnitViews(units []listUnitView) {
	priority := map[string]int{"failed": 0, "activating": 1, "deactivating": 1, "active": 2, "inactive": 3, "unknown": 4}
	sort.SliceStable(units, func(i, j int) bool {
		left, right := priority[units[i].RuntimeState], priority[units[j].RuntimeState]
		if left != right {
			return left < right
		}
		return units[i].Name < units[j].Name
	})
}

func internalServiceRole(name string) string {
	switch {
	case strings.HasSuffix(name, "-orchestrd"):
		return "orchestrator"
	case strings.HasSuffix(name, "-notifyd"), strings.HasSuffix(name, "-dbusd"), strings.HasSuffix(name, "-socketd"):
		return "manager"
	case strings.HasSuffix(name, "-log"):
		return "logger"
	default:
		return "service"
	}
}

func parseDinitListRows(output string) []dinitListRow {
	rows := make([]dinitListRow, 0)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		close := strings.LastIndex(line, "] ")
		if close < 0 {
			continue
		}
		prefix := line[:close+1]
		rest := strings.TrimSpace(line[close+2:])
		name := rest
		if index := strings.Index(rest, " ("); index >= 0 {
			name = rest[:index]
		}
		state := "inactive"
		switch {
		case strings.Contains(prefix, "X") || strings.Contains(rest, "exit status:"):
			state = "failed"
		case strings.Contains(prefix, "+"):
			state = "active"
		case strings.Contains(prefix, "{+") || strings.Contains(prefix, "{{"):
			state = "activating"
		}
		pid := 0
		if start := strings.Index(rest, "(pid: "); start >= 0 {
			value := rest[start+6:]
			if end := strings.IndexByte(value, ')'); end >= 0 {
				value = value[:end]
			}
			pid, _ = strconv.Atoi(strings.TrimSpace(value))
		}
		rows = append(rows, dinitListRow{Name: strings.TrimSpace(name), RuntimeState: state, PID: pid})
	}
	return rows
}

func collectListView(source displayDataSource, includeInternal bool) (listView, error) {
	view := listView{SchemaVersion: 1, Mode: config.Mode, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	response, ok := source.queryUnits()
	if ok {
		if strings.TrimSpace(response.GeneratedAt) != "" {
			view.GeneratedAt = response.GeneratedAt
		}
	} else {
		units := make([]string, 0)
		if source.propertyLists != nil {
			lists, err := source.propertyLists()
			if err == nil {
				units = append(units, lists.EffectiveUnits...)
			}
		}
		if len(units) == 0 {
			units = discoverSystemdUnits(config)
		}
		for _, name := range normalizeUnitListNames(units) {
			snapshot, err := source.buildSnapshot(config, name)
			if err != nil {
				continue
			}
			response.Units = append(response.Units, snapshot)
		}
	}
	applicationNames := make(map[string]bool)
	for _, snapshot := range response.Units {
		name := strings.TrimSuffix(strings.TrimSpace(snapshot.Name), ".service")
		if name == "" {
			continue
		}
		unit, _ := source.parseUnit(name)
		enabled := false
		if source.enabled != nil {
			enabled = source.enabled(name)
		}
		row := buildListUnitView(snapshot, unit, enabled)
		view.Units = append(view.Units, row)
		applicationNames[name] = true
	}
	sortListUnitViews(view.Units)
	if !includeInternal {
		return view, nil
	}
	seenInternal := make(map[string]bool)
	if source.dinitList != nil {
		output, _, _ := source.dinitList("list")
		for _, row := range parseDinitListRows(output) {
			role := internalServiceRole(row.Name)
			if role == "service" || seenInternal[row.Name] {
				continue
			}
			seenInternal[row.Name] = true
			view.Units = append(view.Units, listUnitView{Unit: row.Name, Name: row.Name, RuntimeState: row.RuntimeState, Type: role, Role: role, MainPID: row.PID, Backend: "dinit", Internal: true})
		}
	}
	for name := range applicationNames {
		if source.orchestratorExists == nil || !source.orchestratorExists(name) {
			continue
		}
		serviceName := s6OrchestrdServiceName(name)
		if seenInternal[serviceName] {
			continue
		}
		pid := 0
		if source.orchestratorPID != nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(source.orchestratorPID(name)))
		}
		state := "inactive"
		if pid > 0 {
			state = "active"
		}
		view.Units = append(view.Units, listUnitView{Unit: serviceName, Name: serviceName, RuntimeState: state, Type: "orchestrator", Role: "orchestrator", MainPID: pid, Backend: "s6", Internal: true})
		seenInternal[serviceName] = true
	}
	if source.s6Services != nil {
		for _, row := range source.s6Services() {
			if seenInternal[row.Name] {
				continue
			}
			role := internalServiceRole(row.Name)
			view.Units = append(view.Units, listUnitView{Unit: row.Name, Name: row.Name, RuntimeState: row.RuntimeState, Type: role, Role: role, MainPID: row.PID, Backend: "s6", Internal: true})
			seenInternal[row.Name] = true
		}
	}
	apps := listRows(view, false)
	internals := listRows(view, true)
	sortListUnitViews(apps)
	sortListUnitViews(internals)
	view.Units = append(apps, internals...)
	return view, nil
}

func liveS6InternalServices() []dinitListRow {
	serviceRoot := filepath.Join(filepath.Dir(s6LiveDir()), "service")
	entries, err := os.ReadDir(serviceRoot)
	if err != nil {
		return nil
	}
	rows := make([]dinitListRow, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !isInternalS6Service(name) {
			continue
		}
		run, _ := os.ReadFile(filepath.Join(s6SourceRoot(), name, "run"))
		if !internalS6ServiceMatchesMode(name, string(run), config.Mode) {
			continue
		}
		text, code, _ := commandOutputFunc("/bin/s6-svstat", "-o", "up,pid", filepath.Join(serviceRoot, name))
		fields := strings.Fields(text)
		state := "unknown"
		pid := 0
		if code == 0 && len(fields) > 0 {
			if strings.EqualFold(fields[0], "true") {
				state = "active"
			} else {
				state = "inactive"
			}
			if len(fields) > 1 {
				pid, _ = strconv.Atoi(fields[1])
			}
		}
		rows = append(rows, dinitListRow{Name: name, RuntimeState: state, PID: pid})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

func internalS6ServiceMatchesMode(name, run, mode string) bool {
	user := strings.EqualFold(strings.TrimSpace(mode), "user")
	switch {
	case name == "sys-propertyd", name == "sys-cgroupd", name == "sysvisiond", name == "servicectl-api":
		return !user
	case strings.HasPrefix(name, "sysvisiond-user-"), strings.HasPrefix(name, "servicectl-api-user-"):
		return user
	case strings.HasSuffix(name, "-orchestrd"), strings.HasPrefix(name, "group-"):
		return strings.Contains(run, "sys-orchestrd --user ") == user
	default:
		return false
	}
}

func isInternalS6Service(name string) bool {
	return strings.HasSuffix(name, "-orchestrd") || strings.HasPrefix(name, "group-") || strings.HasPrefix(name, "servicectl-api") || strings.HasPrefix(name, "sysvisiond") || name == "sys-propertyd" || name == "sys-cgroupd"
}

func visibleWidth(text string) int {
	width := 0
	for i := 0; i < len(text); {
		if text[i] == '\x1b' && i+1 < len(text) && text[i+1] == '[' {
			i += 2
			for i < len(text) {
				b := text[i]
				i++
				if b >= '@' && b <= '~' {
					break
				}
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if size == 0 {
			break
		}
		width += runeDisplayWidth(r)
		i += size
	}
	return width
}

func runeDisplayWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == '\u200d' {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) || (r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) || (r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) || (r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1faff) || (r >= 0x20000 && r <= 0x3fffd)) {
		return 2
	}
	return 1
}

func truncateVisible(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if visibleWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	target := width - 1
	current := 0
	var out strings.Builder
	for _, r := range text {
		runeWidth := runeDisplayWidth(r)
		if current+runeWidth > target {
			break
		}
		out.WriteRune(r)
		current += runeWidth
	}
	return out.String() + "…"
}

func padVisible(text string, width int) string {
	text = truncateVisible(text, width)
	if padding := width - visibleWidth(text); padding > 0 {
		text += strings.Repeat(" ", padding)
	}
	return text
}

func padVisibleLeft(text string, width int) string {
	text = truncateVisible(text, width)
	if padding := width - visibleWidth(text); padding > 0 {
		text = strings.Repeat(" ", padding) + text
	}
	return text
}

func displayStateStyle(state string) ansiStyle {
	switch state {
	case "failed":
		return styleRed
	case "active":
		return styleGreen
	case "activating", "deactivating":
		return styleYellow
	default:
		return styleGray
	}
}

func styleText(text string, caps renderCapabilities, styles ...ansiStyle) string {
	if !caps.Color || text == "" {
		return text
	}
	var out strings.Builder
	for _, style := range styles {
		out.WriteString(string(style))
	}
	out.WriteString(text)
	out.WriteString(string(styleReset))
	return out.String()
}

func listRows(view listView, internal bool) []listUnitView {
	rows := make([]listUnitView, 0)
	for _, unit := range view.Units {
		if unit.Internal == internal {
			rows = append(rows, unit)
		}
	}
	return rows
}

func renderListTerminal(w io.Writer, view listView, caps renderCapabilities) error {
	if caps.Width <= 0 {
		caps.Width = 80
	}
	apps := listRows(view, false)
	internals := listRows(view, true)
	if _, err := fmt.Fprintf(w, "%s\n\n", styleText(fmt.Sprintf("SERVICES · %s · %d %s", view.Mode, len(apps), plural(len(apps), "unit", "units")), caps, styleBold, styleCyan)); err != nil {
		return err
	}
	if len(apps) == 0 {
		if _, err := fmt.Fprintln(w, "No service units found."); err != nil {
			return err
		}
	} else if err := renderListRows(w, apps, caps); err != nil {
		return err
	}
	if len(internals) > 0 {
		if _, err := fmt.Fprintf(w, "\n%s\n\n", styleText(fmt.Sprintf("INTERNAL · %d %s", len(internals), plural(len(internals), "service", "services")), caps, styleBold, styleCyan)); err != nil {
			return err
		}
		return renderListRows(w, internals, caps)
	}
	return nil
}

func renderListRows(w io.Writer, rows []listUnitView, caps renderCapabilities) error {
	stateWidth := 14
	nameWidth := 1
	for _, unit := range rows {
		if width := visibleWidth(unit.Name); width > nameWidth {
			nameWidth = width
		}
	}
	maxNameWidth := caps.Width - stateWidth - 5
	if maxNameWidth < 1 {
		maxNameWidth = 1
	}
	if nameWidth > maxNameWidth {
		nameWidth = maxNameWidth
	}
	for _, unit := range rows {
		state := unit.RuntimeState
		stateStyle := displayStateStyle(state)
		left := styleText("● "+padVisible(state, stateWidth-2), caps, stateStyle)
		name := padVisible(unit.Name, nameWidth)
		typeName := unit.Type
		if unit.Internal && unit.Role != "" {
			typeName = unit.Role
		}
		meta := "[" + typeName + "]"
		if !unit.Internal && unit.EnabledState != "" {
			meta += " " + unit.EnabledState
		}
		if unit.MainPID > 0 {
			meta += " pid " + strconv.Itoa(unit.MainPID)
		}
		available := caps.Width - visibleWidth(left) - 4 - nameWidth
		if available < 1 {
			available = 1
		}
		if _, err := fmt.Fprintf(w, "%s  %s  %s\n", left, name, truncateVisible(meta, available)); err != nil {
			return err
		}
	}
	return nil
}

func plural(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func renderListPlain(w io.Writer, view listView) error {
	for _, unit := range view.Units {
		pid := ""
		if unit.MainPID > 0 {
			pid = strconv.Itoa(unit.MainPID)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", unit.Unit, unit.RuntimeState, unit.EnabledState, unit.Type, pid); err != nil {
			return err
		}
	}
	return nil
}

func renderListJSON(w io.Writer, view listView) error {
	return json.NewEncoder(w).Encode(view)
}

func runListDisplay(w io.Writer, options displayOptions, runtime displayRuntime, source displayDataSource) int {
	view, err := collectListView(source, options.all)
	if err != nil {
		fmt.Fprintln(w, oneLineError("list services", err))
		return 1
	}
	mode := resolveDisplayMode(options.mode, runtime.TTY)
	switch mode {
	case displayModeJSON:
		err = renderListJSON(w, view)
	case displayModePlain:
		err = renderListPlain(w, view)
	default:
		err = renderListTerminal(w, view, renderCapabilities{Width: runtime.Width, Color: runtime.Color})
	}
	if err != nil {
		return 1
	}
	return 0
}

func runStatusDisplay(w io.Writer, unitName string, options displayOptions, runtime displayRuntime, collect statusModelCollector) int {
	mode := resolveDisplayMode(options.mode, runtime.TTY)
	unit := strings.TrimSuffix(strings.TrimSpace(unitName), ".service") + ".service"
	if collect == nil {
		return renderStatusCollectionError(w, mode, unit, errors.New("status collector is unavailable"), 1)
	}
	uid := uint32(0)
	if userMode() {
		uid = uint32(os.Geteuid())
	}
	model, err := collect(context.Background(), unitName, config.Mode, uid)
	if err != nil {
		var collectionErr *statusCollectionError
		if errors.As(err, &collectionErr) && collectionErr.Kind == statusCollectionNotFound {
			return renderStatusCollectionError(w, mode, unit, errStatusUnitNotFound, 4)
		}
		return renderStatusCollectionError(w, mode, unit, err, 1)
	}
	switch mode {
	case displayModeJSON:
		err = renderStatusJSONV2(w, model)
	case displayModePlain:
		err = renderStatusPlainV2(w, model, options.verbose)
	default:
		err = renderStatusTerminalV2(w, model, renderCapabilities{Width: runtime.Width, Color: runtime.Color}, options.verbose)
	}
	if err != nil {
		return 1
	}
	return statusview.ExitCode(model)
}

func renderStatusCollectionError(w io.Writer, mode displayMode, unit string, err error, exitCode int) int {
	if mode == displayModeJSON {
		code := "status_collection_failed"
		message := "Unable to collect status for " + unit + "."
		if exitCode == 4 {
			code = "unit_not_found"
			message = "Unit " + unit + " could not be found."
		} else if err != nil {
			message = err.Error()
		}
		if renderErr := renderStatusJSONErrorV2(w, code, message, unit); renderErr != nil {
			return 1
		}
		return exitCode
	}
	if exitCode == 4 {
		if _, writeErr := fmt.Fprintf(w, "Unit %s could not be found.\n", unit); writeErr != nil {
			return 1
		}
		return 4
	}
	if err == nil {
		err = errors.New("status collection failed")
	}
	if _, writeErr := fmt.Fprintln(w, oneLineError("status", err)); writeErr != nil {
		return 1
	}
	return exitCode
}

func currentDisplayRuntime() displayRuntime {
	info, err := os.Stdout.Stat()
	tty := err == nil && info.Mode()&os.ModeCharDevice != 0
	width := 80
	if tty {
		width = terminalWidth(os.Stdout.Fd())
	}
	color := tty && os.Getenv("NO_COLOR") == "" && strings.TrimSpace(os.Getenv("TERM")) != "" && strings.TrimSpace(os.Getenv("TERM")) != "dumb"
	return displayRuntime{TTY: tty, Width: width, Color: color}
}

func parseListDisplayArgs(args []string) (displayOptions, error) {
	var options displayOptions
	for _, arg := range args {
		switch arg {
		case "--all":
			options.all = true
		case "--plain":
			if options.mode == displayModeJSON {
				return displayOptions{}, fmt.Errorf("--plain and --json are mutually exclusive")
			}
			options.mode = displayModePlain
		case "--json":
			if options.mode == displayModePlain {
				return displayOptions{}, fmt.Errorf("--plain and --json are mutually exclusive")
			}
			options.mode = displayModeJSON
		default:
			return displayOptions{}, fmt.Errorf("unexpected list argument %q", arg)
		}
	}
	return options, nil
}

func parseStatusDisplayArgs(args []string) (string, displayOptions, error) {
	var options displayOptions
	unit := ""
	for _, arg := range args {
		switch arg {
		case "--plain":
			if options.mode == displayModeJSON {
				return "", displayOptions{}, fmt.Errorf("--plain and --json are mutually exclusive")
			}
			options.mode = displayModePlain
		case "--json":
			if options.mode == displayModePlain {
				return "", displayOptions{}, fmt.Errorf("--plain and --json are mutually exclusive")
			}
			options.mode = displayModeJSON
		case "--verbose":
			options.verbose = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", displayOptions{}, fmt.Errorf("unexpected status argument %q", arg)
			}
			if unit != "" {
				return "", displayOptions{}, fmt.Errorf("status accepts exactly one unit")
			}
			unit = strings.TrimSuffix(strings.TrimSpace(arg), ".service")
		}
	}
	if unit == "" {
		return "", displayOptions{}, fmt.Errorf("status requires a unit")
	}
	if options.mode == displayModeJSON && options.verbose {
		return "", displayOptions{}, fmt.Errorf("--json and --verbose are mutually exclusive")
	}
	return unit, options, nil
}
