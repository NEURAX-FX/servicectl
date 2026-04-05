package visionapi

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func PrepareUnixStreamListener(path string) error {
	return prepareUnixListener(path, "unix")
}

func PrepareUnixDatagramListener(path string) error {
	return prepareUnixListener(path, "unixgram")
}

func prepareUnixListener(path string, network string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create unix socket directory: %w", err)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect unix socket path: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("existing path is not a socket: %s", path)
	}
	active, err := unixSocketActive(path, network)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("unix socket already active: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale unix socket: %w", err)
	}
	return nil
}

func unixSocketActive(path string, network string) (bool, error) {
	switch network {
	case "unix":
		conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true, nil
		}
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return false, nil
		}
		return false, fmt.Errorf("probe unix socket %s: %w", path, err)
	case "unixgram":
		conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
		if err == nil {
			_ = conn.Close()
			return true, nil
		}
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return false, nil
		}
		return false, fmt.Errorf("probe unix datagram socket %s: %w", path, err)
	default:
		return false, fmt.Errorf("unsupported unix socket network %q", network)
	}
}
