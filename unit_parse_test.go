package main

import (
	"strings"
	"testing"
)

func TestDinitQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: `""`},
		{name: "plain", in: "foo", want: "foo"},
		{name: "path", in: "/tmp/runtime-0", want: "/tmp/runtime-0"},
		{name: "single quotes are literal", in: "'foo'", want: "'foo'"},
		{name: "space", in: "foo bar", want: `foo\ bar`},
		{name: "tab", in: "foo\tbar", want: "foo\\\tbar"},
		{name: "newline", in: "foo\nbar", want: "foo\\\nbar"},
		{name: "double quote", in: `foo"bar`, want: `foo\"bar`},
		{name: "backslash", in: `foo\bar`, want: `foo\\bar`},
		{name: "hash", in: "foo#bar", want: `foo\#bar`},
		{name: "multi", in: `Image Gen "MCP" Server`, want: `Image\ Gen\ \"MCP\"\ Server`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dinitQuote(tt.in); got != tt.want {
				t.Fatalf("dinitQuote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildEnvPrefix(t *testing.T) {
	t.Run("empty map returns empty string", func(t *testing.T) {
		if got := buildEnvPrefix(nil); got != "" {
			t.Fatalf("buildEnvPrefix(nil) = %q, want empty", got)
		}
		if got := buildEnvPrefix(map[string]string{}); got != "" {
			t.Fatalf("buildEnvPrefix(empty) = %q, want empty", got)
		}
	})

	t.Run("sorted keys", func(t *testing.T) {
		got := buildEnvPrefix(map[string]string{"B": "2", "A": "1", "C": "3"})
		want := "/usr/bin/env A=1 B=2 C=3 "
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	// Regression for the wireplumber bug: env values must NOT be wrapped in
	// single quotes. dinit's command-line parser treats single quotes as
	// literal characters, so wrapping XDG_RUNTIME_DIR in single quotes caused
	// wireplumber to see XDG_RUNTIME_DIR='/tmp/runtime-0' (with literal
	// quotes) and fail to connect to the pipewire socket.
	t.Run("path values are not single-quoted", func(t *testing.T) {
		got := buildEnvPrefix(map[string]string{"XDG_RUNTIME_DIR": "/tmp/runtime-0"})
		want := "/usr/bin/env XDG_RUNTIME_DIR=/tmp/runtime-0 "
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
		if strings.Contains(got, "'") {
			t.Fatalf("buildEnvPrefix should never emit single quotes, got %q", got)
		}
	})

	// Values containing spaces still need to survive dinit's tokenizer; we
	// backslash-escape whitespace so /usr/bin/env sees one token per value.
	t.Run("value with space is backslash-escaped", func(t *testing.T) {
		got := buildEnvPrefix(map[string]string{"NAME": "Image Gen MCP Server"})
		want := `/usr/bin/env NAME=Image\ Gen\ MCP\ Server `
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}
