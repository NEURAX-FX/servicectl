package dinit

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// Fragmented replies and unsolicited events share the same stream. Losing frame
// boundaries here can turn a state event into a successful command response.
func TestClientDemultiplexesFragmentedEventBeforeReply(t *testing.T) {
	listener, err := net.Listen("unix", t.TempDir()+"/control.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer c.Close()
		var q [1]byte
		_, e = io.ReadFull(c, q[:])
		if e != nil {
			done <- e
			return
		}
		version := []byte{58, 0, 0, 0, 0}
		binary.NativeEndian.PutUint16(version[1:3], 1)
		binary.NativeEndian.PutUint16(version[3:5], 6)
		for _, b := range version {
			if _, e = c.Write([]byte{b}); e != nil {
				done <- e
				return
			}
		}
		header := make([]byte, 3)
		if _, e = io.ReadFull(c, header); e != nil {
			done <- e
			return
		}
		name := make([]byte, int(binary.NativeEndian.Uint16(header[1:])))
		_, e = io.ReadFull(c, name)
		if e != nil {
			done <- e
			return
		}
		record := []byte{59, 0, 7, 0, 0, 0, 0}
		binary.NativeEndian.PutUint32(record[2:6], 7)
		c.Write(record)
		request := make([]byte, 5)
		if _, e = io.ReadFull(c, request); e != nil {
			done <- e
			return
		}
		event := make([]byte, 21)
		event[0] = 101
		event[1] = 21
		binary.NativeEndian.PutUint32(event[2:6], 7)
		event[6] = 0
		event[7] = 2
		event[8] = 2
		event[9] = 16
		binary.NativeEndian.PutUint32(event[13:17], 4242)
		status := append([]byte{70, 0}, event[7:]...)
		for _, b := range append(event, status...) {
			if _, e = c.Write([]byte{b}); e != nil {
				done <- e
				return
			}
		}
		done <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := Dial(ctx, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	status, err := client.Status(ctx, "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != Started || status.PID != 4242 {
		t.Fatalf("status=%+v", status)
	}
	select {
	case e := <-client.Events():
		if e.Handle != 7 || e.Status.PID != 4242 {
			t.Fatalf("event=%+v", e)
		}
	case <-ctx.Done():
		t.Fatal("event not delivered")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
