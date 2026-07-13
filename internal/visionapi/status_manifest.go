package visionapi

import (
	"fmt"
	"strings"
	"time"

	"servicectl/internal/statusview"
)

func ValidateStatusParticipationManifest(manifest StatusParticipationManifest) error {
	if manifest.Version != StatusManifestVersion {
		return fmt.Errorf("unsupported status manifest version %d", manifest.Version)
	}
	if !manifest.Complete {
		return fmt.Errorf("status manifest is incomplete")
	}
	if strings.TrimSpace(manifest.Unit) == "" || strings.TrimSpace(manifest.Scope) == "" {
		return fmt.Errorf("status manifest requires unit and scope")
	}
	canonicalUnit, err := statusview.CanonicalUnitName(manifest.Unit)
	if err != nil || canonicalUnit != manifest.Unit {
		return fmt.Errorf("status manifest unit %q is not canonical", manifest.Unit)
	}
	if manifest.Mode != ModeSystem && manifest.Mode != ModeUser {
		return fmt.Errorf("status manifest has unsupported mode %q", manifest.Mode)
	}
	wantScope, err := statusview.CanonicalScope(manifest.Mode, int(manifest.UID))
	if err != nil || manifest.Scope != wantScope {
		return fmt.Errorf("status manifest scope %q does not match mode %q and UID %d", manifest.Scope, manifest.Mode, manifest.UID)
	}
	if strings.TrimSpace(manifest.Source) == "" {
		return fmt.Errorf("status manifest requires source")
	}
	generatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(manifest.GeneratedAt))
	if err != nil || generatedAt.IsZero() {
		return fmt.Errorf("status manifest generated_at is invalid")
	}

	wantNamespaces := map[string]bool{
		StatusNamespaceAccounting:  true,
		StatusNamespaceBus:         true,
		StatusNamespaceControl:     true,
		StatusNamespaceObservation: true,
	}
	seenNamespaces := make(map[string]bool, len(manifest.Namespaces))
	applicableNamespaces := make(map[string]bool, len(manifest.Namespaces))
	for _, namespace := range manifest.Namespaces {
		if !wantNamespaces[namespace.Name] {
			return fmt.Errorf("unknown status manifest namespace %q", namespace.Name)
		}
		if seenNamespaces[namespace.Name] {
			return fmt.Errorf("duplicate status manifest namespace %q", namespace.Name)
		}
		if !namespace.Complete {
			return fmt.Errorf("status manifest namespace %q is incomplete", namespace.Name)
		}
		seenNamespaces[namespace.Name] = true
		applicableNamespaces[namespace.Name] = namespace.Applicable
	}
	for namespace := range wantNamespaces {
		if !seenNamespaces[namespace] {
			return fmt.Errorf("status manifest namespace %q is missing", namespace)
		}
	}

	components := make(map[string]StatusManifestComponent, len(manifest.Components))
	allowedComponentTypes := map[string]bool{
		"service":        true,
		"servicectl-api": true,
		"s6":             true,
		"sys-orchestrd":  true,
		"dinit":          true,
		"sys-notifyd":    true,
		"sysvbus":        true,
		"dbus":           true,
		"sysvisiond":     true,
		"sys-cgroupd":    true,
	}
	serviceCount := 0
	serviceKey := ""
	for _, component := range manifest.Components {
		if strings.TrimSpace(component.Key) == "" || strings.TrimSpace(component.Type) == "" || strings.TrimSpace(component.Name) == "" || strings.TrimSpace(component.Scope) == "" || strings.TrimSpace(component.Identity) == "" {
			return fmt.Errorf("status manifest component has incomplete identity")
		}
		wantKey, err := statusview.NewNodeID(component.Type, component.Scope, component.Identity)
		if err != nil || component.Key != wantKey {
			return fmt.Errorf("status manifest component %q has invalid stable key, want %q", component.Key, wantKey)
		}
		if _, ok := components[component.Key]; ok {
			return fmt.Errorf("duplicate status manifest component key %q", component.Key)
		}
		if !allowedComponentTypes[component.Type] {
			return fmt.Errorf("status manifest component %q has unsupported type %q", component.Key, component.Type)
		}
		if component.Scope != manifest.Scope && !(component.Type == "sys-cgroupd" && component.Scope == "system") {
			return fmt.Errorf("status manifest component %q scope %q does not match manifest scope %q", component.Key, component.Scope, manifest.Scope)
		}
		if component.Type != "dbus" && strings.TrimSpace(component.ServiceName) == "" {
			return fmt.Errorf("status manifest component %q requires service_name", component.Key)
		}
		if component.Type == "dbus" && strings.TrimSpace(component.BusName) == "" {
			return fmt.Errorf("status manifest D-Bus component %q requires bus_name", component.Key)
		}
		components[component.Key] = component
		if component.Type == "service" {
			serviceCount++
			serviceKey = component.Key
			if component.Scope != manifest.Scope || component.Identity != manifest.Unit || component.ServiceName != manifest.Unit {
				return fmt.Errorf("status manifest service component does not match unit identity")
			}
		}
	}
	if serviceCount != 1 {
		return fmt.Errorf("status manifest has %d service components, want 1", serviceCount)
	}

	allowedRelations := map[string]bool{
		string(statusview.RelationControls):   true,
		string(statusview.RelationActivates):  true,
		string(statusview.RelationSupervises): true,
		string(statusview.RelationAccounts):   true,
		string(statusview.RelationObserves):   true,
	}
	seenRelationships := make(map[string]bool, len(manifest.Relationships))
	adjacent := make(map[string][]string, len(components))
	primaryIncoming := make(map[string]int)
	primaryOutgoing := make(map[string]int)
	primaryNext := make(map[string]string)
	primaryNodes := make(map[string]bool)
	primaryEdges := 0
	for _, relationship := range manifest.Relationships {
		if _, ok := components[relationship.From]; !ok {
			return fmt.Errorf("relationship from %q references missing component", relationship.From)
		}
		if _, ok := components[relationship.To]; !ok {
			return fmt.Errorf("relationship to %q references missing component", relationship.To)
		}
		if !seenNamespaces[relationship.Namespace] {
			return fmt.Errorf("relationship uses unknown namespace %q", relationship.Namespace)
		}
		if relationship.Primary && relationship.Namespace != StatusNamespaceControl {
			return fmt.Errorf("primary relationship %q to %q must use control namespace", relationship.From, relationship.To)
		}
		if !applicableNamespaces[relationship.Namespace] {
			return fmt.Errorf("relationship uses namespace %q which is not applicable", relationship.Namespace)
		}
		if !allowedRelations[relationship.Relation] {
			return fmt.Errorf("relationship %q to %q has unsupported relation %q", relationship.From, relationship.To, relationship.Relation)
		}
		if !statusManifestRelationUsesNamespace(relationship.Relation, relationship.Namespace) {
			return fmt.Errorf("relationship %q to %q relation %q uses incompatible namespace %q", relationship.From, relationship.To, relationship.Relation, relationship.Namespace)
		}
		relationshipKey := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%t", relationship.Namespace, relationship.From, relationship.To, relationship.Relation, relationship.Primary)
		if seenRelationships[relationshipKey] {
			return fmt.Errorf("duplicate relationship %q to %q", relationship.From, relationship.To)
		}
		seenRelationships[relationshipKey] = true
		adjacent[relationship.From] = append(adjacent[relationship.From], relationship.To)
		adjacent[relationship.To] = append(adjacent[relationship.To], relationship.From)
		if !relationship.Primary {
			continue
		}
		primaryEdges++
		primaryNodes[relationship.From] = true
		primaryNodes[relationship.To] = true
		primaryOutgoing[relationship.From]++
		primaryIncoming[relationship.To]++
		if primaryOutgoing[relationship.From] > 1 || primaryIncoming[relationship.To] > 1 {
			return fmt.Errorf("status manifest primary path branches at %q or %q", relationship.From, relationship.To)
		}
		primaryNext[relationship.From] = relationship.To
	}
	if primaryEdges == 0 {
		if applicableNamespaces[StatusNamespaceControl] {
			return fmt.Errorf("status manifest control namespace is applicable but primary path is empty")
		}
		return validateStatusManifestConnectivity(serviceKey, components, adjacent)
	}
	if !applicableNamespaces[StatusNamespaceControl] {
		return fmt.Errorf("status manifest primary path requires applicable control namespace")
	}
	roots := make([]string, 0, 1)
	terminals := make([]string, 0, 1)
	for key := range primaryNodes {
		if primaryIncoming[key] == 0 {
			roots = append(roots, key)
		}
		if primaryOutgoing[key] == 0 {
			terminals = append(terminals, key)
		}
	}
	if len(roots) != 1 || len(terminals) != 1 || terminals[0] != serviceKey {
		return fmt.Errorf("status manifest primary path must have one root and end at the service component")
	}
	visited := make(map[string]bool, len(primaryNodes))
	for current := roots[0]; current != ""; current = primaryNext[current] {
		if visited[current] {
			return fmt.Errorf("status manifest primary path contains a cycle")
		}
		visited[current] = true
	}
	if len(visited) != len(primaryNodes) {
		return fmt.Errorf("status manifest primary path is disconnected")
	}
	return validateStatusManifestConnectivity(serviceKey, components, adjacent)
}

func statusManifestRelationUsesNamespace(relation, namespace string) bool {
	switch relation {
	case string(statusview.RelationControls), string(statusview.RelationSupervises):
		return namespace == StatusNamespaceControl
	case string(statusview.RelationActivates):
		return namespace == StatusNamespaceBus
	case string(statusview.RelationAccounts):
		return namespace == StatusNamespaceAccounting
	case string(statusview.RelationObserves):
		return namespace == StatusNamespaceBus || namespace == StatusNamespaceObservation
	default:
		return false
	}
}

func validateStatusManifestConnectivity(serviceKey string, components map[string]StatusManifestComponent, adjacent map[string][]string) error {
	visited := make(map[string]bool, len(components))
	queue := []string{serviceKey}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if visited[current] {
			continue
		}
		visited[current] = true
		for _, next := range adjacent[current] {
			if !visited[next] {
				queue = append(queue, next)
			}
		}
	}
	if len(visited) != len(components) {
		return fmt.Errorf("status manifest component graph is disconnected: reached %d of %d components", len(visited), len(components))
	}
	return nil
}
