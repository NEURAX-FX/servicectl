package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	snapshot, err := queryUnitSnapshotViaSysvisionMode(ctx, config.Mode, unitName)
	return snapshot, err == nil
}

func queryUnitSnapshotViaSysvisionMode(ctx context.Context, mode, unitName string) (visionapi.UnitSnapshot, error) {
	resp, err := sysvisionRequestMode(ctx, mode, "/v1/query/unit/"+strings.TrimSuffix(unitName, ".service")+".service")
	if err != nil {
		return visionapi.UnitSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return visionapi.UnitSnapshot{}, fmt.Errorf("sysvision unit query returned %s", resp.Status)
	}
	var snapshot visionapi.UnitSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return visionapi.UnitSnapshot{}, err
	}
	return snapshot, nil
}

func queryUnitSnapshotsViaSysvision() (visionapi.UnitsResponse, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := sysvisionRequest(ctx, "/v1/query/units?refresh=0")
	if err != nil {
		return visionapi.UnitsResponse{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return visionapi.UnitsResponse{}, false
	}
	var snapshots visionapi.UnitsResponse
	if err := json.NewDecoder(resp.Body).Decode(&snapshots); err != nil {
		return visionapi.UnitsResponse{}, false
	}
	return snapshots, true
}

func queryBusMetaViaSysvision() (sysvisionMetaResponse, bool) {
	return queryBusMetaViaSysvisionMode(config.Mode)
}

func queryBusMetaViaSysvisionMode(mode string) (sysvisionMetaResponse, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	meta, err := queryBusMetaViaSysvisionContext(ctx, mode)
	return meta, err == nil
}

func queryBusMetaViaSysvisionContext(ctx context.Context, mode string) (sysvisionMetaResponse, error) {
	resp, err := sysvisionRequestMode(ctx, mode, "/v1/meta")
	if err != nil {
		return sysvisionMetaResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return sysvisionMetaResponse{}, fmt.Errorf("sysvision meta returned %s", resp.Status)
	}
	var meta sysvisionMetaResponse
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return sysvisionMetaResponse{}, err
	}
	return meta, nil
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
