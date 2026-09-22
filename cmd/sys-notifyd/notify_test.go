package main

import (
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// This subprocess keeps its PID while dropping credentials, like chronyd.
func TestNotifySenderProcess(t *testing.T) {
	if os.Getenv("SERVICECTL_NOTIFY_TEST_CHILD") != "1" {
		return
	}
	if os.Getenv("SERVICECTL_NOTIFY_TEST_DROP") == "1" {
		if err := syscall.Setgroups(nil); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Setgid(65534); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Setuid(65534); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: os.Getenv("NOTIFY_SOCKET"), Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := os.Getenv("SERVICECTL_NOTIFY_TEST_PAYLOAD")
	if payload == "" {
		payload = "READY=1"
	}
	if os.Getenv("SERVICECTL_NOTIFY_TEST_MAINPID") == "1" {
		payload = "MAINPID=" + strconv.Itoa(os.Getpid()) + "\n" + payload
	}
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	// Stay alive until the parent verifies readiness and terminates us.
	for {
		time.Sleep(time.Hour)
	}
}

func TestNotifyAcceptsReadinessAfterPrivilegeDrop(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise setuid after exec")
	}
	// t.TempDir's parent is private; the dropped child must traverse the socket directory.
	dir, err := os.MkdirTemp("", "notify-drop-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(dir, "sender")
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVICECTL_NOTIFY_TEST_CHILD", "1")
	t.Setenv("SERVICECTL_NOTIFY_TEST_DROP", "1")
	srv, err := newServer(config{
		serviceName: "notify-drop", serviceType: "notify",
		notify: true, notifyPath: filepath.Join(dir, "notify.sock"),
		command:  shellEscape(copyPath) + " -test.run=^TestNotifySenderProcess$",
		startNow: true, readyTimeout: 3 * time.Second,
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.cleanup()
	if err := srv.startChild(); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	child, done := srv.cmd, srv.cmdDone
	srv.mu.Unlock()
	defer func() { _ = child.Process.Kill(); <-done }()
	if err := srv.waitUntilReady(); err != nil {
		t.Fatalf("unprivileged backend could not signal readiness: %v", err)
	}
	if srv.currentPhase() != "ready" {
		t.Fatal("backend notification did not transition service to ready")
	}
}

func TestNotifyRejectsUnrelatedSender(t *testing.T) {
	srv, err := newServer(config{
		serviceType: "notify", notify: true,
		notifyPath: filepath.Join(t.TempDir(), "notify.sock"),
		command:    "/bin/sleep 30", readyTimeout: 3 * time.Second,
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.cleanup()
	if err := srv.startChild(); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	child, done, ready := srv.cmd, srv.cmdDone, srv.childReady
	srv.mu.Unlock()
	defer func() { _ = child.Process.Kill(); <-done }()
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: srv.cfg.notifyPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Same-UID, unrelated processes must not be able to mark somebody else's service ready.
	if _, err := conn.Write([]byte("READY=1")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
		t.Fatal("unrelated process forged service readiness")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestNotifyAcceptsBackendHelper(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVICECTL_NOTIFY_TEST_CHILD", "1")
	t.Setenv("SERVICECTL_NOTIFY_TEST_MAINPID", "1")
	// Keep the helper alive until readiness is observed; notification attribution
	// cannot rely on /proc ancestry after a helper has already exited.
	srv, err := newServer(config{
		serviceType: "notify", notify: true,
		notifyPath: filepath.Join(t.TempDir(), "notify.sock"),
		command:    "/bin/sh -c " + shellEscape(shellEscape(executable)+" -test.run=^TestNotifySenderProcess$ & wait"),
		startNow:   true, readyTimeout: 3 * time.Second,
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.cleanup()
	if err := srv.startChild(); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	child, done := srv.cmd, srv.cmdDone
	srv.mu.Unlock()
	defer func() {
		srv.mu.Lock()
		pid := srv.mainPID
		srv.mu.Unlock()
		if pid > 0 && pid != child.Process.Pid {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		} else {
			_ = child.Process.Kill()
		}
		<-done
	}()
	if err := srv.waitUntilReady(); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyRejectsUnrelatedMainPID(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVICECTL_NOTIFY_TEST_CHILD", "1")
	t.Setenv("SERVICECTL_NOTIFY_TEST_PAYLOAD", "MAINPID="+strconv.Itoa(os.Getpid())+"\nREADY=1")
	srv, err := newServer(config{
		serviceType: "notify", notify: true,
		notifyPath: filepath.Join(t.TempDir(), "notify.sock"),
		command:    shellEscape(executable) + " -test.run=^TestNotifySenderProcess$",
		startNow:   true, readyTimeout: 3 * time.Second,
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.cleanup()
	if err := srv.startChild(); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	child, done := srv.cmd, srv.cmdDone
	srv.mu.Unlock()
	defer func() { _ = child.Process.Kill(); <-done }()
	if err := srv.waitUntilReady(); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	mainPID := srv.mainPID
	srv.mu.Unlock()
	if mainPID != child.Process.Pid {
		t.Fatalf("backend redirected supervisor to unrelated PID %d", mainPID)
	}
}
