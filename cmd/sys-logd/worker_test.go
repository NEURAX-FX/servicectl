package main

import (
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestBrokerSinkSendsHelloAndWaitsForAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		hello := readProtocolPacket(t, conn)
		if hello.Kind != packetHello {
			serverErr <- errors.New("missing hello")
			return
		}
		logPacket := readProtocolPacket(t, conn)
		if logPacket.Kind != packetLog || logPacket.Message != "token" {
			serverErr <- errors.New("invalid log packet")
			return
		}
		encoded, err := encodeProtocolPacket(protocolPacket{Version: protocolVersion, Kind: packetAck, Sequence: logPacket.Sequence})
		if err == nil {
			_, err = conn.Write(encoded)
		}
		serverErr <- err
	}()

	sink := newBrokerSink(path, time.Second)
	defer sink.Close()
	if err := sink.WriteMessage("token"); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestBrokerSinkReturnsNackAndReconnects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = readProtocolPacket(t, conn)
		logPacket := readProtocolPacket(t, conn)
		encoded, _ := encodeProtocolPacket(protocolPacket{Version: protocolVersion, Kind: packetNack, Sequence: logPacket.Sequence, Retryable: true, Error: "journal unavailable"})
		_, _ = conn.Write(encoded)
	}()

	sink := newBrokerSink(path, time.Second)
	err = sink.WriteMessage("token")
	var nack *brokerNackError
	if !errors.As(err, &nack) || !nack.Retryable {
		t.Fatalf("error = %#v", err)
	}
	if sink.conn != nil {
		t.Fatal("broker connection retained after NACK")
	}
}
