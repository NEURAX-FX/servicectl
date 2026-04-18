package main

import (
	"bytes"
	"io"
	"log"
	"log/syslog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeSyslog struct {
	info []string
	err  []string
}

func (f *fakeSyslog) Info(msg string) error {
	f.info = append(f.info, msg)
	return nil
}

func (f *fakeSyslog) Err(msg string) error {
	f.err = append(f.err, msg)
	return nil
}

func TestRecordAttemptFailureFusesAfterThreshold(t *testing.T) {
	tmp := t.TempDir()
	d := &daemon{
		logger:      log.New(io.Discard, "", 0),
		stateFile:   filepath.Join(tmp, "state"),
		serviceName: "group-pipewire-orchestrd",
		maxFailures: 3,
	}

	for i := 0; i < 3; i++ {
		d.recordAttemptFailure(&operationError{Executor: "servicectl", Action: "start", Target: "foo.service", Err: io.EOF})
	}

	if !d.fused {
		t.Fatalf("expected daemon to be fused after threshold")
	}
	if d.failureCount != 3 {
		t.Fatalf("expected failure count 3, got %d", d.failureCount)
	}
	content := readTextFile(t, d.stateFile)
	if !strings.Contains(content, "fused=yes") {
		t.Fatalf("expected state file to contain fused=yes, got %q", content)
	}
}

func TestClearFuseResetsFailureState(t *testing.T) {
	tmp := t.TempDir()
	d := &daemon{
		logger:      log.New(io.Discard, "", 0),
		stateFile:   filepath.Join(tmp, "state"),
		serviceName: "openai-cpa-orchestrd",
		maxFailures: 2,
	}
	d.recordAttemptFailure(&operationError{Executor: "servicectl", Action: "start", Target: "foo.service", Err: io.EOF})
	d.recordAttemptFailure(&operationError{Executor: "servicectl", Action: "start", Target: "foo.service", Err: io.EOF})

	d.clearFuse("group.changed")

	if d.fused {
		t.Fatalf("expected fuse to be cleared")
	}
	if d.failureCount != 0 {
		t.Fatalf("expected failure count reset, got %d", d.failureCount)
	}
}

func TestOrchestrdServiceNameForGroupAndUnit(t *testing.T) {
	group := orchestrdServiceName("", "pipewire")
	if group != "group-pipewire-orchestrd" {
		t.Fatalf("unexpected group service name: %s", group)
	}
	unit := orchestrdServiceName("NetworkManager.service", "")
	if unit != "NetworkManager-orchestrd" {
		t.Fatalf("unexpected unit service name: %s", unit)
	}
}

func TestAttemptStartWithRetrySuppressedByFuse(t *testing.T) {
	d := &daemon{
		logger:      log.New(io.Discard, "", 0),
		serviceName: "group-pipewire-orchestrd",
		maxFailures: 3,
		fused:       true,
		fuseReason:  "unit arp-ethers not found",
	}
	called := 0
	ok := d.attemptStartWithRetry("group-enabled", func() *operationError {
		called++
		return nil
	})
	if ok {
		t.Fatalf("expected suppressed attempt to return false")
	}
	if called != 0 {
		t.Fatalf("expected runner to never execute when fused, got %d", called)
	}
}

func TestPermanentStartErrorClassification(t *testing.T) {
	if !isPermanentStartError("unit arp-ethers not found") {
		t.Fatalf("expected not found to be permanent")
	}
	if isPermanentStartError("dial unix /run/servicectl/sysvisiond.sock: connect: no such file or directory") {
		t.Fatalf("expected transient dial failure to be non-permanent")
	}
}

func TestRecordAttemptSuccessLogsOnlyToSyslog(t *testing.T) {
	var terminal bytes.Buffer
	sys := &fakeSyslog{}
	d := &daemon{
		logger:      log.New(&terminal, "", 0),
		syslogger:   sys,
		serviceName: "slurmctld-orchestrd",
		unit:        "slurmctld.service",
		maxFailures: 3,
		failureCount: 2,
		fused:       true,
		fuseReason:  "previous error",
	}

	d.recordAttemptSuccess("initial-start")

	if strings.Contains(terminal.String(), "attempt succeeded") {
		t.Fatalf("expected attempt success log to stay off terminal, got %q", terminal.String())
	}
	if len(sys.info) != 1 || !strings.Contains(sys.info[0], "attempt succeeded") {
		t.Fatalf("expected attempt success log in syslog, got %#v", sys.info)
	}
}

func TestClearFuseLogsOnlyToSyslog(t *testing.T) {
	var terminal bytes.Buffer
	sys := &fakeSyslog{}
	d := &daemon{
		logger:       log.New(&terminal, "", 0),
		syslogger:    sys,
		serviceName:  "slurmctld-orchestrd",
		unit:         "slurmctld.service",
		maxFailures:  3,
		failureCount: 3,
		fused:        true,
		fuseReason:   "start failed",
		stateFile:    filepath.Join(t.TempDir(), "state"),
	}

	d.clearFuse("group-change-enabled")

	if strings.Contains(terminal.String(), "circuit fuse cleared") {
		t.Fatalf("expected fuse clear log to stay off terminal, got %q", terminal.String())
	}
	if len(sys.info) == 0 || !strings.Contains(sys.info[0], "circuit fuse cleared") {
		t.Fatalf("expected fuse clear log in syslog, got %#v", sys.info)
	}
}

func TestRecordAttemptFailureStillLogsToTerminalAndSyslog(t *testing.T) {
	var terminal bytes.Buffer
	sys := &fakeSyslog{}
	d := &daemon{
		logger:      log.New(&terminal, "", 0),
		syslogger:   sys,
		serviceName: "slurmctld-orchestrd",
		unit:        "slurmctld.service",
		maxFailures: 3,
		stateFile:   filepath.Join(t.TempDir(), "state"),
	}

	d.recordAttemptFailure(&operationError{Executor: "servicectl", Action: "start", Target: "slurmctld.service", Err: io.EOF})

	if !strings.Contains(terminal.String(), "attempt failed") {
		t.Fatalf("expected failure log on terminal, got %q", terminal.String())
	}
	if len(sys.err) == 0 || !strings.Contains(sys.err[0], "attempt failed") {
		t.Fatalf("expected failure log in syslog, got %#v", sys.err)
	}
}

func TestFuseOpenStillLogsToTerminalAndSyslog(t *testing.T) {
	var terminal bytes.Buffer
	sys := &fakeSyslog{}
	d := &daemon{
		logger:      log.New(&terminal, "", 0),
		syslogger:   sys,
		serviceName: "slurmctld-orchestrd",
		unit:        "slurmctld.service",
		maxFailures: 1,
		stateFile:   filepath.Join(t.TempDir(), "state"),
	}

	d.recordAttemptFailure(&operationError{Executor: "servicectl", Action: "start", Target: "slurmctld.service", Err: io.EOF})

	if !strings.Contains(terminal.String(), "circuit fuse open") {
		t.Fatalf("expected fuse-open log on terminal, got %q", terminal.String())
	}
	found := false
	for _, msg := range sys.err {
		if strings.Contains(msg, "circuit fuse open") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected fuse-open log in syslog, got %#v", sys.err)
	}
}

func TestTypedNilSyslogWriterDoesNotPanic(t *testing.T) {
	var terminal bytes.Buffer
	var sysw *syslog.Writer
	d := &daemon{
		logger:      log.New(&terminal, "", 0),
		syslogger:   sysw,
		serviceName: "slurmctld-orchestrd",
		unit:        "slurmctld.service",
		maxFailures: 1,
		stateFile:   filepath.Join(t.TempDir(), "state"),
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected typed nil syslog writer to be ignored, got panic: %v", r)
		}
	}()

	d.recordAttemptFailure(&operationError{Executor: "servicectl", Action: "start", Target: "slurmctld.service", Err: io.EOF})
	d.recordAttemptSuccess("initial-start")
}

func readTextFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	return string(b)
}
