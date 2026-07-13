package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"servicectl/internal/visionapi"
)

type propertyUpdateRequest struct {
	Key        string `json:"key"`
	Value      string `json:"value"`
	Persistent bool   `json:"persistent"`
	Mode       string `json:"mode,omitempty"`
}

type resolvedTarget struct {
	Input  string `json:"input"`
	Group  string `json:"group"`
	Target string `json:"target,omitempty"`
}

func propertyRequest(ctx context.Context, method string, path string, body any) (*http.Response, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", visionapi.SystemPropertySocketPath())
		},
	}
	client := &http.Client{Transport: transport}
	var payloadReader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payloadReader = bytes.NewReader(payload)
	} else {
		payloadReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, payloadReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return client.Do(req)
}

func propertyResolveTarget(raw string) (resolvedTarget, error) {
	_ = propertyReload()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := propertyRequest(ctx, http.MethodGet, "/v1/resolve-target?mode="+url.QueryEscape(config.Mode)+"&name="+url.QueryEscape(strings.TrimSpace(raw)), nil)
	if err != nil {
		return resolvedTarget{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resolvedTarget{}, fmt.Errorf("property resolve returned %s", resp.Status)
	}
	var out resolvedTarget
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return resolvedTarget{}, err
	}
	return out, nil
}

func propertyReload() error {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := propertyRequest(ctx, http.MethodPost, "/v1/reload?mode="+url.QueryEscape(config.Mode), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("property reload returned %s", resp.Status)
	}
	return nil
}

func propertySet(key string, value string, persistent bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	body := propertyUpdateRequest{Key: key, Value: value, Persistent: persistent}
	if !persistent {
		body.Mode = config.Mode
	}
	resp, err := propertyRequest(ctx, http.MethodPost, "/v1/property", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("property update returned %s", resp.Status)
	}
	return nil
}

func propertySetEnabledUnit(unit string, present bool) error {
	return propertyUnitListMembership("/v1/enabled-unit", visionapi.UnitListMembershipRequest{
		Mode: config.Mode, Unit: strings.TrimSpace(unit), Present: present,
	})
}

func propertySetEnabledGroup(group string, present bool) error {
	return propertyUnitListMembership("/v1/enabled-group", visionapi.UnitListMembershipRequest{
		Mode: config.Mode, Group: strings.TrimSpace(group), Present: present,
	})
}

func propertyReplaceEnabledUnits(units []string) error {
	return propertyReplaceUnitListForMode(config.Mode, "/v1/enabled-list", units)
}

func propertyReplaceRunnerUnits(units []string) error {
	return propertyReplaceUnitListForMode(config.Mode, "/v1/runner-list", units)
}

func propertySetRunnerUnit(unit string, present bool) error {
	return propertySetRunnerUnitForMode(config.Mode, unit, present)
}

func propertyUnitLists() (visionapi.UnitListsResponse, error) {
	return propertyUnitListsForMode(config.Mode)
}

func propertyUnitListsForMode(mode string) (visionapi.UnitListsResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := propertyRequest(ctx, http.MethodGet, "/v1/unit-lists?mode="+url.QueryEscape(mode), nil)
	if err != nil {
		return visionapi.UnitListsResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return visionapi.UnitListsResponse{}, fmt.Errorf("unit lists query returned %s", resp.Status)
	}
	var out visionapi.UnitListsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return visionapi.UnitListsResponse{}, err
	}
	return out, nil
}

func propertyReplaceUnitListForMode(mode string, path string, units []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := propertyRequest(ctx, http.MethodPut, path, visionapi.UnitListReplaceRequest{Mode: mode, Units: units})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unit list replacement returned %s", resp.Status)
	}
	return nil
}

func propertySetRunnerUnitForMode(mode, unit string, present bool) error {
	return propertyUnitListMembership("/v1/runner-unit", visionapi.UnitListMembershipRequest{
		Mode: mode, Unit: strings.TrimSpace(unit), Present: present,
	})
}

func propertyUnitListMembership(path string, request visionapi.UnitListMembershipRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := propertyRequest(ctx, http.MethodPost, path, request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unit list update returned %s", resp.Status)
	}
	return nil
}

func propertyValue(key string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := propertyRequest(ctx, http.MethodGet, "/v1/properties?mode="+url.QueryEscape(config.Mode), nil)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var out visionapi.PropertiesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false
	}
	for _, prop := range out.Properties {
		if strings.TrimSpace(prop.Key) == key {
			return prop.Value, true
		}
	}
	return "", false
}

func queryGroupState(name string) (visionapi.GroupState, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := propertyRequest(ctx, http.MethodGet, "/v1/group/"+url.PathEscape(strings.TrimSpace(name))+"?mode="+url.QueryEscape(config.Mode), nil)
	if err != nil {
		return visionapi.GroupState{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return visionapi.GroupState{}, false
	}
	var out visionapi.GroupState
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return visionapi.GroupState{}, false
	}
	return out, true
}

func queryAllGroups() ([]visionapi.GroupState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := propertyRequest(ctx, http.MethodGet, "/v1/groups?mode="+url.QueryEscape(config.Mode), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("group query returned %s", resp.Status)
	}
	var out visionapi.GroupsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Groups, nil
}

func queryUnitGroups(name string) (visionapi.UnitGroupsResponse, bool) {
	_ = propertyReload()
	out, err := propertyUnitGroupsForMode(config.Mode, name)
	return out, err == nil
}

func propertyUnitGroupsForMode(mode, name string) (visionapi.UnitGroupsResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := propertyRequest(ctx, http.MethodGet, "/v1/unit-groups/"+url.PathEscape(strings.TrimSpace(name))+"?mode="+url.QueryEscape(mode), nil)
	if err != nil {
		return visionapi.UnitGroupsResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return visionapi.UnitGroupsResponse{}, fmt.Errorf("unit group query returned %s", resp.Status)
	}
	var out visionapi.UnitGroupsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return visionapi.UnitGroupsResponse{}, err
	}
	return out, nil
}
