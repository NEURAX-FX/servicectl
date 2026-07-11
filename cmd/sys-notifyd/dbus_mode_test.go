package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDbusModeIsLazyActivationService(t *testing.T) {
	srv, err := newServer(config{busName: "org.freedesktop.locale1", stateFile: filepath.Join(t.TempDir(), "state")}, log.New(os.Stdout, "", 0))
	if err != nil {
		t.Fatalf("newServer returned error: %v", err)
	}
	defer srv.cleanup()

	if !srv.isLazyDBusService() {
		t.Fatal("dbus mode should be lazy activation")
	}
	srv.mu.Lock()
	srv.resetForIdleLocked()
	srv.mu.Unlock()
	if err := srv.handleChildExit(nil); err != nil {
		t.Fatalf("handleChildExit returned %v, want nil for dbus idle exit", err)
	}
	if srv.phase != "ready" {
		t.Fatalf("phase = %q, want ready", srv.phase)
	}
	if srv.status != "listening" {
		t.Fatalf("status = %q, want listening", srv.status)
	}
	if srv.childState != "idle" {
		t.Fatalf("childState = %q, want idle", srv.childState)
	}
}

func TestControlSocketQueuesStartForRootPeer(t *testing.T) {
	tempDir := t.TempDir()
	controlPath := filepath.Join(tempDir, "control.sock")
	srv, err := newServer(config{
		serviceName: "systemd-localed",
		serviceType: "notify",
		command:     "/bin/true",
		busName:     "org.freedesktop.locale1",
		controlPath: controlPath,
	}, log.New(os.Stdout, "", 0))
	if err != nil {
		t.Fatalf("newServer returned error: %v", err)
	}
	defer srv.cleanup()

	if err := srv.armControlActivation(); err != nil {
		t.Fatalf("armControlActivation returned error: %v", err)
	}
	info, err := os.Stat(controlPath)
	if err != nil {
		t.Fatalf("control socket was not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("control socket mode = %o, want 600", got)
	}
	if err := sendControlActivation(context.Background(), controlPath); err != nil {
		t.Fatalf("sendControlActivation returned error: %v", err)
	}

	select {
	case reason := <-srv.startReq:
		if reason != "dbus activation" {
			t.Fatalf("start reason = %q, want dbus activation", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("control activation did not queue backend start")
	}
}

func TestControlPeerAuthorizationRequiresRoot(t *testing.T) {
	if err := authorizeControlPeer(0); err != nil {
		t.Fatalf("root peer rejected: %v", err)
	}
	if err := authorizeControlPeer(1000); err == nil {
		t.Fatal("non-root peer unexpectedly accepted")
	}
}

func TestDbusTriggerSocketQueuesStart(t *testing.T) {
	tempDir := t.TempDir()
	triggerPath := filepath.Join(tempDir, "dbus.trigger")
	srv, err := newServer(config{
		serviceName:     "systemd-localed",
		serviceType:     "notify",
		command:         "/bin/true",
		busName:         "org.freedesktop.locale1",
		dbusTriggerPath: triggerPath,
	}, log.New(os.Stdout, "", 0))
	if err != nil {
		t.Fatalf("newServer returned error: %v", err)
	}
	defer srv.cleanup()

	if err := srv.armDBusActivation(); err != nil {
		t.Fatalf("armDBusActivation returned error: %v", err)
	}
	if _, err := os.Stat(triggerPath); err != nil {
		t.Fatalf("trigger socket was not created: %v", err)
	}
	if err := sendDBusActivationTrigger(triggerPath); err != nil {
		t.Fatalf("sendDBusActivationTrigger returned error: %v", err)
	}

	select {
	case reason := <-srv.startReq:
		if reason != "dbus activation" {
			t.Fatalf("start reason = %q, want dbus activation", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("dbus activation did not queue backend start")
	}
}
