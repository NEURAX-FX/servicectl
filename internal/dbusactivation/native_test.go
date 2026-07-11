package dbusactivation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeCommandUsesDirectArgvEnvironmentAndCredential(t *testing.T) {
	lookup := func(name string) (*user.User, error) {
		if name != "example" {
			return nil, errors.New("unexpected user")
		}
		return &user.User{Username: "example", Uid: "123", Gid: "456"}, nil
	}
	groups := func(*user.User) ([]int, error) { return []int{456, 789}, nil }
	starter := NewNativeStarter(NativeOptions{
		LookupUser: lookup,
		GroupIDs:   groups,
		BusAddress: "unix:path=/run/dbus/system_bus_socket",
	})

	cmd, err := starter.command(context.Background(), NativeRoute{
		Argv: []string{"/usr/libexec/example", "--flag", "value with space"},
		User: "example",
	}, []string{"LANG=C.UTF-8"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/usr/libexec/example" || !reflect.DeepEqual(cmd.Args, []string{"/usr/libexec/example", "--flag", "value with space"}) {
		t.Fatalf("command = %q %#v", cmd.Path, cmd.Args)
	}
	if cmd.Dir != "/" {
		t.Fatalf("working directory = %q, want /", cmd.Dir)
	}
	if !containsEnv(cmd.Env, "LANG=C.UTF-8") || !containsEnv(cmd.Env, "DBUS_STARTER_ADDRESS=unix:path=/run/dbus/system_bus_socket") || !containsEnv(cmd.Env, "DBUS_STARTER_BUS_TYPE=system") {
		t.Fatalf("environment = %#v", cmd.Env)
	}
	credential := cmd.SysProcAttr.Credential
	if credential == nil || credential.Uid != 123 || credential.Gid != 456 || !reflect.DeepEqual(credential.Groups, []uint32{456, 789}) {
		t.Fatalf("credential = %#v", credential)
	}
}

func TestNativeCommandRejectsMissingUserAndEmptyArgv(t *testing.T) {
	starter := NewNativeStarter(NativeOptions{LookupUser: func(string) (*user.User, error) {
		return nil, user.UnknownUserError("missing")
	}})
	if _, err := starter.command(context.Background(), NativeRoute{Argv: []string{"/bin/true"}, User: "missing"}, nil); err == nil {
		t.Fatal("missing user unexpectedly succeeded")
	}
	if _, err := starter.command(context.Background(), NativeRoute{}, nil); err == nil {
		t.Fatal("empty argv unexpectedly succeeded")
	}
	if _, err := starter.command(context.Background(), NativeRoute{Argv: []string{"relative-command"}}, nil); err == nil {
		t.Fatal("relative executable unexpectedly succeeded")
	}
}

func TestNativeStarterReportsProcessExit(t *testing.T) {
	starter := NewNativeStarter(NativeOptions{})
	started, err := starter.Start(context.Background(), Route{Kind: RouteNative, Native: NativeRoute{
		Argv: []string{os.Args[0], "-test.run=TestNativeChildProcess", "--", "7"},
	}}, []string{"GO_WANT_NATIVE_CHILD=1"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-started.Exit:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
			t.Fatalf("exit error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child did not exit")
	}
}

func TestNativeStarterDoesNotKillServiceWhenActivationContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	starter := NewNativeStarter(NativeOptions{})
	started, err := starter.Start(ctx, Route{Kind: RouteNative, Native: NativeRoute{
		Argv: []string{"/bin/sh", "-c", "sleep 0.15"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-started.Exit:
		if err != nil {
			t.Fatalf("service was killed with activation context: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("service did not exit normally")
	}
}

func TestNativeChildProcess(t *testing.T) {
	if os.Getenv("GO_WANT_NATIVE_CHILD") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(99)
	}
	code, _ := strconv.Atoi(os.Args[separator+1])
	os.Exit(code)
}

func TestResultFromChildSignal(t *testing.T) {
	err := &exec.ExitError{ProcessState: fakeSignaledProcessState(t)}
	result := resultFromChildExit(err)
	if result.Code != ResultChildSignaled {
		t.Fatalf("result = %#v", result)
	}
}

func fakeSignaledProcessState(t *testing.T) *os.ProcessState {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "kill -TERM $$")
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		t.Fatalf("process was not signaled: %v", status)
	}
	return exitErr.ProcessState
}

func containsEnv(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestNativeEnvironmentReplacesStarterVariables(t *testing.T) {
	starter := NewNativeStarter(NativeOptions{BusAddress: "unix:path=/new"})
	cmd, err := starter.command(context.Background(), NativeRoute{Argv: []string{"/bin/true"}}, []string{
		"DBUS_STARTER_ADDRESS=unix:path=/old",
		"DBUS_STARTER_BUS_TYPE=session",
		"LANG=C",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Env, "\n")
	if strings.Contains(joined, "unix:path=/old") || strings.Contains(joined, "DBUS_STARTER_BUS_TYPE=session") {
		t.Fatalf("old starter environment remained: %#v", cmd.Env)
	}
}
