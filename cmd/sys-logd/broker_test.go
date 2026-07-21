package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunBrokerClosesActiveConnectionsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	path := filepath.Join(t.TempDir(), "broker.sock")
	done := make(chan error, 1)
	go func() {
		done <- runBroker(ctx, brokerConfig{SocketPath: path, JournalSocketPath: filepath.Join(t.TempDir(), "journal.sock")})
	}()
	var conn *net.UnixConn
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var err error
		conn, err = net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: path, Net: "unixpacket"})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		cancel()
		t.Fatal("broker did not accept connections")
	}
	defer conn.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker shutdown waited for idle connection deadline")
	}
}

func TestBrokerIdleTimeoutIsQuiet(t *testing.T) {
	if !isQuietBrokerReadError(timeoutTestError{}) {
		t.Fatal("timeout was treated as a connection rejection")
	}
}

type timeoutTestError struct{}

func (timeoutTestError) Error() string   { return "i/o timeout" }
func (timeoutTestError) Timeout() bool   { return true }
func (timeoutTestError) Temporary() bool { return true }

func TestUnixPeerCredentials(t *testing.T) {
	client, server := unixPacketPair(t)
	defer client.Close()
	defer server.Close()
	credentials, err := unixPeerCredentials(server)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.PID != os.Getpid() || credentials.UID != uint32(os.Getuid()) || credentials.GID != uint32(os.Getgid()) {
		t.Fatalf("credentials = %#v", credentials)
	}
}

func TestPrepareBrokerSocketRejectsActiveListener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := prepareBrokerSocket(path); err == nil {
		t.Fatal("active broker socket was replaced")
	}
}

func TestBrokerConnectionLimiterEnforcesPerUIDAndTotalLimits(t *testing.T) {
	limiter := newBrokerConnectionLimiter(2, 1)
	if !limiter.Acquire(1000) {
		t.Fatal("first UID 1000 connection rejected")
	}
	if limiter.Acquire(1000) {
		t.Fatal("second UID 1000 connection accepted")
	}
	if !limiter.Acquire(1001) {
		t.Fatal("first UID 1001 connection rejected")
	}
	if limiter.Acquire(1002) {
		t.Fatal("connection accepted above total limit")
	}
	limiter.Release(1000)
	if !limiter.Acquire(1002) {
		t.Fatal("connection rejected after release")
	}
}

func TestBrokerAcknowledgesJournalSuccess(t *testing.T) {
	client, server := unixPacketPair(t)
	defer client.Close()
	defer server.Close()

	peer := peerCredentials{PID: 42, UID: 1000, GID: 1000}
	wantRoute := logRoute{Scope: "user", Unit: "demo.service", Identifier: "servicectl[demo]", PID: 42, UID: 1000, GID: 1000}
	var gotMessage string
	done := make(chan error, 1)
	go func() {
		done <- serveBrokerConnection(server, peer, brokerDependencies{
			resolveRoute: func(got peerCredentials) (logRoute, error) {
				if got != peer {
					t.Errorf("peer = %#v, want %#v", got, peer)
				}
				return wantRoute, nil
			},
			writeJournal: func(route logRoute, priority int, message string) error {
				if route != wantRoute || priority != 6 {
					t.Errorf("journal route/priority = %#v/%d", route, priority)
				}
				gotMessage = message
				return nil
			},
		})
	}()

	writeProtocolPacket(t, client, protocolPacket{Version: protocolVersion, Kind: packetHello})
	writeProtocolPacket(t, client, protocolPacket{Version: protocolVersion, Kind: packetLog, Sequence: 9, Priority: 6, Message: "token"})
	ack := readProtocolPacket(t, client)
	if ack.Kind != packetAck || ack.Sequence != 9 {
		t.Fatalf("ack = %#v", ack)
	}
	if gotMessage != "token" {
		t.Fatalf("journal message = %q", gotMessage)
	}
	_ = client.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker connection did not stop")
	}
}

func TestBrokerReturnsRetryableNackWhenJournalUnavailable(t *testing.T) {
	client, server := unixPacketPair(t)
	defer client.Close()
	defer server.Close()
	go func() {
		_ = serveBrokerConnection(server, peerCredentials{PID: 42}, brokerDependencies{
			resolveRoute: func(peerCredentials) (logRoute, error) {
				return logRoute{Scope: "system", Unit: "demo.service", Identifier: "servicectl[demo]"}, nil
			},
			writeJournal: func(logRoute, int, string) error { return errors.New("journal socket missing") },
		})
	}()

	writeProtocolPacket(t, client, protocolPacket{Version: protocolVersion, Kind: packetHello})
	writeProtocolPacket(t, client, protocolPacket{Version: protocolVersion, Kind: packetLog, Sequence: 2, Priority: 6, Message: "token"})
	nack := readProtocolPacket(t, client)
	if nack.Kind != packetNack || nack.Sequence != 2 || !nack.Retryable || nack.Error == "" {
		t.Fatalf("nack = %#v", nack)
	}
}

func TestBrokerRejectsUnauthenticatedPeer(t *testing.T) {
	client, server := unixPacketPair(t)
	defer client.Close()
	defer server.Close()
	journalCalled := make(chan struct{}, 1)
	go func() {
		_ = serveBrokerConnection(server, peerCredentials{PID: 42}, brokerDependencies{
			resolveRoute: func(peerCredentials) (logRoute, error) { return logRoute{}, errors.New("unauthorized") },
			writeJournal: func(logRoute, int, string) error { journalCalled <- struct{}{}; return nil },
		})
	}()

	writeProtocolPacket(t, client, protocolPacket{Version: protocolVersion, Kind: packetHello})
	nack := readProtocolPacket(t, client)
	if nack.Kind != packetNack || nack.Retryable || nack.Error == "" {
		t.Fatalf("nack = %#v", nack)
	}
	select {
	case <-journalCalled:
		t.Fatal("journal writer called")
	default:
	}
}

func unixPacketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan *net.UnixConn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			errCh <- err
			return
		}
		accepted <- conn
	}()
	client, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case server := <-accepted:
		return client, server
	case err := <-errCh:
		client.Close()
		t.Fatal(err)
	case <-time.After(time.Second):
		client.Close()
		t.Fatal("accept timed out")
	}
	return nil, nil
}

func writeProtocolPacket(t *testing.T, conn *net.UnixConn, packet protocolPacket) {
	t.Helper()
	encoded, err := encodeProtocolPacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(encoded); err != nil {
		t.Fatal(err)
	}
}

func readProtocolPacket(t *testing.T, conn *net.UnixConn) protocolPacket {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, maxProtocolPacketBytes+1)
	n, err := conn.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := decodeProtocolPacket(buffer[:n])
	if err != nil {
		t.Fatal(err)
	}
	return packet
}
