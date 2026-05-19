package util

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestYesNo(t *testing.T) {
	if got := YesNo(true); got != "yes" {
		t.Fatalf("YesNo(true) = %q, want %q", got, "yes")
	}
	if got := YesNo(false); got != "no" {
		t.Fatalf("YesNo(false) = %q, want %q", got, "no")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{name: "empty", in: nil, want: ""},
		{name: "all blank", in: []string{"", "  ", "\t"}, want: ""},
		{name: "first value", in: []string{"hello", "world"}, want: "hello"},
		{name: "skip blanks", in: []string{"", "  ", "x"}, want: "x"},
		{name: "trims result", in: []string{"  trimmed  "}, want: "trimmed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FirstNonEmpty(tt.in...); got != tt.want {
				t.Fatalf("FirstNonEmpty(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWriteJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteJSON(rr, map[string]string{"hello": "world"})
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var decoded map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if decoded["hello"] != "world" {
		t.Fatalf("decoded = %v, want hello=world", decoded)
	}
	if !strings.Contains(rr.Body.String(), "\n") {
		t.Fatalf("expected pretty-printed JSON to contain newline, got %q", rr.Body.String())
	}
}

func TestJournalIdentifier(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "servicectl[service]"},
		{"  ", "servicectl[service]"},
		{"foo", "servicectl[foo]"},
		{"foo.service", "servicectl[foo]"},
		{"  foo.service  ", "servicectl[foo]"},
	}
	for _, tt := range tests {
		if got := JournalIdentifier(tt.in); got != tt.want {
			t.Fatalf("JournalIdentifier(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeS6Name(t *testing.T) {
	tests := []struct {
		in, fallback, want string
	}{
		{"", "group", "group"},
		{"   ", "service", "service"},
		{"foo/bar.baz", "x", "foo-bar-baz"},
		{"a:b c", "x", "a-b-c"},
		{"---hello---", "x", "hello"},
		{"/././", "fallback", "fallback"},
	}
	for _, tt := range tests {
		if got := SanitizeS6Name(tt.in, tt.fallback); got != tt.want {
			t.Fatalf("SanitizeS6Name(%q,%q) = %q, want %q", tt.in, tt.fallback, got, tt.want)
		}
	}
}

func TestExternalManagedValueEnabled(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{" yes ", true},
		{"on", true},
		{"0", false},
		{"false", false},
		{"off", false},
		{"", false},
		{"random", false},
	}
	for _, tt := range tests {
		if got := ExternalManagedValueEnabled(tt.value); got != tt.want {
			t.Fatalf("ExternalManagedValueEnabled(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}
