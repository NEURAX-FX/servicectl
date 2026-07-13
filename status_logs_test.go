package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"servicectl/internal/statusview"
	"servicectl/internal/visionapi"
)

func TestParseJournalStatusLogs(t *testing.T) {
	input := strings.Join([]string{
		`{"__REALTIME_TIMESTAMP":"1783852198000000","PRIORITY":"6","_TRANSPORT":"stdout","__SEQNUM":"81","MESSAGE":"ready 雪\nnext"}`,
		`{"__REALTIME_TIMESTAMP":"1783852197000000","PRIORITY":"3","_TRANSPORT":"stderr","MESSAGE":"failed"}`,
	}, "\n")

	got, err := parseJournalStatusLogs([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2", len(got))
	}
	if got[0].Message != "failed" || got[0].Severity != statusview.LogError || got[0].Stream != "stderr" || got[0].SourceSequence != 0 {
		t.Fatalf("first entry = %#v", got[0])
	}
	if got[1].Message != "ready 雪\nnext" || got[1].Severity != statusview.LogInfo || got[1].Stream != "stdout" || got[1].SourceSequence != 81 {
		t.Fatalf("second entry = %#v", got[1])
	}
	if got[1].Timestamp.Format(time.RFC3339Nano) != "2026-07-12T10:29:58Z" {
		t.Fatalf("timestamp = %s", got[1].Timestamp.Format(time.RFC3339Nano))
	}
}

func TestParseJournalStatusLogsPriorityMapping(t *testing.T) {
	want := []statusview.LogSeverity{
		statusview.LogCritical,
		statusview.LogCritical,
		statusview.LogCritical,
		statusview.LogError,
		statusview.LogWarning,
		statusview.LogInfo,
		statusview.LogInfo,
		statusview.LogDebug,
		statusview.LogUnknown,
	}
	for priority, severity := range want {
		got := journalPrioritySeverity(priorityString(priority))
		if got != severity {
			t.Fatalf("priority %d = %q, want %q", priority, got, severity)
		}
	}
}

func TestCollectStatusLogs(t *testing.T) {
	observedAt := time.Date(2026, 7, 12, 10, 30, 0, 123456789, time.UTC)
	var gotName string
	var gotArgs []string
	deps := statusLogDependencies{
		command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			gotName = name
			gotArgs = append([]string(nil), args...)
			return []byte(`{"__REALTIME_TIMESTAMP":"1783866600000000","PRIORITY":"6","MESSAGE":"ready"}` + "\n"), nil
		},
		readFile: func(string) ([]byte, error) {
			t.Fatal("fallback read should not run")
			return nil, nil
		},
	}

	logs, diagnostics := collectStatusLogs(context.Background(), "demo.service", visionapi.ModeUser, observedAt, deps)
	if len(logs) != 1 || len(diagnostics) != 0 {
		t.Fatalf("logs/diagnostics = %#v / %#v", logs, diagnostics)
	}
	if gotName != "journalctl" {
		t.Fatalf("command = %q", gotName)
	}
	wantArgs := []string{
		"--no-pager",
		"--output=json",
		"--user",
		"-n", "50",
		"--until", observedAt.Format(time.RFC3339Nano),
		"SYSLOG_IDENTIFIER=servicectl[demo]",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestCollectStatusLogsFallsBackToSyslog(t *testing.T) {
	observedAt := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	deps := statusLogDependencies{
		command: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("journal unavailable")
		},
		readFile: func(path string) ([]byte, error) {
			if path != "/var/log/messages" {
				return nil, errors.New("missing")
			}
			return []byte("other line\nJul 12 host servicectl[demo]: fallback 雪\n"), nil
		},
	}
	logs, diagnostics := collectStatusLogs(context.Background(), "demo", visionapi.ModeSystem, observedAt, deps)
	if len(logs) != 1 || logs[0].Message != "Jul 12 host servicectl[demo]: fallback 雪" || logs[0].Severity != statusview.LogUnknown || logs[0].SourceSequence != 1 {
		t.Fatalf("logs = %#v", logs)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestCollectStatusLogsUnavailableIsInformational(t *testing.T) {
	observedAt := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	deps := statusLogDependencies{
		command: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("journal unavailable")
		},
		readFile: func(string) ([]byte, error) { return nil, errors.New("missing") },
	}
	logs, diagnostics := collectStatusLogs(context.Background(), "demo", visionapi.ModeSystem, observedAt, deps)
	if len(logs) != 0 || len(diagnostics) != 1 {
		t.Fatalf("logs/diagnostics = %#v / %#v", logs, diagnostics)
	}
	diagnostic := diagnostics[0]
	if diagnostic.Domain != statusview.DomainOutput || diagnostic.Severity != statusview.SeverityInfo || diagnostic.AffectsHealth || diagnostic.Code != "logs_unavailable" {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
}

func TestSelectDefaultStatusLogs(t *testing.T) {
	entries := makeStatusLogEntries(8)
	got := selectDefaultStatusLogs(entries)
	if messages := statusLogMessages(got); !reflect.DeepEqual(messages, []string{"line 4", "line 5", "line 6", "line 7", "line 8"}) {
		t.Fatalf("messages = %v", messages)
	}

	entries[5].Severity = statusview.LogError
	got = selectDefaultStatusLogs(entries)
	if messages := statusLogMessages(got); !reflect.DeepEqual(messages, []string{"line 4", "line 5", "line 6", "line 7", "line 8"}) {
		t.Fatalf("centered messages = %v", messages)
	}

	entries[1].Severity = statusview.LogCritical
	got = selectDefaultStatusLogs(entries)
	if messages := statusLogMessages(got); !reflect.DeepEqual(messages, []string{"line 4", "line 5", "line 6", "line 7", "line 8"}) {
		t.Fatalf("newest error messages = %v", messages)
	}
}

func TestSelectDefaultStatusLogsErrorAtEdges(t *testing.T) {
	for _, index := range []int{0, 4} {
		entries := makeStatusLogEntries(5)
		entries[index].Severity = statusview.LogCritical
		got := selectDefaultStatusLogs(entries)
		if !reflect.DeepEqual(got, entries) {
			t.Fatalf("index %d: entries = %#v", index, got)
		}
	}
}

func TestNormalizeStatusLogsNewestFiftyDeterministic(t *testing.T) {
	entries := makeStatusLogEntries(55)
	reversed := append([]statusview.LogEntry(nil), entries...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	first := normalizeStatusLogs(entries)
	second := normalizeStatusLogs(reversed)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("normalized logs depend on input order")
	}
	if len(first) != 50 || first[0].Message != "line 6" || first[49].Message != "line 55" {
		t.Fatalf("normalized logs = %d, first=%q last=%q", len(first), first[0].Message, first[len(first)-1].Message)
	}
}

func TestReadStatusLogTailIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readStatusLogTail(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "6789" {
		t.Fatalf("tail = %q, want 6789", got)
	}
}

func makeStatusLogEntries(count int) []statusview.LogEntry {
	base := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	entries := make([]statusview.LogEntry, count)
	for i := range entries {
		entries[i] = statusview.LogEntry{
			Timestamp:      base.Add(time.Duration(i) * time.Second),
			SourceSequence: uint64(i + 1),
			Stream:         "stdout",
			Severity:       statusview.LogInfo,
			Message:        "line " + priorityString(i+1),
		}
	}
	return entries
}

func statusLogMessages(entries []statusview.LogEntry) []string {
	result := make([]string, len(entries))
	for i, entry := range entries {
		result[i] = entry.Message
	}
	return result
}

func priorityString(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}
