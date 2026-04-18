package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/syslog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"servicectl/internal/visionapi"
)

type daemon struct {
	unit       string
	group      string
	userMode   bool
	runtime    string
	state      string
	stateFile  string
	serviceName string
	logger     *log.Logger
	syslogger  syslogSink
	maxFailures int
	failureCount int
	fused     bool
	fuseReason string
	groups     map[string]bool
	groupUnits []string
	debug      *startupDebugger
}

type syslogSink interface {
	Info(string) error
	Err(string) error
}

func newSyslogSink(priority syslog.Priority, tag string) syslogSink {
	sysw, err := syslog.New(priority, tag)
	if err != nil || sysw == nil {
		return nil
	}
	return sysw
}

func usableSyslogSink(sink syslogSink) syslogSink {
	if sink == nil {
		return nil
	}
	if writer, ok := sink.(*syslog.Writer); ok && writer == nil {
		return nil
	}
	return sink
}

type operationError struct {
	Executor  string
	Action    string
	Target    string
	Detail    string
	Err       error
	Permanent bool
}

func (e *operationError) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{}
	if strings.TrimSpace(e.Executor) != "" {
		parts = append(parts, "executor="+e.Executor)
	}
	if strings.TrimSpace(e.Action) != "" {
		parts = append(parts, "action="+e.Action)
	}
	if strings.TrimSpace(e.Target) != "" {
		parts = append(parts, "target="+e.Target)
	}
	if strings.TrimSpace(e.Detail) != "" {
		parts = append(parts, "detail="+strings.ReplaceAll(strings.TrimSpace(e.Detail), "\n", " | "))
	}
	if e.Err != nil {
		parts = append(parts, "error="+e.Err.Error())
	}
	return strings.Join(parts, " ")
}

func (e *operationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func main() {
	unit := flag.String("unit", "", "target unit")
	group := flag.String("group", "", "target group")
	userMode := flag.Bool("user", false, "run in user mode")
	flag.Parse()
	if (strings.TrimSpace(*unit) == "") == (strings.TrimSpace(*group) == "") {
		fmt.Fprintln(os.Stderr, "sys-orchestrd requires exactly one of --unit or --group")
		os.Exit(2)
	}
	logger := log.New(os.Stdout, "sys-orchestrd: ", log.LstdFlags)
	runtime := visionapi.RuntimeDir(*userMode, os.Getenv("XDG_RUNTIME_DIR"))
	stateName := strings.TrimSpace(*unit)
	if strings.TrimSpace(*group) != "" {
		stateName = "group:" + strings.TrimSpace(*group)
	}
	serviceName := orchestrdServiceName(strings.TrimSpace(*unit), strings.TrimSpace(*group))
	logger = log.New(os.Stdout, "sys-orchestrd["+serviceName+"]: ", log.LstdFlags)
	sysw := newSyslogSink(syslog.LOG_INFO|syslog.LOG_DAEMON, "servicectl["+serviceName+"]")
	d := &daemon{unit: strings.TrimSpace(*unit), group: strings.TrimSpace(*group), userMode: *userMode, runtime: runtime, logger: logger, syslogger: sysw, state: "waiting", stateFile: orchestrdStateFile(stateName, *userMode, runtime), serviceName: serviceName, maxFailures: maxFailureThreshold(), groups: map[string]bool{}, debug: newStartupDebugger()}
	if sink := usableSyslogSink(d.syslogger); sink != nil {
		if closer, ok := sink.(interface{ Close() error }); ok {
			defer closer.Close()
		}
	}
	if err := d.run(); err != nil {
		d.logError("fatal err=%v", err)
		os.Exit(1)
	}
}

func (d *daemon) run() error {
	if err := os.MkdirAll(filepath.Dir(d.stateFile), 0755); err != nil {
		return err
	}
	if d.isGroupMode() {
		return d.runGroup()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	d.writeState("waiting", "startup")
	d.initialSync(ctx)
	events := make(chan visionapi.EventEnvelope, 32)
	go d.watchEvents(ctx, events)
	for {
		select {
		case <-ctx.Done():
			d.writeState("stopping", "signal")
			_ = d.runServicectl("stop")
			d.publishState("stopping", "signal")
			return nil
		case event := <-events:
			if exitErr := d.handleEvent(event); exitErr != nil {
				d.logError("non-fatal event handler error: %v", exitErr)
			}
		}
	}
}

func (d *daemon) runGroup() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	d.writeState("waiting", "startup")
	d.initialGroupSync()
	events := make(chan visionapi.EventEnvelope, 32)
	go d.watchEvents(ctx, events)
	go d.maintainMissingGroup(ctx.Done())
	for {
		select {
		case <-ctx.Done():
			d.writeState("stopping", "signal")
			_ = d.runGroupAction("stop", reverseServiceOrder(d.groupUnits))
			d.publishState("stopping", "signal")
			return nil
		case event := <-events:
			if exitErr := d.handleEvent(event); exitErr != nil {
				d.logError("non-fatal event handler error: %v", exitErr)
			}
		}
	}
}

func (d *daemon) initialSync(ctx context.Context) error {
	for ctx.Err() == nil {
		snapshot, err := d.queryUnit(d.unit)
		if err != nil {
			d.writeState("waiting", "initial-sync")
			d.logError("initial sync failed: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}
		if snapshot.State == "STARTED" || snapshot.Phase == "ready" || snapshot.ChildState == "running" {
			d.refreshGroups()
			d.writeState("running", "initial-state")
			d.publishState("running", "initial-state")
			return nil
		}
		d.writeState("starting", "initial-start")
		d.publishState("starting", "initial-start")
		d.refreshGroups()
		if !d.attemptStartWithRetry("initial-start", func() *operationError { return d.runServicectl("start") }) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}
		return nil
	}
	return nil
}

func (d *daemon) handleEvent(event visionapi.EventEnvelope) error {
	switch event.Source {
	case visionapi.SourceSysNotifyd:
		failure := strings.TrimSpace(event.Payload["failure"])
		phase := strings.TrimSpace(event.Payload["phase"])
		child := strings.TrimSpace(event.Payload["child_state"])
		if failure != "" {
			d.writeState("failed", failure)
			d.publishState("failed", failure)
			d.logError("notifyd reported failure unit=%s detail=%s", d.unit, failure)
			return nil
		}
		if phase == "ready" || child == "running" {
			d.writeState("running", firstNonEmpty(phase, child))
			d.publishState("running", firstNonEmpty(phase, child))
		}
		if phase == "stopping" || child == "stopping" {
			d.writeState("stopping", firstNonEmpty(phase, child))
			d.publishState("stopping", firstNonEmpty(phase, child))
		}
	case visionapi.SourceServicectl:
		if event.Payload["action"] == "stop" && event.Payload["result"] == "ok" {
			d.writeState("waiting", "stopped")
			d.publishState("waiting", "stopped")
		}
	case visionapi.SourceSysPropertyd:
		if event.Kind == visionapi.KindGroupChanged || event.Kind == visionapi.KindPropertyChanged {
			d.clearFuse("property-change")
		}
		if event.Kind == visionapi.KindGroupChanged {
			return d.handleGroupScopedChange(event)
		}
	}
	return nil
}

func (d *daemon) handleGroupChange(event visionapi.EventEnvelope) error {
	group := strings.TrimSpace(event.Payload["group"])
	if group == "" || !d.groups[group] {
		return nil
	}
	enabled := strings.EqualFold(strings.TrimSpace(event.Payload["enabled"]), "yes") || strings.TrimSpace(event.Payload["enabled"]) == "1"
	if enabled {
		snapshot, err := d.queryUnit(d.unit)
		if err != nil {
			d.recordAttemptFailure(&operationError{Executor: "sysvision-api", Action: "query-unit", Target: d.unit, Err: err, Permanent: false})
			return nil
		}
		if snapshot.State == "STARTED" || snapshot.Phase == "ready" || snapshot.ChildState == "running" {
			return nil
		}
		d.writeState("starting", "group-enabled:"+group)
		d.publishState("starting", "group-enabled:"+group)
		if !d.attemptStartWithRetry("group-enabled:"+group, func() *operationError { return d.runServicectl("start") }) {
			return nil
		}
		return nil
	}
	d.writeState("stopping", "group-disabled:"+group)
	d.publishState("stopping", "group-disabled:"+group)
	if err := d.runServicectl("stop"); err != nil {
		d.recordAttemptFailure(err)
		return nil
	}
	d.recordAttemptSuccess("group-disabled:" + group)
	d.writeState("waiting", "group-disabled:"+group)
	d.publishState("waiting", "group-disabled:"+group)
	return nil
}

func (d *daemon) watchEvents(ctx context.Context, events chan<- visionapi.EventEnvelope) {
	for ctx.Err() == nil {
		if err := d.watchEventsOnce(ctx, events); err != nil && ctx.Err() == nil {
			d.logError("sysvision watch failed: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (d *daemon) watchEventsOnce(ctx context.Context, events chan<- visionapi.EventEnvelope) error {
	path := "/v1/watch?mode=" + url.QueryEscape(visionapi.ModeForUser(d.userMode))
	resp, err := d.sysvisionRequest(ctx, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sysvision watch returned %s", resp.Status)
	}
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		var event visionapi.EventEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- event:
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return io.EOF
}

func (d *daemon) runServicectl(action string) *operationError {
	bin := os.Getenv("SERVICECTL_BIN")
	if strings.TrimSpace(bin) == "" {
		bin = "servicectl"
	}
	d.debug.logMungeProbe("before-servicectl-" + action)
	d.debug.event("orchestrd.before-servicectl-start", map[string]any{"action": action, "unit": d.unit, "user_mode": d.userMode, "bin": bin})
	args := []string{action, d.unit}
	if d.userMode {
		args = append([]string{"--user"}, args...)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	fields := map[string]any{"action": action, "unit": d.unit, "user_mode": d.userMode, "ok": err == nil}
	if err != nil {
		fields["error"] = err.Error()
	}
	d.debug.event("orchestrd.after-servicectl-start", fields)
	if err == nil {
		return nil
	}
	perm := isPermanentStartError(err.Error())
	return &operationError{Executor: "servicectl", Action: action, Target: d.unit, Err: err, Permanent: perm}
}

func (d *daemon) runGroupAction(action string, units []string) *operationError {
	for _, unit := range units {
		d.unit = strings.TrimSuffix(strings.TrimSpace(unit), ".service") + ".service"
		if err := d.runServicectl(action); err != nil {
			return err
		}
	}
	return nil
}

func (d *daemon) writeState(state string, reason string) {
	d.state = state
	content := strings.Join([]string{
		"unit=" + d.unit,
		"service=" + d.serviceName,
		"state=" + state,
		"reason=" + reason,
		"failure_count=" + strconv.Itoa(d.failureCount),
		"fused=" + yesNo(d.fused),
		"updated_at=" + time.Now().UTC().Format(time.RFC3339Nano),
	}, "\n") + "\n"
	_ = os.WriteFile(d.stateFile, []byte(content), 0644)
}

func (d *daemon) publishState(state string, reason string) {
	payload := map[string]string{"state": state, "reason": reason, "service": d.serviceName, "failure_count": strconv.Itoa(d.failureCount), "fused": yesNo(d.fused)}
	envelope := visionapi.NewEvent(visionapi.ModeForUser(d.userMode), visionapi.SourceSysOrchestrd, visionapi.KindUnitOrchestration, d.objectName(), payload)
	data, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	addr := &net.UnixAddr{Name: visionapi.SysvisionIngressSocketPath(d.userMode, d.runtime), Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write(data)
}

func (d *daemon) queryGroup(name string) (visionapi.GroupState, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := d.sysvisionRequest(ctx, "/v1/query/group/"+url.PathEscape(strings.TrimSpace(name)))
	if err != nil {
		d.logError("api failure executor=sysvision-api action=query-group target=%s err=%v", strings.TrimSpace(name), err)
		return visionapi.GroupState{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		d.logError("api failure executor=sysvision-api action=query-group target=%s status=%s", strings.TrimSpace(name), resp.Status)
		return visionapi.GroupState{}, false
	}
	var out visionapi.GroupState
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		d.logError("api failure executor=sysvision-api action=query-group target=%s decode=%v", strings.TrimSpace(name), err)
		return visionapi.GroupState{}, false
	}
	return out, true
}

func (d *daemon) queryUnit(unit string) (visionapi.UnitSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := d.sysvisionRequest(ctx, "/v1/query/unit/"+url.PathEscape(strings.TrimSuffix(unit, ".service")+".service"))
	if err != nil {
		return visionapi.UnitSnapshot{}, &operationError{Executor: "sysvision-api", Action: "query-unit", Target: unit, Err: err, Permanent: false}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return visionapi.UnitSnapshot{}, &operationError{Executor: "sysvision-api", Action: "query-unit", Target: unit, Err: fmt.Errorf("sysvision query returned %s", resp.Status), Permanent: false}
	}
	var snapshot visionapi.UnitSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return visionapi.UnitSnapshot{}, &operationError{Executor: "sysvision-api", Action: "query-unit", Target: unit, Err: err, Permanent: false}
	}
	return snapshot, nil
}

func (d *daemon) refreshGroups() {
	resp, ok := d.queryUnitGroups(d.unit)
	if !ok {
		return
	}
	groups := make(map[string]bool, len(resp.Groups))
	for _, group := range resp.Groups {
		groups[strings.TrimSpace(group.Name)] = true
	}
	d.groups = groups
}

func (d *daemon) queryUnitGroups(unit string) (visionapi.UnitGroupsResponse, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := d.sysvisionRequest(ctx, "/v1/query/unit-groups/"+url.PathEscape(strings.TrimSuffix(unit, ".service")+".service"))
	if err != nil {
		return visionapi.UnitGroupsResponse{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return visionapi.UnitGroupsResponse{}, false
	}
	var out visionapi.UnitGroupsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return visionapi.UnitGroupsResponse{}, false
	}
	return out, true
}

func (d *daemon) sysvisionRequest(ctx context.Context, path string) (*http.Response, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", visionapi.SysvisionSocketPath(d.userMode, d.runtime))
	}}
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func orchestrdStateFile(unit string, userMode bool, runtime string) string {
	if value := strings.TrimSpace(os.Getenv("SYS_ORCHESTRD_STATE_FILE")); value != "" {
		return value
	}
	name := strings.TrimSuffix(strings.TrimSpace(unit), ".service") + ".state"
	return filepath.Join(visionapi.RuntimeDir(userMode, runtime), "orchestrd", name)
}

func sanitizeS6Name(value string) string {
	replacer := strings.NewReplacer("/", "-", ".", "-", ":", "-", " ", "-")
	clean := strings.Trim(replacer.Replace(strings.TrimSpace(value)), "-")
	if clean == "" {
		return "service"
	}
	return clean
}

func orchestrdServiceName(unit string, group string) string {
	if strings.TrimSpace(group) != "" {
		return "group-" + sanitizeS6Name(group) + "-orchestrd"
	}
	base := strings.TrimSuffix(strings.TrimSpace(unit), ".service")
	return sanitizeS6Name(base) + "-orchestrd"
}

func maxFailureThreshold() int {
	value := strings.TrimSpace(os.Getenv("SERVICECTL_ORCHESTRD_MAX_FAILURES"))
	if value == "" {
		return 3
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 3
	}
	return n
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func isPermanentStartError(msg string) bool {
	v := strings.ToLower(strings.TrimSpace(msg))
	if v == "" {
		return false
	}
	return strings.Contains(v, "not found") || strings.Contains(v, "cycle detected") || strings.Contains(v, "invalid")
}

func (d *daemon) logInfo(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if d.logger != nil {
		d.logger.Print(msg)
	}
	if sink := usableSyslogSink(d.syslogger); sink != nil {
		_ = sink.Info(msg)
	}
}

func (d *daemon) logInfoSyslogOnly(format string, args ...any) {
	if sink := usableSyslogSink(d.syslogger); sink != nil {
		_ = sink.Info(fmt.Sprintf(format, args...))
	}
}

func (d *daemon) logError(format string, args ...any) {
	if d.logger != nil {
		d.logger.Printf(format, args...)
	}
	if sink := usableSyslogSink(d.syslogger); sink != nil {
		_ = sink.Err(fmt.Sprintf(format, args...))
	}
}

func (d *daemon) recordAttemptSuccess(reason string) {
	d.failureCount = 0
	if d.fused {
		d.fused = false
		d.fuseReason = ""
	}
	d.logInfoSyslogOnly("attempt succeeded service=%s object=%s reason=%s", d.serviceName, d.objectName(), reason)
}

func (d *daemon) recordAttemptFailure(err *operationError) {
	if err == nil {
		return
	}
	d.failureCount++
	d.logError("attempt failed service=%s object=%s attempt=%d/%d fused=%s %s", d.serviceName, d.objectName(), d.failureCount, d.maxFailures, yesNo(d.fused), err.Error())
	if d.failureCount >= d.maxFailures {
		d.fused = true
		d.fuseReason = err.Error()
		d.writeState("failed", "fused:"+firstNonEmpty(err.Action, "start-error"))
		d.publishState("failed", "fused:"+firstNonEmpty(err.Action, "start-error"))
		d.logError("circuit fuse open service=%s object=%s reason=%s", d.serviceName, d.objectName(), d.fuseReason)
		return
	}
	d.writeState("failed", firstNonEmpty(err.Action, "start-error"))
	d.publishState("failed", firstNonEmpty(err.Action, "start-error"))
}

func (d *daemon) clearFuse(trigger string) {
	if !d.fused && d.failureCount == 0 {
		return
	}
	d.fused = false
	d.fuseReason = ""
	d.failureCount = 0
	d.logInfoSyslogOnly("circuit fuse cleared service=%s object=%s trigger=%s", d.serviceName, d.objectName(), trigger)
	d.writeState("waiting", "fuse-cleared:"+trigger)
	d.publishState("waiting", "fuse-cleared:"+trigger)
}

func (d *daemon) attemptStartWithRetry(reason string, runner func() *operationError) bool {
	if d.fused {
		d.logError("attempt suppressed by fuse service=%s object=%s reason=%s", d.serviceName, d.objectName(), d.fuseReason)
		return false
	}
	for attempt := 0; attempt < d.maxFailures; attempt++ {
		err := runner()
		if err == nil {
			d.recordAttemptSuccess(reason)
			return true
		}
		d.recordAttemptFailure(err)
		if d.fused {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
