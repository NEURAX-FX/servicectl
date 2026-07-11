package dbusactivation

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestManagedStarterInstallsStartsAndActivates(t *testing.T) {
	var calls []string
	starter := NewManagedStarter(ManagedOptions{
		Install: func(_ context.Context, unit string) error {
			calls = append(calls, "install "+unit)
			return nil
		},
		Start: func(_ context.Context, service string) error {
			calls = append(calls, "start "+service)
			return nil
		},
		Activate: func(_ context.Context, path string) error {
			calls = append(calls, "activate "+path)
			return nil
		},
	})

	started, err := starter.Start(context.Background(), Route{Kind: RouteManaged, Managed: ManagedRoute{
		Unit:        "systemd-localed",
		ServiceName: "systemd-localed-dbusd",
		ControlPath: "/run/servicectl/managed/systemd-localed-dbusd/control.sock",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if started.Exit != nil || started.Stop != nil {
		t.Fatalf("managed start result = %#v, want empty", started)
	}
	want := []string{
		"install systemd-localed",
		"start systemd-localed-dbusd",
		"activate /run/servicectl/managed/systemd-localed-dbusd/control.sock",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestNotifyDBusRouteTargetsNotifydWrapperNotBackend(t *testing.T) {
	var started string
	var activated string
	starter := NewManagedStarter(ManagedOptions{
		Install: func(context.Context, string) error { return nil },
		Start: func(_ context.Context, service string) error {
			started = service
			return nil
		},
		Activate: func(_ context.Context, path string) error {
			activated = path
			return nil
		},
	})

	_, err := starter.Start(context.Background(), Route{Kind: RouteManaged, Managed: ManagedRoute{
		Unit:        "systemd-localed",
		ServiceName: "systemd-localed-dbusd",
		ControlPath: "/run/servicectl/managed/systemd-localed-dbusd/control.sock",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if started != "systemd-localed-dbusd" {
		t.Fatalf("started service = %q, want notifyd wrapper", started)
	}
	if activated != "/run/servicectl/managed/systemd-localed-dbusd/control.sock" {
		t.Fatalf("activation path = %q", activated)
	}
}

func TestManagedStarterStopsAtFirstFailure(t *testing.T) {
	starter := NewManagedStarter(ManagedOptions{
		Install: func(context.Context, string) error { return errors.New("install failed") },
		Start:   func(context.Context, string) error { t.Fatal("start called"); return nil },
	})
	if _, err := starter.Start(context.Background(), Route{Kind: RouteManaged, Managed: ManagedRoute{Unit: "unit"}}, nil); err == nil {
		t.Fatal("Start unexpectedly succeeded")
	}
}

func TestCompositeStarterDispatchesByRouteKind(t *testing.T) {
	native := &recordingStarter{}
	managed := &recordingStarter{}
	starter := CompositeStarter{Native: native, Managed: managed}
	if _, err := starter.Start(context.Background(), Route{Kind: RouteNative}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := starter.Start(context.Background(), Route{Kind: RouteManaged}, nil); err != nil {
		t.Fatal(err)
	}
	if native.calls != 1 || managed.calls != 1 {
		t.Fatalf("calls native=%d managed=%d", native.calls, managed.calls)
	}
}

type recordingStarter struct{ calls int }

func (s *recordingStarter) Start(context.Context, Route, []string) (StartResult, error) {
	s.calls++
	return StartResult{}, nil
}
