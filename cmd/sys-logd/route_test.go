package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidatePeerOwnedPathRejectsSymlinkComponents(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := validatePeerOwnedPath(filepath.Join(link, "file"), uint32(os.Getuid())); err == nil {
		t.Fatal("symlink component was accepted")
	}
}

func TestParseWorkerRoute(t *testing.T) {
	got, err := parseWorkerRoute([]string{
		"/usr/local/bin/sys-logd",
		"--worker",
		"--scope", "system",
		"--unit", "demo.service",
		"--logger-service", "demo-log",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := workerRoute{Scope: "system", Unit: "demo.service", LoggerService: "demo-log"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("route = %#v, want %#v", got, want)
	}
}

func TestParseWorkerRouteRejectsIncompleteOrUnknownArgs(t *testing.T) {
	tests := [][]string{
		{"sys-logd", "--worker", "--scope", "system", "--unit", "demo.service"},
		{"sys-logd", "--worker", "--scope", "system", "--unit", "demo.service", "--logger-service", "demo-log", "--spoof", "x"},
		{"sys-logd", "--worker", "--scope", "other", "--unit", "demo.service", "--logger-service", "demo-log"},
		{"sys-logd", "--worker", "--scope", "system", "--unit", "../demo", "--logger-service", "demo-log"},
	}
	for _, args := range tests {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			if _, err := parseWorkerRoute(args); err == nil {
				t.Fatalf("parseWorkerRoute(%v) succeeded", args)
			}
		})
	}
}

func TestResolvePeerRouteRequiresDinitPIDMatch(t *testing.T) {
	peer := peerCredentials{PID: 42, UID: 1000, GID: 1000}
	deps := testRouteDependencies(peer, []string{
		"sys-logd", "--worker", "--scope", "system", "--unit", "demo.service", "--logger-service", "demo-log",
	})
	deps.dinitPID = func(socketPath, service string) (int, error) {
		if socketPath != systemDinitSocketPath || service != "demo-log" {
			t.Fatalf("dinit query = %q %q", socketPath, service)
		}
		return 41, nil
	}
	if _, err := resolvePeerRoute(peer, deps); err == nil {
		t.Fatal("PID mismatch was accepted")
	}
}

func TestResolvePeerRouteUsesPeerUIDForUserDinit(t *testing.T) {
	peer := peerCredentials{PID: 42, UID: 1000, GID: 1000}
	deps := testRouteDependencies(peer, []string{
		"sys-logd", "--worker", "--scope", "user", "--unit", "demo.service", "--logger-service", "demo-log",
	})
	deps.environment = func(int) (map[string]string, error) {
		return map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, nil
	}
	var queriedSocket string
	deps.dinitPID = func(socketPath, _ string) (int, error) {
		queriedSocket = socketPath
		return peer.PID, nil
	}

	route, err := resolvePeerRoute(peer, deps)
	if err != nil {
		t.Fatal(err)
	}
	if queriedSocket != "/run/user/1000/dinitctl" {
		t.Fatalf("user dinit socket = %q", queriedSocket)
	}
	if route.Scope != "user" || route.UID != peer.UID || route.PID != peer.PID {
		t.Fatalf("route = %#v", route)
	}
}

func testRouteDependencies(peer peerCredentials, args []string) routeDependencies {
	return routeDependencies{
		cmdline: func(int) ([]string, error) { return args, nil },
		environment: func(int) (map[string]string, error) {
			return map[string]string{}, nil
		},
		executable:       func(int) (string, error) { return "/usr/local/bin/sys-logd", nil },
		executableSecure: func(string) error { return nil },
		pathSecure:       func(string, uint32) error { return nil },
		dinitPID:         func(string, string) (int, error) { return peer.PID, nil },
	}
}
