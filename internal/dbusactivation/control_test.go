package dbusactivation

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestActivateControlWaitsForSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	serverDone := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		listener, err := net.Listen("unix", path)
		if err != nil {
			serverDone <- err
			return
		}
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		request, err := bufio.NewReader(conn).ReadString('\n')
		if err == nil && request != "activate\n" {
			t.Errorf("request = %q", request)
		}
		if err == nil {
			_, err = conn.Write([]byte("ok\n"))
		}
		serverDone <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ActivateControl(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
