package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if err := maybeExtendStartup(); err != nil {
		return err
	}
	if err := runNotify([]string{"--ready", fmt.Sprintf("--pid=%d", os.Getpid()), "--status=notify-sleeper running"}); err != nil {
		return err
	}
	go watchdogLoop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case <-sigCh:
			return runNotify([]string{"--stopping", "--status=notify-sleeper stopping"})
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func maybeExtendStartup() error {
	extendUsec, _ := strconv.ParseUint(os.Getenv("NOTIFY_EXTEND_START_USEC"), 10, 64)
	if extendUsec == 0 {
		return nil
	}
	intervalUsec, _ := strconv.ParseUint(os.Getenv("NOTIFY_EXTEND_INTERVAL_USEC"), 10, 64)
	if intervalUsec == 0 {
		intervalUsec = 200000
	}
	target := time.Duration(extendUsec) * time.Microsecond
	interval := time.Duration(intervalUsec) * time.Microsecond
	deadline := time.Now().Add(target)
	for time.Now().Before(deadline) {
		if err := runNotify([]string{fmt.Sprintf("EXTEND_TIMEOUT_USEC=%d", extendUsec), "--status=notify-sleeper extending startup"}); err != nil {
			return err
		}
		time.Sleep(interval)
	}
	return nil
}

func watchdogLoop() {
	watchdogUsec, _ := strconv.ParseUint(os.Getenv("NOTIFY_WATCHDOG_USEC"), 10, 64)
	if watchdogUsec == 0 {
		return
	}
	if err := runNotify([]string{fmt.Sprintf("WATCHDOG_USEC=%d", watchdogUsec), "--status=notify-sleeper watchdog armed"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	interval := time.Duration(watchdogUsec) * time.Microsecond / 2
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if err := runNotify([]string{"WATCHDOG=1"}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
	}
}

func runNotify(args []string) error {
	if os.Getenv("NOTIFY_SOCKET") == "" {
		return fmt.Errorf("NOTIFY_SOCKET is required")
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
