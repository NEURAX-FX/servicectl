package dbusactivation

import (
	"context"

	"servicectl/internal/dbusmanager"
)

type BusNameClient interface {
	GetNameOwner(context.Context, string) (string, error)
	WatchNameOwnerChanged(context.Context, string) (<-chan dbusmanager.NameOwnerChanged, func(), error)
}

type GodbusMonitor struct {
	bus BusNameClient
}

func NewGodbusMonitor(bus BusNameClient) *GodbusMonitor {
	return &GodbusMonitor{bus: bus}
}

func (m *GodbusMonitor) Owner(ctx context.Context, name string) (string, error) {
	return m.bus.GetNameOwner(ctx, name)
}

func (m *GodbusMonitor) Watch(ctx context.Context, name string) (<-chan dbusmanager.NameOwnerChanged, func(), error) {
	return m.bus.WatchNameOwnerChanged(ctx, name)
}
