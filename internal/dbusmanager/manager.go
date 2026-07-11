package dbusmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var ErrNoOwner = errors.New("dbus name has no owner")

type NameOwnerChanged struct {
	Name     string
	OldOwner string
	NewOwner string
}

type Bus interface {
	StartServiceByName(context.Context, string) error
	GetNameOwner(context.Context, string) (string, error)
	WatchNameOwnerChanged(context.Context, string) (<-chan NameOwnerChanged, func(), error)
}

type Options struct {
	BusName      string
	Bus          Bus
	StartBackend func(context.Context) error
}

type Manager struct {
	busName      string
	bus          Bus
	startBackend func(context.Context) error
	startMu      sync.Mutex
}

func New(opts Options) *Manager {
	return &Manager{busName: strings.TrimSpace(opts.BusName), bus: opts.Bus, startBackend: opts.StartBackend}
}

func (m *Manager) Activate(ctx context.Context) error {
	if err := m.validate(); err != nil {
		return err
	}
	if m.startBackend != nil {
		if err := m.startOnce(ctx); err != nil {
			return err
		}
	}
	return m.bus.StartServiceByName(ctx, m.busName)
}

func (m *Manager) WaitForOwner(ctx context.Context, interval time.Duration) (string, error) {
	if err := m.validate(); err != nil {
		return "", err
	}
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	for {
		owner, err := m.bus.GetNameOwner(ctx, m.busName)
		if err == nil && owner != "" {
			return owner, nil
		}
		if err != nil && !errors.Is(err, ErrNoOwner) {
			return "", err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) WatchOwner(ctx context.Context) (<-chan struct{}, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	changes, stop, err := m.bus.WatchNameOwnerChanged(ctx, m.busName)
	if err != nil {
		return nil, err
	}
	lost := make(chan struct{})
	go func() {
		defer close(lost)
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case change, ok := <-changes:
				if !ok {
					return
				}
				if change.Name == m.busName && change.OldOwner != "" && change.NewOwner == "" {
					return
				}
			}
		}
	}()
	return lost, nil
}

func (m *Manager) validate() error {
	if m.busName == "" {
		return fmt.Errorf("bus name is required")
	}
	if m.bus == nil {
		return fmt.Errorf("bus is required")
	}
	return nil
}

func (m *Manager) startOnce(ctx context.Context) error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	owner, err := m.bus.GetNameOwner(ctx, m.busName)
	if err == nil && owner != "" {
		return nil
	}
	if err != nil && !errors.Is(err, ErrNoOwner) {
		return err
	}
	return m.startBackend(ctx)
}

type Child struct {
	Command     string
	StopCommand string
	KillSignal  string
	cmd         *exec.Cmd
	mu          sync.Mutex
}

func (c *Child) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil {
		return nil
	}
	if strings.TrimSpace(c.Command) == "" {
		return fmt.Errorf("command is required")
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", c.Command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	c.cmd = cmd
	go cmd.Wait()
	return nil
}

func (c *Child) Stop(ctx context.Context) error {
	c.mu.Lock()
	cmd := c.cmd
	c.cmd = nil
	c.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if strings.TrimSpace(c.StopCommand) != "" {
		stopCmd := exec.CommandContext(ctx, "/bin/sh", "-c", c.StopCommand)
		stopCmd.Stdout = os.Stdout
		stopCmd.Stderr = os.Stderr
		stopCmd.Env = append(os.Environ(), "MAINPID="+strconv.Itoa(cmd.Process.Pid))
		if err := stopCmd.Run(); err != nil {
			return err
		}
	}
	sig := syscall.SIGTERM
	if parsed := ParseSignal(c.KillSignal); parsed != nil {
		sig = parsed.(syscall.Signal)
	}
	return cmd.Process.Signal(sig)
}

func ParseSignal(raw string) os.Signal {
	switch strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(raw), "SIG")) {
	case "", "TERM":
		return syscall.SIGTERM
	case "INT":
		return syscall.SIGINT
	case "KILL":
		return syscall.SIGKILL
	case "HUP":
		return syscall.SIGHUP
	default:
		return nil
	}
}
