package dbusactivation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type Activator interface {
	Activate(context.Context, ActivateRequest) ActivationResult
}

type Peer struct {
	PID        int32
	ParentPID  int32
	Ancestors  []int32
	UID        uint32
	GID        uint32
	Executable string
	Device     uint64
	Inode      uint64
}

type ServerOptions struct {
	Path        string
	Mode        os.FileMode
	GID         int
	Activator   Activator
	Environment *EnvironmentStore
	Authorize   func(Frontend, Peer) error
	Reload      func() error
	Status      func() []byte
}

type Server struct {
	path        string
	mode        os.FileMode
	gid         int
	activator   Activator
	environment *EnvironmentStore
	authorize   func(Frontend, Peer) error
	reload      func() error
	status      func() []byte
}

func NewServer(options ServerOptions) *Server {
	environment := options.Environment
	if environment == nil {
		environment = &EnvironmentStore{}
	}
	return &Server{
		path:        options.Path,
		mode:        options.Mode,
		gid:         options.GID,
		activator:   options.Activator,
		environment: environment,
		authorize:   options.Authorize,
		reload:      options.Reload,
		status:      options.Status,
	}
}

func (s *Server) Serve(ctx context.Context) error {
	if s.path == "" {
		return errors.New("server path is required")
	}
	if s.activator == nil {
		return errors.New("activator is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(s.path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("existing control path is not a socket: %s", s.path)
		}
		if err := os.Remove(s.path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: s.path, Net: "unixpacket"})
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(s.path)
	mode := s.mode
	if mode == 0 {
		mode = 0o600
	}
	if s.gid >= 0 {
		if err := os.Chown(s.path, 0, s.gid); err != nil {
			return err
		}
	}
	if err := os.Chmod(s.path, mode); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	var handlers sync.WaitGroup
	defer handlers.Wait()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			s.handleConnection(ctx, conn)
		}()
	}
}

type serverConnection struct {
	conn        *net.UnixConn
	writeMu     sync.Mutex
	mu          sync.Mutex
	frontend    Frontend
	environment []string
	requests    map[uint64]context.CancelFunc
}

func (s *Server) handleConnection(parent context.Context, conn *net.UnixConn) {
	defer conn.Close()
	connectionDone := make(chan struct{})
	defer close(connectionDone)
	go func() {
		select {
		case <-parent.Done():
			_ = conn.Close()
		case <-connectionDone:
		}
	}()
	peer, err := unixPeer(conn)
	if err != nil {
		return
	}
	state := &serverConnection{conn: conn, requests: make(map[uint64]context.CancelFunc)}
	defer state.cancelAll()
	buffer := make([]byte, HeaderSize+int(MaxPayload)+1)
	for {
		n, err := conn.Read(buffer)
		if err != nil {
			return
		}
		packet, err := DecodePacket(buffer[:n])
		if err != nil {
			return
		}
		if state.frontend == 0 {
			if packet.Type != MessageHello || len(packet.Payload) != 1 {
				return
			}
			frontend := Frontend(packet.Payload[0])
			if !validFrontend(frontend) {
				return
			}
			if s.authorize != nil && s.authorize(frontend, peer) != nil {
				return
			}
			state.frontend = frontend
			if err := state.write(Packet{Type: MessageHello, RequestID: packet.RequestID, Payload: []byte{byte(frontend)}}); err != nil {
				return
			}
			continue
		}

		switch packet.Type {
		case MessageSetEnvironment:
			if state.frontend == FrontendAdmin {
				return
			}
			request, err := DecodeSetEnvironment(packet.Payload)
			if err != nil || request.Frontend != state.frontend {
				return
			}
			values := make([]string, 0, len(request.Values))
			for key, value := range request.Values {
				values = append(values, key+"="+value)
			}
			filtered, err := FilterEnvironment(values)
			if err != nil {
				if state.writeResult(packet.RequestID, ActivationResult{Code: ResultInvalidArguments, Detail: err.Error()}) != nil {
					return
				}
				continue
			}
			s.environment.Replace(state.frontend, filtered)
			state.environment = environmentValues(filtered)
			if state.writeResult(packet.RequestID, ActivationResult{Code: ResultSuccess}) != nil {
				return
			}
		case MessageActivate:
			if state.frontend == FrontendAdmin {
				return
			}
			request, err := DecodeActivate(packet.Payload)
			if err != nil || request.Frontend != state.frontend || packet.RequestID == 0 {
				return
			}
			requestCtx, cancel := context.WithCancel(parent)
			request.environment = append([]string(nil), state.environment...)
			request.environmentSet = true
			if !state.addRequest(packet.RequestID, cancel) {
				cancel()
				return
			}
			go func(requestID uint64, request ActivateRequest) {
				result := s.activator.Activate(requestCtx, request)
				state.removeRequest(requestID)
				_ = state.writeResult(requestID, result)
			}(packet.RequestID, request)
		case MessageCancel:
			state.cancelRequest(packet.RequestID)
		case MessageReload:
			if state.frontend != FrontendAdmin {
				return
			}
			result := ActivationResult{Code: ResultSuccess}
			if s.reload != nil {
				if err := s.reload(); err != nil {
					result = ActivationResult{Code: ResultFailed, Detail: err.Error()}
				}
			}
			if state.writeResult(packet.RequestID, result) != nil {
				return
			}
		case MessageStatus:
			if state.frontend != FrontendAdmin {
				return
			}
			var payload []byte
			if s.status != nil {
				payload = s.status()
			}
			if len(payload) > int(MaxPayload) || state.write(Packet{Type: MessageStatus, RequestID: packet.RequestID, Payload: payload}) != nil {
				return
			}
		case MessagePing:
			if state.write(Packet{Type: MessagePing, RequestID: packet.RequestID}) != nil {
				return
			}
		default:
			return
		}
	}
}

func environmentValues(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func (c *serverConnection) writeResult(requestID uint64, result ActivationResult) error {
	payload, err := EncodeActivationResult(result)
	if err != nil {
		return err
	}
	return c.write(Packet{Type: MessageActivationResult, RequestID: requestID, Payload: payload})
}

func (c *serverConnection) write(packet Packet) error {
	data, err := EncodePacket(packet)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.conn.Write(data)
	return err
}

func (c *serverConnection) addRequest(id uint64, cancel context.CancelFunc) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.requests[id]; exists {
		return false
	}
	c.requests[id] = cancel
	return true
}

func (c *serverConnection) removeRequest(id uint64) {
	c.mu.Lock()
	delete(c.requests, id)
	c.mu.Unlock()
}

func (c *serverConnection) cancelRequest(id uint64) {
	c.mu.Lock()
	cancel := c.requests[id]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *serverConnection) cancelAll() {
	c.mu.Lock()
	requests := c.requests
	c.requests = make(map[uint64]context.CancelFunc)
	c.mu.Unlock()
	for _, cancel := range requests {
		cancel()
	}
}

func unixPeer(conn *net.UnixConn) (Peer, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var credential *unix.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credential, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if socketErr != nil {
		return Peer{}, socketErr
	}
	executablePath := fmt.Sprintf("/proc/%d/exe", credential.Pid)
	executable, err := os.Readlink(executablePath)
	if err != nil {
		return Peer{}, err
	}
	var stat unix.Stat_t
	if err := unix.Stat(executablePath, &stat); err != nil {
		return Peer{}, err
	}
	ancestors, err := procAncestors(credential.Pid, 8)
	if err != nil {
		return Peer{}, err
	}
	parentPID := ancestors[0]
	return Peer{
		PID:        credential.Pid,
		ParentPID:  parentPID,
		Ancestors:  ancestors,
		UID:        credential.Uid,
		GID:        credential.Gid,
		Executable: executable,
		Device:     uint64(stat.Dev),
		Inode:      stat.Ino,
	}, nil
}

func procAncestors(pid int32, maximum int) ([]int32, error) {
	if maximum <= 0 {
		return nil, errors.New("ancestor limit must be positive")
	}
	ancestors := make([]int32, 0, maximum)
	current := pid
	for len(ancestors) < maximum {
		parent, err := procParentPID(current)
		if err != nil {
			return nil, err
		}
		ancestors = append(ancestors, parent)
		if parent == 1 {
			return ancestors, nil
		}
		current = parent
	}
	return ancestors, nil
}

func procParentPID(pid int32) (int32, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		value, found := strings.CutPrefix(line, "PPid:")
		if !found {
			continue
		}
		parent, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
		if err != nil || parent <= 0 {
			return 0, fmt.Errorf("invalid parent pid %q", value)
		}
		return int32(parent), nil
	}
	return 0, errors.New("parent pid is missing from process status")
}
