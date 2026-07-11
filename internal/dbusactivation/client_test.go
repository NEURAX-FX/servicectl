package dbusactivation

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestClientPingStatusAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server := NewServer(ServerOptions{
		Path:      path,
		Activator: &fakeActivator{},
		Authorize: func(frontend Frontend, _ Peer) error {
			if frontend != FrontendAdmin {
				return errors.New("admin required")
			}
			return nil
		},
		Reload: func() error { return nil },
		Status: func() []byte { return []byte(`{"healthy":true}`) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	waitForSocket(t, path)

	client, err := DialClient(context.Background(), path, FrontendAdmin)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background())
	if err != nil || string(status) != `{"healthy":true}` {
		t.Fatalf("status = %q, %v", status, err)
	}
	if err := client.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientActivateAndEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	activator := &fakeActivator{result: ActivationResult{Code: ResultSuccess}}
	environments := &EnvironmentStore{}
	server := NewServer(ServerOptions{Path: path, Activator: activator, Environment: environments})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	waitForSocket(t, path)

	client, err := DialClient(context.Background(), path, FrontendDaemonHelper)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SetEnvironment(context.Background(), map[string]string{"LANG": "C"}); err != nil {
		t.Fatal(err)
	}
	result, err := client.Activate(context.Background(), "org.example.Service")
	if err != nil || result.Code != ResultSuccess {
		t.Fatalf("activate = %#v, %v", result, err)
	}
	if generation, values := environments.Snapshot(FrontendDaemonHelper); generation != 1 || len(values) != 1 || values[0] != "LANG=C" {
		t.Fatalf("environment = %d %#v", generation, values)
	}
}

func TestClientHonorsContextDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.AcceptUnix()
		if err == nil {
			defer conn.Close()
			time.Sleep(time.Second)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := DialClient(ctx, path, FrontendAdmin); err == nil {
		t.Fatal("DialClient unexpectedly succeeded")
	}
}
