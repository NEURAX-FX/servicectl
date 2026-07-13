package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"servicectl/internal/statusview"
	"servicectl/internal/util"
	"servicectl/internal/visionapi"
)

var statusSyslogPaths = []string{
	"/var/log/messages",
	"/var/log/syslog",
	"/var/log/daemon.log",
	"/var/log/user.log",
}

type statusLogDependencies struct {
	command  func(context.Context, string, ...string) ([]byte, error)
	readFile func(string) ([]byte, error)
}

type journalStatusLog struct {
	RealtimeTimestamp string `json:"__REALTIME_TIMESTAMP"`
	Priority          string `json:"PRIORITY"`
	Transport         string `json:"_TRANSPORT"`
	Sequence          string `json:"__SEQNUM"`
	Message           string `json:"MESSAGE"`
}

func defaultStatusLogDependencies() statusLogDependencies {
	return statusLogDependencies{
		command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		readFile: func(path string) ([]byte, error) {
			return readStatusLogTail(path, 1<<20)
		},
	}
}

func readStatusLogTail(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if maximum > 0 && info.Size() > maximum {
		start = info.Size() - maximum
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, maximum))
}

func collectStatusLogs(ctx context.Context, unitName, mode string, observedAt time.Time, deps statusLogDependencies) ([]statusview.LogEntry, []statusview.Diagnostic) {
	if deps.command == nil {
		deps.command = defaultStatusLogDependencies().command
	}
	if deps.readFile == nil {
		deps.readFile = defaultStatusLogDependencies().readFile
	}

	args := []string{"--no-pager", "--output=json"}
	if visionapi.PlaneForMode(mode).Mode == visionapi.ModeUser {
		args = append(args, "--user")
	}
	args = append(args,
		"-n", "50",
		"--until", observedAt.UTC().Format(time.RFC3339Nano),
		"SYSLOG_IDENTIFIER="+util.JournalIdentifier(unitName),
	)
	output, commandErr := deps.command(ctx, "journalctl", args...)
	if commandErr == nil {
		entries, parseErr := parseJournalStatusLogs(output)
		if parseErr == nil {
			return normalizeStatusLogs(entries), nil
		}
		commandErr = parseErr
	}

	fallback := collectStatusSyslogFallback(unitName, observedAt, deps.readFile)
	if len(fallback) > 0 {
		return normalizeStatusLogs(fallback), nil
	}
	return []statusview.LogEntry{}, []statusview.Diagnostic{{
		Severity:      statusview.SeverityInfo,
		Domain:        statusview.DomainOutput,
		Code:          "logs_unavailable",
		Message:       "Recent service logs are unavailable.",
		AffectsHealth: false,
		ObservedAt:    observedAt.UTC(),
		Source:        "journalctl",
		Hint:          strings.TrimSpace(commandErr.Error()),
	}}
}

func parseJournalStatusLogs(data []byte) ([]statusview.LogEntry, error) {
	entries := make([]statusview.LogEntry, 0)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var record journalStatusLog
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, fmt.Errorf("parse journal JSON line %d: %w", line, err)
		}
		micros, err := strconv.ParseInt(strings.TrimSpace(record.RealtimeTimestamp), 10, 64)
		if err != nil || micros < 0 {
			return nil, fmt.Errorf("parse journal timestamp on line %d", line)
		}
		sequence, _ := strconv.ParseUint(strings.TrimSpace(record.Sequence), 10, 64)
		stream := strings.TrimSpace(record.Transport)
		if stream == "" {
			stream = "unknown"
		}
		entries = append(entries, statusview.LogEntry{
			Timestamp:      time.Unix(0, micros*int64(time.Microsecond)).UTC(),
			SourceSequence: sequence,
			Stream:         stream,
			Severity:       journalPrioritySeverity(record.Priority),
			Message:        record.Message,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan journal JSON: %w", err)
	}
	return normalizeStatusLogs(entries), nil
}

func journalPrioritySeverity(raw string) statusview.LogSeverity {
	priority, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return statusview.LogUnknown
	}
	switch priority {
	case 0, 1, 2:
		return statusview.LogCritical
	case 3:
		return statusview.LogError
	case 4:
		return statusview.LogWarning
	case 5, 6:
		return statusview.LogInfo
	case 7:
		return statusview.LogDebug
	default:
		return statusview.LogUnknown
	}
}

func normalizeStatusLogs(entries []statusview.LogEntry) []statusview.LogEntry {
	result := append([]statusview.LogEntry(nil), entries...)
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if !left.Timestamp.Equal(right.Timestamp) {
			return left.Timestamp.Before(right.Timestamp)
		}
		if left.SourceSequence != right.SourceSequence {
			return left.SourceSequence < right.SourceSequence
		}
		if left.Stream != right.Stream {
			return left.Stream < right.Stream
		}
		if left.Severity != right.Severity {
			return left.Severity < right.Severity
		}
		return left.Message < right.Message
	})
	if len(result) > 50 {
		result = result[len(result)-50:]
	}
	if result == nil {
		return []statusview.LogEntry{}
	}
	return result
}

func selectDefaultStatusLogs(entries []statusview.LogEntry) []statusview.LogEntry {
	entries = normalizeStatusLogs(entries)
	if len(entries) <= 5 {
		return entries
	}
	newestError := -1
	for i := range entries {
		if entries[i].Severity == statusview.LogError || entries[i].Severity == statusview.LogCritical {
			newestError = i
		}
	}
	if newestError < 0 {
		return append([]statusview.LogEntry(nil), entries[len(entries)-5:]...)
	}
	start := newestError - 2
	if start < 0 {
		start = 0
	}
	end := start + 5
	if end > len(entries) {
		end = len(entries)
		start = end - 5
	}
	return append([]statusview.LogEntry(nil), entries[start:end]...)
}

func collectStatusSyslogFallback(unitName string, observedAt time.Time, readFile func(string) ([]byte, error)) []statusview.LogEntry {
	identifier := util.JournalIdentifier(unitName)
	entries := make([]statusview.LogEntry, 0)
	var sequence uint64
	for _, path := range statusSyslogPaths {
		content, err := readFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" || !strings.Contains(line, identifier) {
				continue
			}
			sequence++
			entries = append(entries, statusview.LogEntry{
				Timestamp:      observedAt.UTC(),
				SourceSequence: sequence,
				Stream:         "syslog",
				Severity:       statusview.LogUnknown,
				Message:        line,
			})
		}
	}
	return entries
}
