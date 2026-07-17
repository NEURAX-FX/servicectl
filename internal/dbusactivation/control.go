package dbusactivation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

func ActivateControl(ctx context.Context, path string) error {
	dialer := net.Dialer{}
	var conn net.Conn
	for {
		var err error
		conn, err = dialer.DialContext(ctx, "unix", path)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ECONNREFUSED) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := io.WriteString(conn, "activate\n"); err != nil {
		return err
	}
	buffer := make([]byte, 16)
	n, err := conn.Read(buffer)
	if err != nil {
		return err
	}
	response := string(buffer[:n])
	if response != "ok\n" {
		return fmt.Errorf("activation control rejected request: %s", strings.TrimSpace(response))
	}
	return nil
}
