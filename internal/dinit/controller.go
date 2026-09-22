package dinit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Request struct {
	Action        string   `json:"action"`
	Name          string   `json:"name,omitempty"`
	Flags         byte     `json:"flags,omitempty"`
	Signal        int      `json:"signal,omitempty"`
	NoWait        bool     `json:"no_wait,omitempty"`
	IgnoreMissing bool     `json:"ignore_missing,omitempty"`
	Values        []string `json:"values,omitempty"`
}
type Result struct {
	Status   Status   `json:"status"`
	Services []Status `json:"services,omitempty"`
	Error    string   `json:"error,omitempty"`
	Missing  bool     `json:"missing,omitempty"`
}

func (r Request) Validate() error {
	if r.Flags&^byte(3) != 0 {
		return errors.New("unsupported Dinit flags")
	}
	switch r.Action {
	case "list":
		if r.Name != "" || len(r.Values) != 0 {
			return errors.New("list accepts no arguments")
		}
	case "setenv", "unsetenv":
		if len(r.Values) == 0 {
			return errors.New("environment entries required")
		}
		for _, v := range r.Values {
			if strings.ContainsRune(v, 0) || (r.Action == "unsetenv" && strings.Contains(v, "=")) {
				return errors.New("invalid environment entry")
			}
		}
	case "status", "start", "stop", "restart", "wake", "release", "reload", "unload", "unpin", "signal":
		if r.Name == "" || strings.ContainsAny(r.Name, "/\\\x00\n\r") {
			return errors.New("invalid Dinit service name")
		}
		if r.Action == "signal" && (r.Signal < 1 || r.Signal > 64) {
			return errors.New("invalid signal number")
		}
	default:
		return fmt.Errorf("unsupported Dinit action %q", r.Action)
	}
	return nil
}

// Controller owns the only persistent service handles in its plane. Reload and
// unload use that same connection, rather than fighting a separate observer.
type Controller struct {
	Path   string
	mu     sync.Mutex
	client *Client
}

func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
}
func (c *Controller) connection(ctx context.Context) (*Client, error) {
	if c.client != nil {
		select {
		case <-c.client.Done():
			c.client = nil
		default:
		}
	}
	if c.client == nil {
		var err error
		c.client, err = Dial(ctx, c.Path)
		if err != nil {
			return nil, err
		}
	}
	return c.client, nil
}
func (c *Controller) Query(ctx context.Context, name string) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	client, err := c.connection(ctx)
	if err != nil {
		return Status{}, err
	}
	return client.Status(ctx, name, false)
}
func (c *Controller) List(ctx context.Context) ([]Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	client, err := c.connection(ctx)
	if err != nil {
		return nil, err
	}
	return client.List(ctx)
}
func (c *Controller) Execute(ctx context.Context, r Request) (Result, error) {
	if err := r.Validate(); err != nil {
		return Result{}, err
	}
	switch r.Action {
	case "status":
		s, e := c.Query(ctx, r.Name)
		return Result{Status: s}, e
	case "list":
		s, e := c.List(ctx)
		return Result{Services: s}, e
	}
	initialPID := 0
	if r.Action == "restart" {
		if initial, e := c.Query(ctx, r.Name); e == nil {
			initialPID = initial.PID
		}
	}
	c.mu.Lock()
	client, err := c.connection(ctx)
	var s Status
	if err == nil {
		if r.Action == "setenv" || r.Action == "unsetenv" {
			err = client.SetEnv(ctx, r.Values)
		} else {
			s, err = client.Change(ctx, r.Action, r.Name, r.Flags, r.Signal)
		}
	}
	if err != nil && (r.Action == "reload" || r.Action == "unload") {
		if client != nil {
			client.Close()
		}
		c.client = nil
	}
	c.mu.Unlock()
	if err != nil {
		if r.IgnoreMissing && errors.Is(err, ErrNotLoaded) {
			return Result{}, nil
		}
		return Result{}, err
	}
	result := Result{Status: s}
	if r.NoWait {
		return result, nil
	}
	target := Stopped
	switch r.Action {
	case "start", "wake", "restart":
		target = Started
	case "stop":
	default:
		return result, nil
	}
	// No lock is held while waiting. The log broker must be able to authenticate
	// the logger PID while Dinit is still waiting for the workload to become ready.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.State == target && (r.Action != "restart" || s.PID != initialPID || s.PID == 0) {
			return Result{Status: s}, nil
		}
		if target == Started && s.State == Stopped && s.Target != Started {
			return Result{}, fmt.Errorf("%s failed to start: reason=%d stage=%d code=%d status=%d", r.Name, s.StopReason, s.ExecStage, s.ExitCode, s.ExitStatus)
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-ticker.C:
		}
		s, err = c.Query(ctx, r.Name)
		if err != nil {
			return Result{}, err
		}
	}
}

// Observe combines native events with a bounded identity resnapshot: smooth
// recovery can replace a PID without emitting a lifecycle event. Reconnects
// discard old handles and publish a fresh snapshot, never replay old mutations.
func (c *Controller) Observe(ctx context.Context, changed func([]Status, error)) {
	var previous []Status
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		c.mu.Lock()
		client, err := c.connection(qctx)
		var rows []Status
		if err == nil {
			rows, err = client.List(qctx)
		}
		if err == nil {
			for _, row := range rows {
				if _, err = client.Status(qctx, row.Name, false); err != nil {
					break
				}
			}
		}
		c.mu.Unlock()
		cancel()
		if err != nil {
			changed(nil, err)
			c.Close()
			previous = nil
		} else {
			equal := len(previous) == len(rows)
			if equal {
				for i := range rows {
					if rows[i] != previous[i] {
						equal = false
						break
					}
				}
			}
			if !equal || previous == nil {
				changed(rows, nil)
				previous = rows
			}
		}
		var events <-chan Event
		var done <-chan struct{}
		if err == nil {
			events = client.Events()
			done = client.Done()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-done:
		case _, ok := <-events:
			if ok {
				previous = nil
			}
		}
	}
}
