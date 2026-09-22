// Package dinit implements the local Dinit control protocol (v5 and newer).
package dinit

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type State uint8

const (
	Stopped State = iota
	Starting
	Started
	Stopping
)

func (s State) String() string {
	switch s {
	case Stopped:
		return "STOPPED"
	case Starting:
		return "STARTING"
	case Started:
		return "STARTED"
	case Stopping:
		return "STOPPING"
	}
	return "UNKNOWN"
}

type Status struct {
	Name       string
	State      State
	Target     State
	Flags      uint8
	StopReason uint8
	ExecStage  uint16
	PID        int
	ExitCode   int
	ExitStatus int
}

func (s Status) Failed() bool {
	return s.State == Stopped && (s.StopReason >= 2 && s.StopReason <= 5 || s.StopReason == 6 && (s.ExitCode != 1 || s.ExitStatus != 0))
}

type Event struct {
	Handle uint32
	Name   string
	Kind   uint8
	Status Status
}

var ErrNotLoaded = errors.New("Dinit service is not loaded")
var ErrRejected = errors.New("Dinit rejected operation (dependency, pin, or outstanding reference)")
var ErrEventOverflow = errors.New("Dinit event stream overflow; resnapshot required")

type Client struct {
	conn    net.Conn
	mu      sync.Mutex
	handles map[string]uint32
	replies chan []byte
	events  chan Event
	done    chan struct{}
	once    sync.Once
	errMu   sync.Mutex
	err     error
	Version uint16
}

func SocketPath(user bool) string {
	if v := os.Getenv("DINIT_SOCKET_PATH"); v != "" {
		return v
	}
	if user {
		if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
			return filepath.Join(v, "dinitctl")
		}
		return filepath.Join(os.Getenv("HOME"), ".dinitctl")
	}
	return "/run/dinitctl"
}
func Dial(ctx context.Context, path string) (*Client, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, handles: make(map[string]uint32), replies: make(chan []byte, 16), events: make(chan Event, 1024), done: make(chan struct{})}
	go c.readLoop()
	c.mu.Lock()
	p, err := c.request(ctx, []byte{0})
	c.mu.Unlock()
	if err == nil {
		if len(p) != 5 || p[0] != 58 {
			err = fmt.Errorf("invalid Dinit version reply")
		} else {
			minimum := binary.NativeEndian.Uint16(p[1:3])
			c.Version = binary.NativeEndian.Uint16(p[3:])
			if minimum > 5 || c.Version < 5 {
				err = fmt.Errorf("unsupported Dinit protocol %d..%d", minimum, c.Version)
			}
		}
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func (c *Client) Events() <-chan Event  { return c.events }
func (c *Client) Done() <-chan struct{} { return c.done }
func (c *Client) Err() error            { c.errMu.Lock(); defer c.errMu.Unlock(); return c.err }
func (c *Client) fail(err error) {
	c.once.Do(func() { c.errMu.Lock(); c.err = err; c.errMu.Unlock(); close(c.done); c.conn.Close() })
}
func (c *Client) Close() error { c.fail(net.ErrClosed); return nil }
func decodeStatus(p []byte) Status {
	s := Status{State: State(p[0]), Target: State(p[1]), Flags: p[2], StopReason: p[3], ExecStage: binary.NativeEndian.Uint16(p[4:6])}
	n := int(int32(binary.NativeEndian.Uint32(p[6:10])))
	if s.Flags&16 != 0 {
		s.PID = n
	} else {
		s.ExitCode = n
		s.ExitStatus = int(int32(binary.NativeEndian.Uint32(p[10:14])))
	}
	return s
}
func (c *Client) readLoop() {
	defer close(c.events)
	for {
		var tag [1]byte
		if _, err := io.ReadFull(c.conn, tag[:]); err != nil {
			c.fail(err)
			return
		}
		p := []byte{tag[0]}
		n := 0
		switch tag[0] {
		case 58:
			n = 4
		case 59:
			n = 6
		case 70:
			n = 15 // All clients deliberately use SERVICESTATUS5, including on v6.
		case 62:
			var length [1]byte
			if _, err := io.ReadFull(c.conn, length[:]); err != nil {
				c.fail(err)
				return
			}
			p = append(p, length[0])
			n = 14 + int(length[0])
			if length[0] == 0 {
				n += 256
			}
		case 65:
			countBytes := make([]byte, strconv.IntSize/8)
			if _, err := io.ReadFull(c.conn, countBytes); err != nil {
				c.fail(err)
				return
			}
			var count uint64
			if len(countBytes) == 8 {
				count = binary.NativeEndian.Uint64(countBytes)
			} else {
				count = uint64(binary.NativeEndian.Uint32(countBytes))
			}
			if count > 65536 {
				c.fail(errors.New("Dinit dependent list exceeds limit"))
				return
			}
			p = append(p, countBytes...)
			n = int(count) * 4
		case 100, 101:
			var length [1]byte
			if _, err := io.ReadFull(c.conn, length[:]); err != nil {
				c.fail(err)
				return
			}
			if length[0] < 2 {
				c.fail(errors.New("invalid Dinit event length"))
				return
			}
			p = append(p, length[0])
			n = int(length[0]) - 2
		case 50, 51, 52, 53, 54, 55, 60, 61, 63, 67, 68, 69, 71, 72, 74, 75, 76, 79:
		default:
			c.fail(fmt.Errorf("unsupported Dinit reply %d", tag[0]))
			return
		}
		size := len(p)
		p = append(p, make([]byte, n)...)
		if _, err := io.ReadFull(c.conn, p[size:]); err != nil {
			c.fail(err)
			return
		}
		if tag[0] == 100 {
			continue
		} // v5+ emits a legacy duplicate after every extended event.
		if tag[0] == 101 {
			if len(p) != 21 {
				c.fail(fmt.Errorf("invalid extended event length %d", len(p)))
				return
			}
			e := Event{Handle: binary.NativeEndian.Uint32(p[2:6]), Kind: p[6], Status: decodeStatus(p[7:])}
			select {
			case c.events <- e:
			case <-c.done:
				return
			default:
				c.fail(ErrEventOverflow)
				return
			}
		} else {
			select {
			case c.replies <- p:
			case <-c.done:
				return
			}
		}
	}
}

// request is called with mu held. Cancellation closes the stream, because a late
// reply must never be consumed as the reply to a later request.
func (c *Client) request(ctx context.Context, p []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { c.fail(ctx.Err()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		c.conn.SetWriteDeadline(deadline)
	} else {
		c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	}
	for len(p) > 0 {
		n, err := c.conn.Write(p)
		if err != nil {
			c.fail(err)
			return nil, err
		}
		p = p[n:]
	}
	select {
	case reply := <-c.replies:
		return reply, nil
	case <-ctx.Done():
		c.fail(ctx.Err())
		return nil, ctx.Err()
	case <-c.done:
		select {
		case reply := <-c.replies:
			return reply, nil
		default:
			return nil, c.Err()
		}
	}
}
func replyError(p []byte) error {
	if len(p) == 0 {
		return io.ErrUnexpectedEOF
	}
	switch p[0] {
	case 50, 61:
		return nil
	case 60:
		return ErrNotLoaded
	case 51, 65:
		return ErrRejected
	default:
		return fmt.Errorf("Dinit reply %d", p[0])
	}
}
func handlePacket(command byte, h uint32) []byte {
	p := make([]byte, 5)
	p[0] = command
	binary.NativeEndian.PutUint32(p[1:], h)
	return p
}
func (c *Client) handle(ctx context.Context, name string, load bool) (uint32, error) {
	if h, ok := c.handles[name]; ok {
		return h, nil
	}
	if len(name) == 0 || len(name) > 1021 {
		return 0, errors.New("invalid Dinit service name length")
	}
	p := make([]byte, 3+len(name))
	p[0] = 1
	if load {
		p[0] = 2
	}
	binary.NativeEndian.PutUint16(p[1:3], uint16(len(name)))
	copy(p[3:], name)
	r, err := c.request(ctx, p)
	if err != nil {
		return 0, err
	}
	if r[0] != 59 {
		if err = replyError(r); err == nil {
			err = fmt.Errorf("expected service record, got reply %d", r[0])
		}
		return 0, err
	}
	h := binary.NativeEndian.Uint32(r[2:6])
	c.handles[name] = h
	return h, nil
}
func (c *Client) Status(ctx context.Context, name string, load bool) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, err := c.handle(ctx, name, load)
	if err != nil {
		return Status{}, err
	}
	return c.status(ctx, name, h)
}
func (c *Client) status(ctx context.Context, name string, h uint32) (Status, error) {
	p, err := c.request(ctx, handlePacket(26, h))
	if err != nil {
		return Status{}, err
	}
	if p[0] != 70 {
		if err = replyError(p); err == nil {
			err = fmt.Errorf("expected service status, got reply %d", p[0])
		}
		return Status{}, err
	}
	s := decodeStatus(p[2:])
	s.Name = name
	return s, nil
}
func (c *Client) List(ctx context.Context) ([]Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := c.request(ctx, []byte{25})
	if err != nil {
		return nil, err
	}
	var rows []Status
	for p[0] != 63 {
		if p[0] != 62 {
			return nil, replyError(p)
		}
		s := decodeStatus(p[2:16])
		s.Name = string(p[16:])
		rows = append(rows, s)
		select {
		case p = <-c.replies:
		case <-ctx.Done():
			c.fail(ctx.Err())
			return nil, ctx.Err()
		case <-c.done:
			return nil, c.Err()
		}
	}
	return rows, nil
}

// Change only waits for command acceptance. The controller waits for final state
// without holding this lock, so a logger can query its PID during service startup.
func (c *Client) Change(ctx context.Context, action, name string, flags byte, signal int) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, err := c.handle(ctx, name, action == "start" || action == "wake" || action == "reload")
	if err != nil {
		return Status{}, err
	}
	var p []byte
	switch action {
	case "start", "stop", "restart", "wake", "release":
		command := byte(3)
		switch action {
		case "stop", "restart":
			command = 4
		case "wake":
			command = 5
		case "release":
			command = 6
		}
		if action == "restart" {
			flags |= 4
		}
		p = append([]byte{command, flags}, handlePacket(0, h)[1:]...)
	case "reload":
		p = handlePacket(16, h)
	case "unload":
		p = handlePacket(9, h)
	case "unpin":
		p = handlePacket(7, h)
	case "signal":
		p = make([]byte, 9)
		p[0] = 21
		binary.NativeEndian.PutUint32(p[1:5], uint32(signal))
		binary.NativeEndian.PutUint32(p[5:], h)
	default:
		return Status{}, fmt.Errorf("unsupported Dinit action %q", action)
	}
	r, err := c.request(ctx, p)
	if action == "reload" {
		delete(c.handles, name)
	}
	if err != nil {
		return Status{}, err
	}
	if err = replyError(r); err != nil {
		return Status{}, err
	}
	if action == "unload" {
		delete(c.handles, name)
		return Status{Name: name, State: Stopped}, nil
	}
	if action == "reload" {
		return Status{Name: name}, nil
	}
	return c.status(ctx, name, h)
}
func (c *Client) SetEnv(ctx context.Context, values []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, value := range values {
		if len(value) == 0 || len(value) > 1021 {
			return errors.New("invalid environment entry length")
		}
		p := make([]byte, 3+len(value))
		p[0] = 17
		binary.NativeEndian.PutUint16(p[1:3], uint16(len(value)))
		copy(p[3:], value)
		r, err := c.request(ctx, p)
		if err != nil {
			return err
		}
		if err = replyError(r); err != nil {
			return err
		}
	}
	return nil
}
