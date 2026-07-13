package statusview

import (
	"encoding/json"
	"testing"
)

func TestCanonicalScope(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		uid     int
		want    string
		wantErr bool
	}{
		{name: "system", mode: "system", uid: 1000, want: "system"},
		{name: "user", mode: "user", uid: 1000, want: "user@1000"},
		{name: "negative user uid", mode: "user", uid: -1, wantErr: true},
		{name: "unknown mode", mode: "session", uid: 1000, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalScope(tt.mode, tt.uid)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("CanonicalScope(%q, %d) succeeded, want error", tt.mode, tt.uid)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonicalScope(%q, %d): %v", tt.mode, tt.uid, err)
			}
			if got != tt.want {
				t.Fatalf("CanonicalScope(%q, %d) = %q, want %q", tt.mode, tt.uid, got, tt.want)
			}
		})
	}
}

func TestCanonicalUnitName(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "cliproxyapi", want: "cliproxyapi.service"},
		{input: "cliproxyapi.service", want: "cliproxyapi.service"},
		{input: "foo.bar", want: "foo.bar.service"},
		{input: "", wantErr: true},
	}

	for _, tt := range tests {
		got, err := CanonicalUnitName(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("CanonicalUnitName(%q) succeeded, want error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Fatalf("CanonicalUnitName(%q): %v", tt.input, err)
		}
		if got != tt.want {
			t.Fatalf("CanonicalUnitName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNodeID(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		scope    string
		identity string
		want     string
		wantErr  bool
	}{
		{
			name:     "user bus",
			typeName: "SysVBus",
			scope:    "user@1000",
			identity: "user@1000",
			want:     "sysvbus:user@1000:user@1000",
		},
		{
			name:     "service",
			typeName: "service",
			scope:    "system",
			identity: "cliproxyapi.service",
			want:     "service:system:cliproxyapi.service",
		},
		{
			name:     "encoded bytes",
			typeName: "custom:type%",
			scope:    "system",
			identity: "snow ☃:\x01%",
			want:     "custom%3Atype%25:system:snow%20%E2%98%83%3A%01%25",
		},
		{name: "empty type", scope: "system", identity: "system", wantErr: true},
		{name: "empty scope", typeName: "service", identity: "a.service", wantErr: true},
		{name: "empty identity", typeName: "service", scope: "system", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewNodeID(tt.typeName, tt.scope, tt.identity)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NewNodeID(%q, %q, %q) succeeded, want error", tt.typeName, tt.scope, tt.identity)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewNodeID(%q, %q, %q): %v", tt.typeName, tt.scope, tt.identity, err)
			}
			if got != tt.want {
				t.Fatalf("NewNodeID(%q, %q, %q) = %q, want %q", tt.typeName, tt.scope, tt.identity, got, tt.want)
			}
		})
	}
}

func TestNodeIDDoesNotDependOnPID(t *testing.T) {
	id, err := NewNodeID("service", "system", "cliproxyapi.service")
	if err != nil {
		t.Fatal(err)
	}

	first := NewNode(id, "service", "cliproxyapi.service", "system")
	first.PID = 100
	second := NewNode(id, "service", "cliproxyapi.service", "system")
	second.PID = 200

	if first.ID != second.ID {
		t.Fatalf("PID changed stable ID: %q != %q", first.ID, second.ID)
	}
}

func TestModelRejectsNodeIDCollision(t *testing.T) {
	model := NewModel()
	model.Orchestration.Nodes = append(model.Orchestration.Nodes,
		NewNode("service:system:a.service", "service", "a.service", "system"),
		NewNode("service:system:a.service", "service", "other", "system"),
	)

	if err := model.ValidateNodeIDs(); err == nil {
		t.Fatal("ValidateNodeIDs succeeded, want duplicate ID error")
	}
}

func TestModelInitializesRequiredSlices(t *testing.T) {
	model := NewModel()
	if model.Orchestration.Nodes == nil {
		t.Fatal("orchestration nodes are nil")
	}
	if model.Orchestration.Edges == nil {
		t.Fatal("orchestration edges are nil")
	}
	if model.Diagnostics == nil {
		t.Fatal("diagnostics are nil")
	}
	if model.Logs == nil {
		t.Fatal("logs are nil")
	}

	node := NewNode("service:system:a.service", "service", "a.service", "system")
	if node.Evidence == nil {
		t.Fatal("node evidence is nil")
	}

	model.Orchestration.Nodes = append(model.Orchestration.Nodes, node)
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	wantFragments := []string{
		`"nodes":[`,
		`"edges":[]`,
		`"evidence":[]`,
		`"diagnostics":[]`,
		`"logs":[]`,
	}
	for _, fragment := range wantFragments {
		if !containsJSONFragment(string(encoded), fragment) {
			t.Fatalf("JSON %s does not contain %s", encoded, fragment)
		}
	}
}

func containsJSONFragment(s, fragment string) bool {
	for i := 0; i+len(fragment) <= len(s); i++ {
		if s[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
