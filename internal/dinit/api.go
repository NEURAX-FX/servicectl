package dinit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"servicectl/internal/visionapi"
)

// Call uses the plane's controller, or a short-lived native connection during
// bootstrap. Only a failed socket dial permits bootstrap fallback; an uncertain
// mutation is never replayed after a request may have reached the controller.
func Call(ctx context.Context, user bool, r Request) (Result, error) {
	path := visionapi.ServicectlSocketPath(user, os.Getenv("XDG_RUNTIME_DIR"))
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ECONNREFUSED) {
			return Result{}, err
		}
		controller := &Controller{Path: SocketPath(user)}
		defer controller.Close()
		return controller.Execute(ctx, r)
	}
	defer conn.Close()
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
	defer transport.CloseIdleConnections()
	body, err := json.Marshal(r)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/dinit", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	var result Result
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return Result{}, err
	}
	if result.Missing {
		return result, ErrNotLoaded
	}
	if result.Error != "" {
		return result, errors.New(result.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("Dinit API returned %s", resp.Status)
	}
	return result, nil
}
func Query(ctx context.Context, user bool, name string) (Status, error) {
	r, e := Call(ctx, user, Request{Action: "status", Name: name})
	return r.Status, e
}
func QuerySocket(ctx context.Context, path, name string) (Status, error) {
	c, e := Dial(ctx, path)
	if e != nil {
		return Status{}, e
	}
	defer c.Close()
	// Listing allocates no persistent handle, so logger authentication cannot
	// prevent the plane controller from reloading a service concurrently.
	rows, e := c.List(ctx)
	if e != nil {
		return Status{}, e
	}
	for _, s := range rows {
		if s.Name == name {
			return s, nil
		}
	}
	return Status{}, ErrNotLoaded
}
func Deadline() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 35*time.Second)
}
