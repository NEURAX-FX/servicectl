package dbusmanager

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeBus struct {
	startCalls int
	owners     []string
	changed    chan NameOwnerChanged
}

func (b *fakeBus) StartServiceByName(_ context.Context, name string) error {
	if name != "org.freedesktop.hostname1" {
		return errors.New("unexpected bus name")
	}
	b.startCalls++
	return nil
}

func (b *fakeBus) GetNameOwner(_ context.Context, _ string) (string, error) {
	if len(b.owners) == 0 {
		return "", ErrNoOwner
	}
	owner := b.owners[0]
	b.owners = b.owners[1:]
	if owner == "" {
		return "", ErrNoOwner
	}
	return owner, nil
}

func (b *fakeBus) WatchNameOwnerChanged(_ context.Context, _ string) (<-chan NameOwnerChanged, func(), error) {
	return b.changed, func() {}, nil
}

func TestManagerStartsBackendWhenBusActivationIsRequested(t *testing.T) {
	bus := &fakeBus{changed: make(chan NameOwnerChanged, 1)}
	started := make(chan struct{}, 1)
	mgr := New(Options{
		BusName: "org.freedesktop.hostname1",
		Bus:     bus,
		StartBackend: func(context.Context) error {
			started <- struct{}{}
			return nil
		},
	})

	if err := mgr.Activate(context.Background()); err != nil {
		t.Fatalf("Activate returned error: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("backend was not started")
	}
	if bus.startCalls != 1 {
		t.Fatalf("StartServiceByName calls = %d, want 1", bus.startCalls)
	}
}

func TestManagerWaitsForNameOwner(t *testing.T) {
	bus := &fakeBus{owners: []string{"", ":1.42"}, changed: make(chan NameOwnerChanged, 1)}
	mgr := New(Options{BusName: "org.freedesktop.hostname1", Bus: bus})

	owner, err := mgr.WaitForOwner(context.Background(), 10*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForOwner returned error: %v", err)
	}
	if owner != ":1.42" {
		t.Fatalf("owner = %q, want :1.42", owner)
	}
}

func TestManagerReportsOwnerLoss(t *testing.T) {
	bus := &fakeBus{changed: make(chan NameOwnerChanged, 1)}
	mgr := New(Options{BusName: "org.freedesktop.hostname1", Bus: bus})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lost, err := mgr.WatchOwner(ctx)
	if err != nil {
		t.Fatalf("WatchOwner returned error: %v", err)
	}
	bus.changed <- NameOwnerChanged{Name: "org.freedesktop.hostname1", OldOwner: ":1.42", NewOwner: ""}

	select {
	case <-lost:
	case <-time.After(time.Second):
		t.Fatal("owner loss was not reported")
	}
}
