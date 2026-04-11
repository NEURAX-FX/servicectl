package main

import "testing"

func TestShouldAutoResolveGroupAction(t *testing.T) {
	tests := []struct {
		action string
		want   bool
	}{
		{action: "enable", want: true},
		{action: "disable", want: true},
		{action: "is-enabled", want: true},
		{action: "status", want: false},
		{action: "start", want: false},
	}

	for _, tt := range tests {
		if got := shouldAutoResolveGroupAction(tt.action); got != tt.want {
			t.Fatalf("shouldAutoResolveGroupAction(%q) = %v, want %v", tt.action, got, tt.want)
		}
	}
}
