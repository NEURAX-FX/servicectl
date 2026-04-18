package main

import "testing"

func TestShouldAutoResolveGroupAction(t *testing.T) {
	tests := []struct {
		action string
		raw    string
		want   bool
	}{
		{action: "enable", raw: "hermes-gateway", want: false},
		{action: "disable", raw: "hermes-gateway", want: false},
		{action: "is-enabled", raw: "hermes-gateway", want: false},
		{action: "enable", raw: "group:default", want: true},
		{action: "enable", raw: "multi-user.target", want: true},
		{action: "status", raw: "group:default", want: false},
		{action: "start", raw: "group:default", want: false},
	}

	for _, tt := range tests {
		if got := shouldAutoResolveGroupAction(tt.action, tt.raw); got != tt.want {
			t.Fatalf("shouldAutoResolveGroupAction(%q, %q) = %v, want %v", tt.action, tt.raw, got, tt.want)
		}
	}
}
