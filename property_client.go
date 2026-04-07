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
			return dialer.DialContext(ctx, "unix", visionapi.PropertySocketPath(userMode(), runtimeDir()))
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
	resp, err := propertyRequest(ctx, http.MethodGet, "/v1/resolve-target?name="+url.QueryEscape(strings.TrimSpace(raw)), nil)
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
	resp, err := propertyRequest(ctx, http.MethodPost, "/v1/reload", nil)
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
	resp, err := propertyRequest(ctx, http.MethodPost, "/v1/property", propertyUpdateRequest{Key: key, Value: value, Persistent: persistent})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("property update returned %s", resp.Status)
	}
	return nil
}

func queryGroupState(name string) (visionapi.GroupState, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := propertyRequest(ctx, http.MethodGet, "/v1/group/"+url.PathEscape(strings.TrimSpace(name)), nil)
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

func queryUnitGroups(name string) (visionapi.UnitGroupsResponse, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_ = propertyReload()
	resp, err := propertyRequest(ctx, http.MethodGet, "/v1/unit-groups/"+url.PathEscape(strings.TrimSpace(name)), nil)
	if err != nil {
		return visionapi.UnitGroupsResponse{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return visionapi.UnitGroupsResponse{}, false
	}
	var out visionapi.UnitGroupsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return visionapi.UnitGroupsResponse{}, false
	}
	return out, true
}
