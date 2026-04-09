package main

import "testing"

func TestEffectiveEnabledFromFlags(t *testing.T) {
	tests := []struct {
		name     string
		managed  bool
		external bool
		want     bool
	}{
		{name: "neither managed", managed: false, external: false, want: false},
		{name: "internally managed", managed: true, external: false, want: true},
		{name: "externally managed", managed: false, external: true, want: true},
		{name: "both managed modes", managed: true, external: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveEnabledFromFlags(tt.managed, tt.external); got != tt.want {
				t.Fatalf("effectiveEnabledFromFlags(%v, %v) = %v, want %v", tt.managed, tt.external, got, tt.want)
			}
		})
	}
}

func TestManagementStateFromFlags(t *testing.T) {
	tests := []struct {
		name     string
		managed  bool
		external bool
		want     string
	}{
		{name: "unmanaged", managed: false, external: false, want: "unmanaged"},
		{name: "internal", managed: true, external: false, want: "internal"},
		{name: "external", managed: false, external: true, want: "external"},
		{name: "internal and external", managed: true, external: true, want: "internal+external"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := managementStateFromFlags(tt.managed, tt.external); got != tt.want {
				t.Fatalf("managementStateFromFlags(%v, %v) = %q, want %q", tt.managed, tt.external, got, tt.want)
			}
		})
	}
}

func TestExternalManagedValueEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "numeric true", value: "1", want: true},
		{name: "word true", value: "true", want: true},
		{name: "yes with whitespace", value: " yes ", want: true},
		{name: "off", value: "off", want: false},
		{name: "zero", value: "0", want: false},
		{name: "empty", value: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := externalManagedValueEnabled(tt.value); got != tt.want {
				t.Fatalf("externalManagedValueEnabled(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
