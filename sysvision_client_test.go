package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestQueryUnitSnapshotsViaSysvisionRefreshesBeforeListing(t *testing.T) {
	requestedPath := ""
	request := func(_ context.Context, path string) (*http.Response, error) {
		requestedPath = path
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"units":[]}`)),
		}, nil
	}

	if _, ok := queryUnitSnapshotsViaSysvisionWithRequest(context.Background(), request); !ok {
		t.Fatal("query failed")
	}
	if requestedPath != "/v1/query/units" {
		t.Fatalf("requested path = %q, want a synchronous refresh", requestedPath)
	}
}
