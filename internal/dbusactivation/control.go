package dbusactivation

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
)

func ActivateControl(ctx context.Context, path string) error {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return err
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
