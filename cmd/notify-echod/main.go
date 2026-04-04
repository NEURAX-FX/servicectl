package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	listenFDs, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil || listenFDs < 1 {
		return fmt.Errorf("LISTEN_FDS must be at least 1")
	}

	listenerFile := os.NewFile(uintptr(3), "listener")
	if listenerFile == nil {
		return fmt.Errorf("listener fd 3 unavailable")
	}
	defer listenerFile.Close()

	listener, err := net.FileListener(listenerFile)
	if err != nil {
		return fmt.Errorf("wrap inherited listener: %w", err)
	}
	defer listener.Close()

	if err := notifyReady("notify-echod accepting connections"); err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		_ = notifyStopping("notify-echod shutting down")
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				return nil
			}
			return fmt.Errorf("accept connection: %w", err)
		}

		response := "hello from notify-echod"
		if fdNames := os.Getenv("LISTEN_FDNAMES"); fdNames != "" {
			response += " fdnames=" + fdNames
		}
		if _, err := conn.Write([]byte(response + "\n")); err != nil {
			_ = conn.Close()
			return fmt.Errorf("write response: %w", err)
		}
		_ = conn.Close()
		if os.Getenv("NOTIFY_ECHOD_EXIT_AFTER_ACCEPT") == "1" {
			return nil
		}
	}
}

func notifyReady(status string) error {
	return runNotify([]string{"--ready", fmt.Sprintf("--pid=%d", os.Getpid()), fmt.Sprintf("--status=%s", status)})
}

func notifyStopping(status string) error {
	return runNotify([]string{"--stopping", fmt.Sprintf("--status=%s", status)})
}

func runNotify(args []string) error {
	if os.Getenv("NOTIFY_SOCKET") == "" {
		return nil
	}
	cmd := exec.Command("systemd-notify", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run systemd-notify: %w", err)
	}
	return nil
}
