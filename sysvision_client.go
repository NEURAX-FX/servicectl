package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"servicectl/internal/visionapi"
)

type sysvisionMetaResponse struct {
	visionapi.MetaResponse
}

func sysvisionAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	resp, err := sysvisionRequest(ctx, "/v1/query/units")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func queryUnitSnapshotViaSysvision(unitName string) (visionapi.UnitSnapshot, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := sysvisionRequest(ctx, "/v1/query/unit/"+strings.TrimSuffix(unitName, ".service")+".service")
	if err != nil {
		return visionapi.UnitSnapshot{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return visionapi.UnitSnapshot{}, false
	}
	var snapshot visionapi.UnitSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return visionapi.UnitSnapshot{}, false
	}
	return snapshot, true
}

func queryBusMetaViaSysvision() (sysvisionMetaResponse, bool) {
	return queryBusMetaViaSysvisionMode(config.Mode)
}

func queryBusMetaViaSysvisionMode(mode string) (sysvisionMetaResponse, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	resp, err := sysvisionRequestMode(ctx, mode, "/v1/meta")
	if err != nil {
		return sysvisionMetaResponse{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return sysvisionMetaResponse{}, false
	}
	var meta sysvisionMetaResponse
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return sysvisionMetaResponse{}, false
	}
	return meta, true
}

func sysvisionRequest(ctx context.Context, path string) (*http.Response, error) {
	return sysvisionRequestMode(ctx, config.Mode, path)
}

func sysvisionRequestMode(ctx context.Context, mode string, path string) (*http.Response, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", visionapi.SysvisionSocketPathForMode(mode))
		},
	}
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}
