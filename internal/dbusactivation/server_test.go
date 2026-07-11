package dbusactivation

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeActivator struct {
	mu       sync.Mutex
	requests []ActivateRequest
	result   ActivationResult
	wait     chan struct{}
}

func (a *fakeActivator) Activate(ctx context.Context, request ActivateRequest) ActivationResult {
	a.mu.Lock()
	a.requests = append(a.requests, request)
	a.mu.Unlock()
	if a.wait != nil {
		select {
		case <-a.wait:
		case <-ctx.Done():
			return ActivationResult{Code: ResultFailed, Detail: ctx.Err().Error()}
		}
	}
	return a.result
}

func TestServerHelloEnvironmentActivationAndPing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	activator := &fakeActivator{result: ActivationResult{Code: ResultSuccess}}
	environments := &EnvironmentStore{}
	server := NewServer(ServerOptions{
		Path:        path,
		Activator:   activator,
		Environment: environments,
		Authorize: func(frontend Frontend, peer Peer) error {
			if frontend != FrontendDaemonHelper || peer.UID != uint32(os.Getuid()) || peer.PID <= 0 || peer.Executable == "" {
				return errors.New("unexpected peer")
			}
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ctx) }()
	waitForSocket(t, path)

	conn := dialPacket(t, path)
	defer conn.Close()
	writeServerPacket(t, conn, Packet{Type: MessageHello, RequestID: 1, Payload: []byte{byte(FrontendDaemonHelper)}})
	hello := readServerPacket(t, conn)
	if hello.Type != MessageHello || hello.RequestID != 1 {
		t.Fatalf("hello response = %#v", hello)
	}

	envPayload, err := EncodeSetEnvironment(SetEnvironmentRequest{Frontend: FrontendDaemonHelper, Values: map[string]string{
		"LANG":                     "C.UTF-8",
		"LD_PRELOAD":               "/tmp/evil.so",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/tmp/session",
	}})
	if err != nil {
		t.Fatal(err)
	}
	writeServerPacket(t, conn, Packet{Type: MessageSetEnvironment, RequestID: 2, Payload: envPayload})
	envResult := readActivationResultPacket(t, conn)
	if envResult.Code != ResultSuccess {
		t.Fatalf("environment result = %#v", envResult)
	}
	if generation, values := environments.Snapshot(FrontendDaemonHelper); generation != 1 || len(values) != 1 || values[0] != "LANG=C.UTF-8" {
		t.Fatalf("environment snapshot = %d %#v", generation, values)
	}
	environments.Replace(FrontendDaemonHelper, map[string]string{"LANG": "overwritten"})

	activatePayload, err := EncodeActivate(ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
	if err != nil {
		t.Fatal(err)
	}
	writeServerPacket(t, conn, Packet{Type: MessageActivate, RequestID: 3, Payload: activatePayload})
	activation := readActivationResultPacket(t, conn)
	if activation.Code != ResultSuccess {
		t.Fatalf("activation result = %#v", activation)
	}

	writeServerPacket(t, conn, Packet{Type: MessagePing, RequestID: 4})
	ping := readServerPacket(t, conn)
	if ping.Type != MessagePing || ping.RequestID != 4 {
		t.Fatalf("ping response = %#v", ping)
	}

	activator.mu.Lock()
	defer activator.mu.Unlock()
	if len(activator.requests) != 1 || activator.requests[0].BusName != "org.example.Service" || !reflect.DeepEqual(activator.requests[0].environment, []string{"LANG=C.UTF-8"}) {
		t.Fatalf("requests = %#v", activator.requests)
	}
}

func TestServerRejectsUnauthorizedPeer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server := NewServer(ServerOptions{
		Path:      path,
		Activator: &fakeActivator{},
		Authorize: func(Frontend, Peer) error { return errors.New("denied") },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	waitForSocket(t, path)
	conn := dialPacket(t, path)
	defer conn.Close()
	writeServerPacket(t, conn, Packet{Type: MessageHello, RequestID: 1, Payload: []byte{byte(FrontendDaemonHelper)}})
	if _, err := readPacketFromConn(conn); err == nil {
		t.Fatal("unauthorized connection remained usable")
	}
}

func TestServerRequiresHelloBeforeRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server := NewServer(ServerOptions{Path: path, Activator: &fakeActivator{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	waitForSocket(t, path)
	conn := dialPacket(t, path)
	defer conn.Close()
	writeServerPacket(t, conn, Packet{Type: MessagePing, RequestID: 1})
	if _, err := readPacketFromConn(conn); err == nil {
		t.Fatal("connection accepted request before hello")
	}
}

func TestServerCancellationClosesHeldConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server := NewServer(ServerOptions{Path: path, Activator: &fakeActivator{}})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ctx) }()
	waitForSocket(t, path)
	conn := dialPacket(t, path)
	defer conn.Close()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop while a client held a connection")
	}
}

func TestServerCancelStopsActivationWaiter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	activator := &fakeActivator{wait: make(chan struct{})}
	server := NewServer(ServerOptions{Path: path, Activator: activator})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	waitForSocket(t, path)
	conn := dialPacket(t, path)
	defer conn.Close()
	writeServerPacket(t, conn, Packet{Type: MessageHello, RequestID: 1, Payload: []byte{byte(FrontendDaemonHelper)}})
	_ = readServerPacket(t, conn)
	payload, _ := EncodeActivate(ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
	writeServerPacket(t, conn, Packet{Type: MessageActivate, RequestID: 42, Payload: payload})
	writeServerPacket(t, conn, Packet{Type: MessageCancel, RequestID: 42})
	result := readActivationResultPacket(t, conn)
	if result.Code != ResultFailed || result.Detail != context.Canceled.Error() {
		t.Fatalf("cancel result = %#v", result)
	}
}

func TestServerReloadAndStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	reloaded := false
	server := NewServer(ServerOptions{
		Path:      path,
		Activator: &fakeActivator{},
		Authorize: func(frontend Frontend, _ Peer) error {
			if frontend != FrontendAdmin {
				return errors.New("admin required")
			}
			return nil
		},
		Reload: func() error {
			reloaded = true
			return nil
		},
		Status: func() []byte { return []byte(`{"healthy":true}`) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	waitForSocket(t, path)
	conn := dialPacket(t, path)
	defer conn.Close()
	writeServerPacket(t, conn, Packet{Type: MessageHello, RequestID: 1, Payload: []byte{byte(FrontendAdmin)}})
	_ = readServerPacket(t, conn)
	writeServerPacket(t, conn, Packet{Type: MessageReload, RequestID: 2})
	if result := readActivationResultPacket(t, conn); result.Code != ResultSuccess || !reloaded {
		t.Fatalf("reload result=%#v reloaded=%v", result, reloaded)
	}
	writeServerPacket(t, conn, Packet{Type: MessageStatus, RequestID: 3})
	status := readServerPacket(t, conn)
	if status.Type != MessageStatus || string(status.Payload) != `{"healthy":true}` {
		t.Fatalf("status = %#v", status)
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket %s was not created", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func dialPacket(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	conn, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	return conn
}

func writeServerPacket(t *testing.T, conn *net.UnixConn, packet Packet) {
	t.Helper()
	data, err := EncodePacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatal(err)
	}
}

func readServerPacket(t *testing.T, conn *net.UnixConn) Packet {
	t.Helper()
	packet, err := readPacketFromConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	return packet
}

func readPacketFromConn(conn *net.UnixConn) (Packet, error) {
	buffer := make([]byte, HeaderSize+int(MaxPayload)+1)
	n, err := conn.Read(buffer)
	if err != nil {
		return Packet{}, err
	}
	return DecodePacket(buffer[:n])
}

func readActivationResultPacket(t *testing.T, conn *net.UnixConn) ActivationResult {
	t.Helper()
	packet := readServerPacket(t, conn)
	if packet.Type != MessageActivationResult {
		t.Fatalf("packet type = %d, want activation result", packet.Type)
	}
	result, err := DecodeActivationResult(packet.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
