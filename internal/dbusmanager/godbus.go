package dbusmanager

import (
	"context"
	"errors"
	"strings"

	"github.com/godbus/dbus/v5"
)

type Godbus struct {
	conn *dbus.Conn
}

func NewBus(address string) (*Godbus, error) {
	conn, err := dbus.Dial(address)
	if err != nil {
		return nil, err
	}
	if err := conn.Auth(nil); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.Hello(); err != nil {
		conn.Close()
		return nil, err
	}
	return &Godbus{conn: conn}, nil
}

func NewSystemBus() (*Godbus, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	return &Godbus{conn: conn}, nil
}

func NewSessionBus() (*Godbus, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, err
	}
	return &Godbus{conn: conn}, nil
}

func (b *Godbus) Close() error {
	if b == nil || b.conn == nil {
		return nil
	}
	return b.conn.Close()
}

func (b *Godbus) StartServiceByName(ctx context.Context, name string) error {
	var result uint32
	return b.conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus").CallWithContext(ctx, "org.freedesktop.DBus.StartServiceByName", 0, name, uint32(0)).Store(&result)
}

func (b *Godbus) GetNameOwner(ctx context.Context, name string) (string, error) {
	var owner string
	err := b.conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus").CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, name).Store(&owner)
	if err != nil {
		if isNameHasNoOwner(err) {
			return "", ErrNoOwner
		}
		return "", err
	}
	return owner, nil
}

func (b *Godbus) GetConnectionUnixProcessID(ctx context.Context, name string) (uint32, error) {
	var pid uint32
	err := b.conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus").CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, name).Store(&pid)
	return pid, err
}

func (b *Godbus) WatchNameOwnerChanged(ctx context.Context, name string) (<-chan NameOwnerChanged, func(), error) {
	options := []dbus.MatchOption{
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchObjectPath("/org/freedesktop/DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, name),
	}
	if err := b.conn.AddMatchSignalContext(ctx, options...); err != nil {
		return nil, func() {}, err
	}
	raw := make(chan *dbus.Signal, 16)
	out := make(chan NameOwnerChanged, 16)
	b.conn.Signal(raw)
	stop := func() {
		b.conn.RemoveSignal(raw)
		_ = b.conn.RemoveMatchSignalContext(context.Background(), options...)
	}
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case sig, ok := <-raw:
				if !ok {
					return
				}
				change, ok := parseNameOwnerChanged(sig)
				if ok && change.Name == name {
					out <- change
				}
			}
		}
	}()
	return out, stop, nil
}

func parseNameOwnerChanged(sig *dbus.Signal) (NameOwnerChanged, bool) {
	if sig == nil || sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) != 3 {
		return NameOwnerChanged{}, false
	}
	name, ok := sig.Body[0].(string)
	if !ok {
		return NameOwnerChanged{}, false
	}
	oldOwner, ok := sig.Body[1].(string)
	if !ok {
		return NameOwnerChanged{}, false
	}
	newOwner, ok := sig.Body[2].(string)
	if !ok {
		return NameOwnerChanged{}, false
	}
	return NameOwnerChanged{Name: name, OldOwner: oldOwner, NewOwner: newOwner}, true
}

func isNameHasNoOwner(err error) bool {
	if err == nil {
		return false
	}
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) && dbusErr.Name == "org.freedesktop.DBus.Error.NameHasNoOwner" {
		return true
	}
	return strings.Contains(err.Error(), "NameHasNoOwner")
}
