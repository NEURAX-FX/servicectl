package visionapi

import (
	"strings"
	"testing"
)

func TestValidateStatusParticipationManifestRejectsMalformedAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*StatusParticipationManifest)
		want   string
	}{
		{
			name: "mode and scope disagree",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Mode = ModeUser
			},
			want: "scope",
		},
		{
			name: "user uid and scope disagree",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Mode = ModeUser
				manifest.UID = 1000
				manifest.Scope = "user@1001"
				for i := range manifest.Components {
					manifest.Components[i].Scope = "user@1001"
					manifest.Components[i].Key = strings.Replace(manifest.Components[i].Key, ":system:", ":user@1001:", 1)
				}
				for i := range manifest.Relationships {
					manifest.Relationships[i].From = strings.Replace(manifest.Relationships[i].From, ":system:", ":user@1001:", 1)
					manifest.Relationships[i].To = strings.Replace(manifest.Relationships[i].To, ":system:", ":user@1001:", 1)
				}
			},
			want: "scope",
		},
		{
			name: "unit is not canonical",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Unit = "demo"
			},
			want: "canonical",
		},
		{
			name: "source is empty",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Source = ""
			},
			want: "source",
		},
		{
			name: "generation time is invalid",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.GeneratedAt = "not-a-time"
			},
			want: "generated_at",
		},
		{
			name: "component key does not match identity",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Components[0].Key = "service:system:other.service"
				manifest.Relationships[0].To = manifest.Components[0].Key
			},
			want: "stable key",
		},
		{
			name: "unknown component type",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Components[1].Type = "mystery"
				manifest.Components[1].Key = "mystery:system:demo.service"
				manifest.Relationships[0].From = manifest.Components[1].Key
			},
			want: "unsupported type",
		},
		{
			name: "component scope disagrees with manifest",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Components[1].Scope = "user@1000"
				manifest.Components[1].Key = "sys-orchestrd:user@1000:demo.service"
				manifest.Relationships[0].From = manifest.Components[1].Key
			},
			want: "scope",
		},
		{
			name: "runtime component lacks service name",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Components[1].ServiceName = ""
			},
			want: "service_name",
		},
		{
			name: "service identity does not match unit",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Components[0].Identity = "other.service"
				manifest.Components[0].Key = "service:system:other.service"
				manifest.Relationships[0].To = manifest.Components[0].Key
			},
			want: "service component",
		},
		{
			name: "relationship uses non applicable namespace",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Namespaces[2].Applicable = false
			},
			want: "not applicable",
		},
		{
			name: "relationship relation is unsupported",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Relationships[0].Relation = "depends_on"
			},
			want: "unsupported relation",
		},
		{
			name: "relationship uses wrong namespace for relation",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Relationships[0].Primary = false
				manifest.Relationships[0].Namespace = StatusNamespaceAccounting
			},
			want: "namespace",
		},
		{
			name: "declared component is disconnected",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Components = append(manifest.Components, StatusManifestComponent{
					Key: "sysvisiond:system:system", Type: "sysvisiond", Name: "sysvisiond", Scope: "system", Identity: "system", ServiceName: "sysvisiond",
				})
			},
			want: "disconnected",
		},
		{
			name: "duplicate relationship",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Relationships = append(manifest.Relationships, manifest.Relationships[0])
			},
			want: "duplicate relationship",
		},
		{
			name: "primary relationship outside control namespace",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Relationships[0].Namespace = StatusNamespaceObservation
			},
			want: "primary relationship",
		},
		{
			name: "primary path does not end at service",
			mutate: func(manifest *StatusParticipationManifest) {
				manifest.Relationships[0].From, manifest.Relationships[0].To = manifest.Relationships[0].To, manifest.Relationships[0].From
			},
			want: "primary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validStatusParticipationManifest()
			tt.mutate(&manifest)
			err := ValidateStatusParticipationManifest(manifest)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateStatusParticipationManifestAcceptsServiceOnlyPath(t *testing.T) {
	manifest := validStatusParticipationManifest()
	manifest.Namespaces[2].Applicable = false
	manifest.Components = manifest.Components[:1]
	manifest.Relationships = []StatusManifestRelationship{}
	if err := ValidateStatusParticipationManifest(manifest); err != nil {
		t.Fatalf("service-only manifest: %v", err)
	}
}

func validStatusParticipationManifest() StatusParticipationManifest {
	return StatusParticipationManifest{
		Version:     StatusManifestVersion,
		Complete:    true,
		Unit:        "demo.service",
		Mode:        ModeSystem,
		Scope:       "system",
		Source:      "manager",
		GeneratedAt: "2026-07-12T10:30:00Z",
		Namespaces: []StatusManifestNamespace{
			{Name: StatusNamespaceAccounting, Complete: true},
			{Name: StatusNamespaceBus, Complete: true},
			{Name: StatusNamespaceControl, Applicable: true, Complete: true},
			{Name: StatusNamespaceObservation, Complete: true},
		},
		Components: []StatusManifestComponent{
			{Key: "service:system:demo.service", Type: "service", Name: "demo.service", Scope: "system", Identity: "demo.service", ServiceName: "demo.service"},
			{Key: "sys-orchestrd:system:demo.service", Type: "sys-orchestrd", Name: "sys-orchestrd", Scope: "system", Identity: "demo.service", ServiceName: "demo-orchestrd"},
		},
		Relationships: []StatusManifestRelationship{
			{Namespace: StatusNamespaceControl, From: "sys-orchestrd:system:demo.service", To: "service:system:demo.service", Relation: "supervises", Primary: true},
		},
	}
}
