package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func readTextFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	return string(b)
}
