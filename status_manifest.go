package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"servicectl/internal/statusview"
	"servicectl/internal/visionapi"
)

type statusManifestInput struct {
	Unit                *Unit
	SocketUnit          *SocketUnit
	Mode                string
	UID                 uint32
	Enabled             bool
	OrchestratorService string
	Generation          uint64
	GeneratedAt         time.Time
}

type statusManifestQueryErrorKind string

const (
	statusManifestQueryTransport statusManifestQueryErrorKind = "transport"
	statusManifestQueryHTTP      statusManifestQueryErrorKind = "http"
	statusManifestQueryDecode    statusManifestQueryErrorKind = "decode"
	statusManifestQueryInvalid   statusManifestQueryErrorKind = "invalid"
)

type statusManifestQueryError struct {
	Kind       statusManifestQueryErrorKind
	StatusCode int
	Err        error
}

func (e *statusManifestQueryError) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("status manifest query returned HTTP %d: %v", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("status manifest query %s error: %v", e.Kind, e.Err)
}

func (e *statusManifestQueryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func buildStatusParticipationManifest(input statusManifestInput) (visionapi.StatusParticipationManifest, error) {
	if input.Unit == nil {
		return visionapi.StatusParticipationManifest{}, fmt.Errorf("status manifest requires a unit")
	}
	unitName, err := statusManifestUnitName(input.Unit.Name)
	if err != nil {
		return visionapi.StatusParticipationManifest{}, err
	}
	mode := visionapi.PlaneForMode(input.Mode).Mode
	scope, err := statusview.CanonicalScope(mode, int(input.UID))
	if err != nil {
		return visionapi.StatusParticipationManifest{}, err
	}
	generatedAt := input.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		return visionapi.StatusParticipationManifest{}, fmt.Errorf("status manifest requires generation time")
	}

	manifest := visionapi.StatusParticipationManifest{
		Version:     visionapi.StatusManifestVersion,
		Complete:    true,
		Unit:        unitName,
		Mode:        mode,
		UID:         input.UID,
		Scope:       scope,
		Source:      string(statusview.EvidenceUnitConfiguration),
		Generation:  input.Generation,
		GeneratedAt: generatedAt.Format(time.RFC3339Nano),
		Namespaces: []visionapi.StatusManifestNamespace{
			{Name: visionapi.StatusNamespaceAccounting, Applicable: input.Enabled, Complete: true},
			{Name: visionapi.StatusNamespaceBus, Applicable: input.Enabled, Complete: true},
			{Name: visionapi.StatusNamespaceControl, Applicable: input.Enabled, Complete: true},
			{Name: visionapi.StatusNamespaceObservation, Applicable: input.Enabled, Complete: true},
		},
		Components:    []visionapi.StatusManifestComponent{},
		Relationships: []visionapi.StatusManifestRelationship{},
	}

	serviceComponent, err := newStatusManifestComponent("service", unitName, scope, unitName, unitName, "", strings.TrimSpace(input.Unit.BusName))
	if err != nil {
		return visionapi.StatusParticipationManifest{}, err
	}
	serviceKey := serviceComponent.Key
	if !input.Enabled {
		manifest.Components = append(manifest.Components, serviceComponent)
		if err := visionapi.ValidateStatusParticipationManifest(manifest); err != nil {
			return visionapi.StatusParticipationManifest{}, err
		}
		return manifest, nil
	}

	cleanName := strings.TrimSuffix(unitName, ".service")
	managementMode := managedServiceModeForUnit(input.Unit, input.SocketUnit)
	backendName := managedServiceName(cleanName, managementMode)
	orchestrdName := strings.TrimSpace(input.OrchestratorService)
	if orchestrdName == "" {
		orchestrdName = cleanName + "-orchestrd"
	}
	apiServiceName := "servicectl-api"
	apiEndpoint := visionapi.ServicectlSocketPathForMode(mode)
	if mode == visionapi.ModeUser {
		apiServiceName += "-user-" + fmt.Sprint(input.UID)
		apiEndpoint = visionapi.ServicectlSocketPathForUID(input.UID)
	}

	apiKey, err := addStatusManifestComponent(&manifest, "servicectl-api", "servicectl-api · "+scope, scope, scope, apiServiceName, apiEndpoint, "")
	if err != nil {
		return visionapi.StatusParticipationManifest{}, err
	}
	s6Key, err := addStatusManifestComponent(&manifest, "s6", "s6 supervisor", scope, scope, orchestrdName, "", "")
	if err != nil {
		return visionapi.StatusParticipationManifest{}, err
	}
	orchestrdKey, err := addStatusManifestComponent(&manifest, "sys-orchestrd", "sys-orchestrd · "+unitName, scope, unitName, orchestrdName, "", "")
	if err != nil {
		return visionapi.StatusParticipationManifest{}, err
	}
	dinitKey, err := addStatusManifestComponent(&manifest, "dinit", "dinit · "+backendName, scope, backendName, backendName, "", "")
	if err != nil {
		return visionapi.StatusParticipationManifest{}, err
	}
	manifest.Relationships = append(manifest.Relationships,
		statusManifestRelationship(visionapi.StatusNamespaceControl, apiKey, s6Key, string(statusview.RelationControls), true),
		statusManifestRelationship(visionapi.StatusNamespaceControl, s6Key, orchestrdKey, string(statusview.RelationSupervises), true),
		statusManifestRelationship(visionapi.StatusNamespaceControl, orchestrdKey, dinitKey, string(statusview.RelationControls), true),
	)

	primaryTail := dinitKey
	if managementMode != managedDirect {
		helperKey, helperErr := addStatusManifestComponent(&manifest, "sys-notifyd", "sys-notifyd · "+backendName, scope, backendName, backendName, "", strings.TrimSpace(input.Unit.BusName))
		if helperErr != nil {
			return visionapi.StatusParticipationManifest{}, helperErr
		}
		manifest.Relationships = append(manifest.Relationships, statusManifestRelationship(visionapi.StatusNamespaceControl, primaryTail, helperKey, string(statusview.RelationSupervises), true))
		primaryTail = helperKey
	}
	manifest.Components = append(manifest.Components, serviceComponent)
	manifest.Relationships = append(manifest.Relationships, statusManifestRelationship(visionapi.StatusNamespaceControl, primaryTail, serviceKey, string(statusview.RelationSupervises), true))

	sysvbusEndpoint := visionapi.ServicectlEventsSocketPathForMode(mode)
	if mode == visionapi.ModeUser {
		sysvbusEndpoint = visionapi.ServicectlEventsSocketPathForUID(input.UID)
	}
	sysvbusKey, err := addStatusManifestComponent(&manifest, "sysvbus", "sysvbus · "+scope, scope, scope, scope, sysvbusEndpoint, "")
	if err != nil {
		return visionapi.StatusParticipationManifest{}, err
	}
	manifest.Relationships = append(manifest.Relationships, statusManifestRelationship(visionapi.StatusNamespaceBus, orchestrdKey, sysvbusKey, string(statusview.RelationObserves), false))

	if busName := strings.TrimSpace(input.Unit.BusName); busName != "" {
		dbusKey, dbusErr := addStatusManifestComponent(&manifest, "dbus", "dbus · "+scope, scope, scope, scope, "", busName)
		if dbusErr != nil {
			return visionapi.StatusParticipationManifest{}, dbusErr
		}
		manifest.Relationships = append(manifest.Relationships, statusManifestRelationship(visionapi.StatusNamespaceBus, dbusKey, serviceKey, string(statusview.RelationActivates), false))
	}

	sysvisionName := "sysvisiond"
	sysvisionEndpoint := visionapi.SysvisionSocketPathForMode(mode)
	if mode == visionapi.ModeUser {
		sysvisionName += "-user-" + fmt.Sprint(input.UID)
		sysvisionEndpoint = visionapi.SysvisionSocketPathForUID(input.UID)
	}
	sysvisionKey, err := addStatusManifestComponent(&manifest, "sysvisiond", "sysvisiond · "+scope, scope, scope, sysvisionName, sysvisionEndpoint, "")
	if err != nil {
		return visionapi.StatusParticipationManifest{}, err
	}
	manifest.Relationships = append(manifest.Relationships, statusManifestRelationship(visionapi.StatusNamespaceObservation, sysvisionKey, serviceKey, string(statusview.RelationObserves), false))

	cgroupKey, cgroupErr := addStatusManifestComponent(&manifest, "sys-cgroupd", "sys-cgroupd · system", "system", "system", "sys-cgroupd", "/run/servicectl/sys-cgroupd.sock", "")
	if cgroupErr != nil {
		return visionapi.StatusParticipationManifest{}, cgroupErr
	}
	manifest.Relationships = append(manifest.Relationships, statusManifestRelationship(visionapi.StatusNamespaceAccounting, cgroupKey, serviceKey, string(statusview.RelationAccounts), false))

	if err := visionapi.ValidateStatusParticipationManifest(manifest); err != nil {
		return visionapi.StatusParticipationManifest{}, err
	}
	return manifest, nil
}

func addStatusManifestComponent(manifest *visionapi.StatusParticipationManifest, typeName, name, scope, identity, serviceName, endpoint, busName string) (string, error) {
	component, err := newStatusManifestComponent(typeName, name, scope, identity, serviceName, endpoint, busName)
	if err != nil {
		return "", err
	}
	manifest.Components = append(manifest.Components, component)
	return component.Key, nil
}

func newStatusManifestComponent(typeName, name, scope, identity, serviceName, endpoint, busName string) (visionapi.StatusManifestComponent, error) {
	key, err := statusview.NewNodeID(typeName, scope, identity)
	if err != nil {
		return visionapi.StatusManifestComponent{}, err
	}
	return visionapi.StatusManifestComponent{
		Key:         key,
		Type:        typeName,
		Name:        name,
		Scope:       scope,
		Identity:    identity,
		ServiceName: serviceName,
		Endpoint:    endpoint,
		BusName:     busName,
	}, nil
}

func statusManifestRelationship(namespace, from, to, relation string, primary bool) visionapi.StatusManifestRelationship {
	return visionapi.StatusManifestRelationship{
		Namespace: namespace,
		From:      from,
		To:        to,
		Relation:  relation,
		Primary:   primary,
	}
}

func statusManifestUnitName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("status manifest unit name is empty")
	}
	if !strings.HasSuffix(name, ".service") {
		name += ".service"
	}
	return name, nil
}

func queryStatusManifest(ctx context.Context, mode, unit string) (visionapi.StatusParticipationManifest, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", visionapi.ServicectlSocketPathForMode(mode))
	}}
	defer transport.CloseIdleConnections()
	return queryStatusManifestWithClient(ctx, &http.Client{Transport: transport}, "http://unix", unit)
}

func queryStatusManifestWithClient(ctx context.Context, client *http.Client, baseURL, unit string) (visionapi.StatusParticipationManifest, error) {
	if client == nil {
		return visionapi.StatusParticipationManifest{}, &statusManifestQueryError{Kind: statusManifestQueryTransport, Err: fmt.Errorf("HTTP client is nil")}
	}
	canonical := strings.TrimSpace(unit)
	if canonical == "" {
		return visionapi.StatusParticipationManifest{}, &statusManifestQueryError{Kind: statusManifestQueryInvalid, Err: fmt.Errorf("unit name is empty")}
	}
	if !strings.HasSuffix(canonical, ".service") {
		canonical += ".service"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/status-manifest/"+url.PathEscape(canonical), nil)
	if err != nil {
		return visionapi.StatusParticipationManifest{}, &statusManifestQueryError{Kind: statusManifestQueryTransport, Err: err}
	}
	response, err := client.Do(request)
	if err != nil {
		return visionapi.StatusParticipationManifest{}, &statusManifestQueryError{Kind: statusManifestQueryTransport, Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = response.Status
		}
		return visionapi.StatusParticipationManifest{}, &statusManifestQueryError{Kind: statusManifestQueryHTTP, StatusCode: response.StatusCode, Err: fmt.Errorf("%s", message)}
	}

	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var manifest visionapi.StatusParticipationManifest
	if err := decoder.Decode(&manifest); err != nil {
		return visionapi.StatusParticipationManifest{}, &statusManifestQueryError{Kind: statusManifestQueryDecode, Err: err}
	}
	if err := ensureStatusManifestJSONEOF(decoder); err != nil {
		return visionapi.StatusParticipationManifest{}, &statusManifestQueryError{Kind: statusManifestQueryDecode, Err: err}
	}
	if err := visionapi.ValidateStatusParticipationManifest(manifest); err != nil {
		return visionapi.StatusParticipationManifest{}, &statusManifestQueryError{Kind: statusManifestQueryInvalid, Err: err}
	}
	return manifest, nil
}

func ensureStatusManifestJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("response contains multiple JSON values")
}

func queryStatusManifestWithTimeout(mode, unit string) (visionapi.StatusParticipationManifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	return queryStatusManifest(ctx, mode, unit)
}
