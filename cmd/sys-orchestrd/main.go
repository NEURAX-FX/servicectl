package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"servicectl/internal/visionapi"
)

type daemon struct {
	unit      string
	userMode  bool
	runtime   string
	state     string
	stateFile string
	logger    *log.Logger
}

func main() {
	unit := flag.String("unit", "", "target unit")
	userMode := flag.Bool("user", false, "run in user mode")
	flag.Parse()
	if strings.TrimSpace(*unit) == "" {
		fmt.Fprintln(os.Stderr, "sys-orchestrd requires --unit")
		os.Exit(2)
	}
	logger := log.New(os.Stdout, "sys-orchestrd: ", log.LstdFlags)
	runtime := visionapi.RuntimeDir(*userMode, os.Getenv("XDG_RUNTIME_DIR"))
	d := &daemon{unit: strings.TrimSpace(*unit), userMode: *userMode, runtime: runtime, logger: logger, state: "waiting", stateFile: orchestrdStateFile(strings.TrimSpace(*unit), *userMode, runtime)}
	if err := d.run(); err != nil {
		logger.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func (d *daemon) run() error {
	if err := os.MkdirAll(filepath.Dir(d.stateFile), 0755); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	d.writeState("waiting", "startup")
	if err := d.initialSync(ctx); err != nil {
		return err
	}
	events := make(chan visionapi.EventEnvelope, 32)
	go d.watchEvents(ctx, events)
	for {
		select {
		case <-ctx.Done():
			d.writeState("stopping", "signal")
			_ = d.runServicectl("stop")
			d.publishState("stopping", "signal")
			return nil
		case event := <-events:
			if exitErr := d.handleEvent(event); exitErr != nil {
				return exitErr
			}
		}
	}
}

func (d *daemon) initialSync(ctx context.Context) error {
	for ctx.Err() == nil {
		snapshot, err := d.queryUnit(d.unit)
		if err != nil {
			d.writeState("waiting", "initial-sync")
			d.logger.Printf("initial sync failed: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}
		if snapshot.State == "STARTED" || snapshot.Phase == "ready" || snapshot.ChildState == "running" {
			d.writeState("running", "initial-state")
			d.publishState("running", "initial-state")
			return nil
		}
		d.writeState("starting", "initial-start")
		d.publishState("starting", "initial-start")
		if err := d.runServicectl("start"); err != nil {
			d.writeState("failed", "start-error")
			d.publishState("failed", "start-error")
			d.logger.Printf("initial start failed: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}
		return nil
	}
	return nil
}

func (d *daemon) handleEvent(event visionapi.EventEnvelope) error {
	switch event.Source {
	case visionapi.SourceSysNotifyd:
		failure := strings.TrimSpace(event.Payload["failure"])
		phase := strings.TrimSpace(event.Payload["phase"])
		child := strings.TrimSpace(event.Payload["child_state"])
		if failure != "" {
			d.writeState("failed", failure)
			d.publishState("failed", failure)
			return fmt.Errorf("unit %s failed: %s", d.unit, failure)
		}
		if phase == "ready" || child == "running" {
			d.writeState("running", firstNonEmpty(phase, child))
			d.publishState("running", firstNonEmpty(phase, child))
		}
		if phase == "stopping" || child == "stopping" {
			d.writeState("stopping", firstNonEmpty(phase, child))
			d.publishState("stopping", firstNonEmpty(phase, child))
		}
	case visionapi.SourceServicectl:
		if event.Payload["action"] == "stop" && event.Payload["result"] == "ok" {
			d.writeState("waiting", "stopped")
			d.publishState("waiting", "stopped")
		}
	}
	return nil
}

func (d *daemon) watchEvents(ctx context.Context, events chan<- visionapi.EventEnvelope) {
	for ctx.Err() == nil {
		if err := d.watchEventsOnce(ctx, events); err != nil && ctx.Err() == nil {
			d.logger.Printf("sysvision watch failed: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (d *daemon) watchEventsOnce(ctx context.Context, events chan<- visionapi.EventEnvelope) error {
	path := "/v1/watch?mode=" + url.QueryEscape(visionapi.ModeForUser(d.userMode)) + "&unit=" + url.QueryEscape(d.unit)
	resp, err := d.sysvisionRequest(ctx, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sysvision watch returned %s", resp.Status)
	}
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		var event visionapi.EventEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- event:
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return io.EOF
}

func (d *daemon) runServicectl(action string) error {
	bin := os.Getenv("SERVICECTL_BIN")
	if strings.TrimSpace(bin) == "" {
		bin = "servicectl"
	}
	args := []string{action, d.unit}
	if d.userMode {
		args = append([]string{"--user"}, args...)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (d *daemon) writeState(state string, reason string) {
	d.state = state
	content := strings.Join([]string{
		"unit=" + d.unit,
		"state=" + state,
		"reason=" + reason,
		"updated_at=" + time.Now().UTC().Format(time.RFC3339Nano),
	}, "\n") + "\n"
	_ = os.WriteFile(d.stateFile, []byte(content), 0644)
}

func (d *daemon) publishState(state string, reason string) {
	payload := map[string]string{"state": state, "reason": reason}
	envelope := visionapi.NewEvent(visionapi.ModeForUser(d.userMode), visionapi.SourceSysOrchestrd, visionapi.KindUnitOrchestration, d.unit, payload)
	data, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	addr := &net.UnixAddr{Name: visionapi.SysvisionIngressSocketPath(d.userMode, d.runtime), Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write(data)
}

func (d *daemon) queryUnit(unit string) (visionapi.UnitSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := d.sysvisionRequest(ctx, "/v1/query/unit/"+url.PathEscape(strings.TrimSuffix(unit, ".service")+".service"))
	if err != nil {
		return visionapi.UnitSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return visionapi.UnitSnapshot{}, fmt.Errorf("sysvision query returned %s", resp.Status)
	}
	var snapshot visionapi.UnitSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return visionapi.UnitSnapshot{}, err
	}
	return snapshot, nil
}

func (d *daemon) sysvisionRequest(ctx context.Context, path string) (*http.Response, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", visionapi.SysvisionSocketPath(d.userMode, d.runtime))
	}}
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func orchestrdStateFile(unit string, userMode bool, runtime string) string {
	if value := strings.TrimSpace(os.Getenv("SYS_ORCHESTRD_STATE_FILE")); value != "" {
		return value
	}
	name := strings.TrimSuffix(strings.TrimSpace(unit), ".service") + ".state"
	return filepath.Join(visionapi.RuntimeDir(userMode, runtime), "orchestrd", name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
