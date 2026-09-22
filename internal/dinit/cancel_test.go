package dinit

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestCancelledRequestClosesStream(t *testing.T) {
	listener, err := net.Listen("unix", t.TempDir()+"/socket")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requestReceived := make(chan struct{})
	peerClosed := make(chan error, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			peerClosed <- e
			return
		}
		defer conn.Close()
		var command [1]byte
		io.ReadFull(conn, command[:])
		reply := []byte{58, 0, 0, 0, 0}
		binary.NativeEndian.PutUint16(reply[1:3], 1)
		binary.NativeEndian.PutUint16(reply[3:5], 5)
		conn.Write(reply)
		header := make([]byte, 3)
		io.ReadFull(conn, header)
		body := make([]byte, int(binary.NativeEndian.Uint16(header[1:])))
		io.ReadFull(conn, body)
		close(requestReceived)
		_, e = conn.Read(command[:])
		peerClosed <- e
	}()
	setup, cancelSetup := context.WithTimeout(context.Background(), time.Second)
	defer cancelSetup()
	client, err := Dial(setup, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := client.Status(ctx, "blocked", false); done <- e }()
	<-requestReceived
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error=%v", err)
		}
	case <-setup.Done():
		t.Fatal("request did not cancel")
	}
	select {
	case err := <-peerClosed:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("stream not closed: %v", err)
		}
	case <-setup.Done():
		t.Fatal("late reply could contaminate stream")
	}
}
