// Package util holds tiny string and HTTP helpers shared across the
// servicectl binaries (root package main and cmd/* daemons). Anything that
// landed here did so because it was previously copy-pasted across two or more
// packages; keep this file boring on purpose.
package util

import (
	"encoding/json"
	"net/http"
	"strings"
)

// YesNo returns "yes" for true and "no" for false. Used in human-readable
// status output and key=value telemetry payloads.
func YesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// FirstNonEmpty returns the first argument whose TrimSpace value is non-empty,
// returning that trimmed value. Returns "" if every argument is blank.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// WriteJSON writes value as a pretty-printed JSON response with the
// application/json content type. Encode errors are intentionally swallowed:
// callers cannot do anything useful with them once headers are flushed.
func WriteJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(value)
}

// JournalIdentifier returns the SyslogIdentifier we want journald to attach to
// a service's stdout/stderr lines. Tolerates names supplied with or without a
// trailing ".service" suffix and falls back to "service" for empty input.
func JournalIdentifier(serviceName string) string {
	clean := strings.TrimSuffix(strings.TrimSpace(serviceName), ".service")
	if clean == "" {
		clean = "service"
	}
	return "servicectl[" + clean + "]"
}

// SanitizeS6Name produces a filesystem-safe s6 service name from a free-form
// input. Slashes, dots, colons and spaces collapse to "-" and leading/trailing
// dashes are trimmed. Falls back to fallback when the result is empty so each
// caller can pick its own context-appropriate placeholder ("group", "service",
// etc).
func SanitizeS6Name(value, fallback string) string {
	replacer := strings.NewReplacer("/", "-", ".", "-", ":", "-", " ", "-")
	clean := strings.Trim(replacer.Replace(strings.TrimSpace(value)), "-")
	if clean == "" {
		return fallback
	}
	return clean
}

// ExternalManagedValueEnabled interprets the persisted property value used to
// flag an externally-managed unit. Truthy strings ("1", "true", "yes", "on")
// are treated as enabled; everything else (including empty) is disabled.
// Comparison is case-insensitive and tolerant of surrounding whitespace.
func ExternalManagedValueEnabled(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
