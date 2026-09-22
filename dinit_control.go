package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"servicectl/internal/dinit"
	"servicectl/internal/visionapi"
)

func nativeDinitCall(ctx context.Context, mode string, r dinit.Request) (dinit.Result, error) {
	return dinit.Call(ctx, mode == visionapi.ModeUser, r)
}
func nativeDinitStatus(mode, name string) (dinit.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, e := nativeDinitCall(ctx, mode, dinit.Request{Action: "status", Name: name})
	return r.Status, e
}
func nativeStatusMap(s dinit.Status) map[string]string {
	m := map[string]string{"State": s.State.String(), "Activation": "dependency"}
	if s.Flags&8 != 0 {
		m["Activation"] = "explicitly started"
	}
	if s.PID > 0 {
		m["Process ID"] = strconv.Itoa(s.PID)
	}
	if s.Failed() {
		m["State"] = "STOPPED (terminated; exited - status " + strconv.Itoa(s.ExitStatus) + ")"
	}
	return m
}
func nativeDinitList() ([]dinitListRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := nativeDinitCall(ctx, config.Mode, dinit.Request{Action: "list"})
	if err != nil {
		return nil, err
	}
	rows := make([]dinitListRow, 0, len(r.Services))
	for _, s := range r.Services {
		state := snapshotRuntimeState(s.State.String())
		if s.Failed() {
			state = "failed"
		}
		rows = append(rows, dinitListRow{Name: s.Name, RuntimeState: state, PID: s.PID})
	}
	return rows, nil
}
func nativeDinitCommand(args ...string) (string, int, error) {
	if len(args) == 0 {
		return "", 1, errors.New("Dinit action required")
	}
	r := dinit.Request{Action: args[0]}
	if r.Action == "stop" || r.Action == "restart" {
		r.Flags = 2
	}
	rest := args[1:]
	if r.Action == "setenv" || r.Action == "unsetenv" {
		r.Values = rest
	} else {
		for _, arg := range rest {
			switch arg {
			case "--no-wait":
				r.NoWait = true
			case "--ignore-unstarted":
				r.IgnoreMissing = true
			case "--pin":
				r.Flags |= 1
			case "--force":
				r.Flags &^= 2
			case "--quiet":
			default:
				if strings.HasPrefix(arg, "--") {
					return "", 1, fmt.Errorf("unsupported native Dinit option %s", arg)
				}
				if r.Action == "signal" && r.Signal == 0 {
					sig := unix.SignalNum("SIG" + strings.TrimPrefix(strings.ToUpper(arg), "SIG"))
					if sig == 0 {
						v, _ := strconv.Atoi(arg)
						sig = unix.Signal(v)
					}
					if sig <= 0 {
						return "", 1, fmt.Errorf("invalid signal %s", arg)
					}
					r.Signal = int(sig)
				} else if r.Name == "" {
					r.Name = arg
				} else {
					return "", 1, errors.New("one service name required")
				}
			}
		}
	}
	check := r.Action == "is-started" || r.Action == "is-failed"
	action := r.Action
	if check {
		r.Action = "status"
	}
	ctx, cancel := dinit.Deadline()
	defer cancel()
	result, err := nativeDinitCall(ctx, config.Mode, r)
	if err != nil {
		return "", 1, err
	}
	if check {
		ok := result.Status.State == dinit.Started
		if action == "is-failed" {
			ok = result.Status.Failed()
		}
		if !ok {
			return "", 1, fmt.Errorf("%s is not %s", r.Name, strings.TrimPrefix(action, "is-"))
		}
		return "", 0, nil
	}
	if r.Action == "status" {
		s := result.Status
		return fmt.Sprintf("Service: %s\nState: %s\nProcess ID: %d", s.Name, s.State, s.PID), 0, nil
	}
	if r.Action == "list" {
		var b strings.Builder
		for _, s := range result.Services {
			fmt.Fprintf(&b, "%-10s %s pid=%d\n", s.State, s.Name, s.PID)
		}
		return strings.TrimSpace(b.String()), 0, nil
	}
	return "", 0, nil
}

type dinitPeerKey struct{}

func dinitPeerContext(ctx context.Context, conn net.Conn) context.Context {
	uid := uint32(^uint32(0))
	if c, ok := conn.(*net.UnixConn); ok {
		if raw, e := c.SyscallConn(); e == nil {
			_ = raw.Control(func(fd uintptr) {
				if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
					uid = cred.Uid
				}
			})
		}
	}
	return context.WithValue(ctx, dinitPeerKey{}, uid)
}
func (s *servicectlPlaneServer) serveDinit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	uid, ok := r.Context().Value(dinitPeerKey{}).(uint32)
	if !ok || (uid != 0 && uid != uint32(os.Geteuid())) {
		http.Error(w, "Dinit control access denied", http.StatusForbidden)
		return
	}
	var request dinit.Request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.dinit == nil {
		http.Error(w, "Dinit controller unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	result, err := s.dinit.Execute(ctx, request)
	if err != nil {
		result.Error = err.Error()
		result.Missing = errors.Is(err, dinit.ErrNotLoaded)
		w.WriteHeader(http.StatusBadGateway)
	}
	_ = json.NewEncoder(w).Encode(result)
}
func (s *servicectlPlaneServer) observeDinit(ctx context.Context) {
	s.dinit.Observe(ctx, func(rows []dinit.Status, err error) {
		payload := map[string]string{}
		if err != nil {
			payload["error"] = err.Error()
		} else {
			for _, row := range rows {
				data, e := os.ReadFile(filepath.Join(s.cfg.DinitGenDir, row.Name))
				if e != nil {
					continue
				}
				first, _, _ := strings.Cut(string(data), "\n")
				if !strings.HasPrefix(first, "# Generated from ") {
					continue
				}
				unit := strings.TrimPrefix(first, "# Generated from ")
				if !strings.HasSuffix(unit, ".service") {
					continue
				}
				_ = propertySetRunnerUnitForMode(s.mode, unit, row.State == dinit.Started || row.State == dinit.Starting)
			}
		}
		s.hub.publish(visionapi.NewEvent(s.mode, "dinit", "runtime.changed", "", payload))
	})
}
