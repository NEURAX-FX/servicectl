package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"servicectl/internal/cgrouptrack"
)

func TestParseCgroupAttach(t *testing.T) {
	request, err := parseCgroupCommand([]string{"attach", "demo.service", "42"}, true, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if request.Operation != cgrouptrack.OpAttach || request.Mode != cgrouptrack.ModeUser || request.UID != 1000 || request.Unit != "demo.service" || request.PID != 42 {
		t.Fatalf("request = %#v", request)
	}
}

func TestParseCgroupCommands(t *testing.T) {
	tests := []struct {
		args     []string
		userMode bool
		uid      uint32
		want     cgrouptrack.Request
	}{
		{[]string{"status"}, false, 0, cgrouptrack.Request{Operation: cgrouptrack.OpStatus}},
		{[]string{"status"}, true, 1000, cgrouptrack.Request{Operation: cgrouptrack.OpStatus, Mode: cgrouptrack.ModeUser, UID: 1000}},
		{[]string{"status"}, true, 0, cgrouptrack.Request{Operation: cgrouptrack.OpStatus, Mode: cgrouptrack.ModeUser, UID: 0}},
		{[]string{"list"}, false, 0, cgrouptrack.Request{Operation: cgrouptrack.OpListUnits, Mode: cgrouptrack.ModeSystem}},
		{[]string{"list"}, true, 1000, cgrouptrack.Request{Operation: cgrouptrack.OpListUnits, Mode: cgrouptrack.ModeUser, UID: 1000}},
		{[]string{"inspect", "demo"}, false, 0, cgrouptrack.Request{Operation: cgrouptrack.OpGetUnit, Mode: cgrouptrack.ModeSystem, Unit: "demo.service"}},
		{[]string{"pids", "demo.service"}, true, 1000, cgrouptrack.Request{Operation: cgrouptrack.OpListPIDs, Mode: cgrouptrack.ModeUser, UID: 1000, Unit: "demo.service"}},
	}
	for _, test := range tests {
		got, err := parseCgroupCommand(test.args, test.userMode, test.uid)
		if err != nil {
			t.Fatalf("parse %#v: %v", test.args, err)
		}
		if got != test.want {
			t.Fatalf("parse %#v = %#v, want %#v", test.args, got, test.want)
		}
	}
}

func TestCgroupCommandRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"status", "extra"},
		{"list", "extra"},
		{"inspect"},
		{"inspect", "a", "b"},
		{"pids"},
		{"attach", "demo.service"},
		{"attach", "demo.service", "0"},
		{"attach", "demo.service", "not-a-pid"},
		{"unknown"},
	} {
		if _, err := parseCgroupCommand(args, false, 0); err == nil {
			t.Fatalf("accepted %#v", args)
		}
	}
}

func TestPrintCgroupStatusAndList(t *testing.T) {
	var output bytes.Buffer
	request := cgrouptrack.Request{Operation: cgrouptrack.OpStatus}
	response := cgrouptrack.Response{OK: true, Status: &cgrouptrack.DaemonStatus{
		Healthy: true, CgroupRoot: "/sys/fs/cgroup/servicectl.slice", LastReconcile: "2026-07-11T00:00:00Z", Pending: 1, Abnormal: 2,
	}}
	if err := printCgroupResponse(&output, request, response); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"healthy", "/sys/fs/cgroup/servicectl.slice", "Pending: 1", "Abnormal: 2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q does not contain %q", text, want)
		}
	}

	output.Reset()
	request = cgrouptrack.Request{Operation: cgrouptrack.OpListUnits}
	response = cgrouptrack.Response{OK: true, Units: []cgrouptrack.UnitStatus{{
		Identity: cgrouptrack.InstanceIdentity{UnitKey: cgrouptrack.UnitKey{Mode: cgrouptrack.ModeUser, UID: 1000, Unit: "demo.service"}, MainPID: 42, Generation: 3},
		State:    cgrouptrack.StateTracked, MemberCount: 2,
	}}}
	if err := printCgroupResponse(&output, request, response); err != nil {
		t.Fatal(err)
	}
	text = output.String()
	for _, want := range []string{"demo.service", "1000", "tracked", "42", "2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q does not contain %q", text, want)
		}
	}
}

func TestPrintCgroupPIDsDoesNotExposeArguments(t *testing.T) {
	var output bytes.Buffer
	request := cgrouptrack.Request{Operation: cgrouptrack.OpListPIDs}
	response := cgrouptrack.Response{OK: true, PIDs: []cgrouptrack.ProcessStatus{{PID: 42, StartTime: 100, UID: 1000, Comm: "worker", MainPID: true}}}
	if err := printCgroupResponse(&output, request, response); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"42", "100", "1000", "worker", "main"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q does not contain %q", text, want)
		}
	}
	if strings.Contains(text, "argv") || strings.Contains(text, "environment") {
		t.Fatalf("sensitive output = %q", text)
	}
}

func TestCgroupCommandUsesDaemonWithoutSupervisionSetup(t *testing.T) {
	oldDo := doCgroupRequest
	oldSocket := cgroupdSocketPath
	t.Cleanup(func() {
		doCgroupRequest = oldDo
		cgroupdSocketPath = oldSocket
	})
	cgroupdSocketPath = "/test/sys-cgroupd.sock"
	called := false
	doCgroupRequest = func(_ context.Context, path string, request cgrouptrack.Request) (cgrouptrack.Response, error) {
		called = true
		if path != cgroupdSocketPath || request.Operation != cgrouptrack.OpStatus {
			t.Fatalf("path=%q request=%#v", path, request)
		}
		return cgrouptrack.Response{OK: true, Status: &cgrouptrack.DaemonStatus{Healthy: true}}, nil
	}
	if status := cgroupCommand([]string{"status"}); status != 0 || !called {
		t.Fatalf("status=%d called=%v", status, called)
	}
}

func TestCgroupCommandReportsSocketError(t *testing.T) {
	oldDo := doCgroupRequest
	t.Cleanup(func() { doCgroupRequest = oldDo })
	doCgroupRequest = func(context.Context, string, cgrouptrack.Request) (cgrouptrack.Response, error) {
		return cgrouptrack.Response{}, errors.New("socket unavailable")
	}
	if status := cgroupCommand([]string{"status"}); status != 1 {
		t.Fatalf("status = %d", status)
	}
}
