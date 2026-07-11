package cgrouptrack

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultMaxConnections = 32
	defaultMaxPerUID      = 4
)

var (
	ErrPIDNamespaceMismatch = errors.New("peer is in a different PID namespace")
	ErrAccessDenied         = errors.New("cgroup request is not authorized")
)

type Peer struct {
	PID          int
	UID          uint32
	GID          uint32
	PIDNamespace FileIdentity
}

type ServerOptions struct {
	Path              string
	Mode              os.FileMode
	Service           Service
	Proc              ProcFS
	RequestTimeout    time.Duration
	MaxConnections    int
	MaxRequestsPerUID int
	ManagedCgroupPath string
	ResolvePeer       func(*net.UnixConn, ProcFS) (Peer, error)
}

type Server struct {
	path              string
	mode              os.FileMode
	service           Service
	proc              ProcFS
	requestTimeout    time.Duration
	connectionSlots   chan struct{}
	maxRequestsPerUID int
	managedPath       string
	mu                sync.Mutex
	activeByUID       map[uint32]int
	attachByUID       map[uint32]*attachBucket
	resolvePeer       func(*net.UnixConn, ProcFS) (Peer, error)
}

type attachBucket struct {
	tokens float64
	last   time.Time
}

func NewServer(options ServerOptions) *Server {
	mode := options.Mode
	if mode == 0 {
		mode = 0o666
	}
	timeout := options.RequestTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	maxConnections := options.MaxConnections
	if maxConnections <= 0 {
		maxConnections = defaultMaxConnections
	}
	maxPerUID := options.MaxRequestsPerUID
	if maxPerUID <= 0 {
		maxPerUID = defaultMaxPerUID
	}
	managedPath := filepath.Clean(options.ManagedCgroupPath)
	if managedPath == "." || options.ManagedCgroupPath == "" {
		managedPath = "/servicectl.slice"
	}
	resolvePeer := options.ResolvePeer
	if resolvePeer == nil {
		resolvePeer = unixPeer
	}
	return &Server{
		path: options.Path, mode: mode, service: options.Service, proc: options.Proc,
		requestTimeout: timeout, connectionSlots: make(chan struct{}, maxConnections),
		maxRequestsPerUID: maxPerUID, managedPath: managedPath,
		activeByUID: make(map[uint32]int), attachByUID: make(map[uint32]*attachBucket),
		resolvePeer: resolvePeer,
	}
}

func (s *Server) Serve(ctx context.Context) error {
	if s.path == "" || s.service == nil || s.proc == nil {
		return errors.New("server path, service, and proc source are required")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(s.path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("existing server path is not a socket: %s", s.path)
		}
		if err := os.Remove(s.path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.path, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(s.path)
	if err := os.Chmod(s.path, s.mode); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	var handlers sync.WaitGroup
	defer handlers.Wait()
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		select {
		case s.connectionSlots <- struct{}{}:
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer func() { <-s.connectionSlots }()
				s.handleConnection(ctx, connection)
			}()
		default:
			_ = connection.Close()
		}
	}
}

func (s *Server) handleConnection(parent context.Context, connection *net.UnixConn) {
	defer connection.Close()
	deadline := time.Now().Add(s.requestTimeout)
	_ = connection.SetDeadline(deadline)
	request, err := DecodeRequest(connection)
	if err != nil {
		_ = EncodeResponse(connection, failureResponse("invalid-request", err))
		return
	}
	peer, err := s.resolvePeer(connection, s.proc)
	if err != nil {
		_ = EncodeResponse(connection, failureResponse("peer-credentials", err))
		return
	}
	daemonNamespace, err := s.proc.SelfPIDNamespace()
	if err != nil {
		daemonNamespace = FileIdentity{}
	}
	scope, err := authorizeRequestWithRoot(peer, request, daemonNamespace, s.proc, s.managedCgroupPath())
	if err != nil {
		_ = EncodeResponse(connection, failureResponse("access-denied", err))
		return
	}
	if !s.acquireUID(peer.UID) {
		_ = EncodeResponse(connection, failureResponse("busy", errors.New("too many concurrent requests for peer UID")))
		return
	}
	defer s.releaseUID(peer.UID)
	if request.Operation == OpAttach && !s.allowAttach(peer.UID, time.Now()) {
		_ = EncodeResponse(connection, failureResponse("rate-limited", errors.New("attach rate limit exceeded")))
		return
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	response := s.dispatch(ctx, scope, request)
	_ = EncodeResponse(connection, response)
}

func (s *Server) SetManagedCgroupPath(path string) {
	clean := filepath.Clean(path)
	if clean == "." || path == "" {
		clean = "/servicectl.slice"
	}
	s.mu.Lock()
	s.managedPath = clean
	s.mu.Unlock()
}

func (s *Server) managedCgroupPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.managedPath
}

func (s *Server) dispatch(ctx context.Context, scope Scope, request Request) Response {
	switch request.Operation {
	case OpStatus:
		status, err := s.service.Status(ctx, scope)
		if err != nil {
			return failureResponse("status-failed", err)
		}
		return Response{OK: true, Status: &status}
	case OpListUnits:
		units, err := s.service.ListUnits(ctx, scope)
		if err != nil {
			return failureResponse("list-units-failed", err)
		}
		return Response{OK: true, Units: units}
	case OpGetUnit:
		unit, err := s.service.GetUnit(ctx, scope, request.Unit)
		if err != nil {
			return failureResponse("get-unit-failed", err)
		}
		return Response{OK: true, Unit: &unit}
	case OpListPIDs:
		pids, err := s.service.ListPIDs(ctx, scope, request.Unit)
		if err != nil {
			return failureResponse("list-pids-failed", err)
		}
		return Response{OK: true, PIDs: pids}
	case OpAttach:
		unit, err := s.service.Attach(ctx, scope, request.Unit, request.PID)
		if err != nil {
			return failureResponse("attach-failed", err)
		}
		return Response{OK: true, Unit: &unit}
	default:
		return failureResponse("invalid-operation", errors.New("unknown operation"))
	}
}

func authorizeRequest(peer Peer, request Request, daemonNamespace FileIdentity, proc ProcFS) (Scope, error) {
	return authorizeRequestWithRoot(peer, request, daemonNamespace, proc, "/servicectl.slice")
}

func authorizeRequestWithRoot(peer Peer, request Request, daemonNamespace FileIdentity, proc ProcFS, managedRoot string) (Scope, error) {
	if peer.PIDNamespace != (FileIdentity{}) && daemonNamespace != (FileIdentity{}) && peer.PIDNamespace != daemonNamespace {
		return Scope{}, ErrPIDNamespaceMismatch
	}
	if err := request.Validate(); err != nil {
		return Scope{}, err
	}
	if peer.UID == 0 {
		scope := Scope{Mode: request.Mode, UID: request.UID, Global: request.Mode == ""}
		if request.Operation == OpAttach && request.Mode == ModeUser {
			if err := authorizeAttachPID(peer, request, proc, managedRoot); err != nil {
				return Scope{}, err
			}
		}
		return scope, nil
	}
	if request.Mode != ModeUser || request.UID != peer.UID {
		return Scope{}, ErrAccessDenied
	}
	if request.Operation == OpAttach {
		if err := authorizeAttachPID(peer, request, proc, managedRoot); err != nil {
			return Scope{}, err
		}
	}
	return Scope{Mode: ModeUser, UID: peer.UID}, nil
}

func authorizeAttachPID(peer Peer, request Request, proc ProcFS, managedRoot string) error {
	process, err := proc.Inspect(request.PID)
	if err != nil {
		return err
	}
	if process.PIDFD >= 0 {
		defer unix.Close(process.PIDFD)
	}
	if process.UID != request.UID {
		return ErrAccessDenied
	}
	if peer.UID != 0 && process.UID != peer.UID {
		return ErrAccessDenied
	}
	mode, uid, managed := managedProcessScope(process.Cgroup, managedRoot)
	if !managed {
		return nil
	}
	if mode != ModeUser || uid != request.UID {
		return ErrAccessDenied
	}
	return nil
}

func managedProcessScope(path string, managedRoot string) (Mode, uint32, bool) {
	root := "/" + strings.Trim(strings.TrimSpace(managedRoot), "/")
	clean := filepath.Clean(path)
	prefix := root
	if root != "/" {
		prefix += "/"
	}
	if clean != root && !strings.HasPrefix(clean, prefix) {
		return "", 0, false
	}
	relative := strings.TrimPrefix(clean, prefix)
	if root == "/" {
		relative = strings.TrimPrefix(clean, "/")
	}
	parts := strings.Split(relative, "/")
	if len(parts) >= 1 && parts[0] == "system" {
		return ModeSystem, 0, true
	}
	if len(parts) >= 2 && parts[0] == "user" {
		uid, err := strconv.ParseUint(parts[1], 10, 32)
		if err == nil {
			return ModeUser, uint32(uid), true
		}
	}
	return "", 0, true
}

func unixPeer(connection *net.UnixConn, proc ProcFS) (Peer, error) {
	raw, err := connection.SyscallConn()
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
	namespace, err := proc.PIDNamespace(int(credential.Pid))
	if err != nil {
		namespace = FileIdentity{}
	}
	return Peer{PID: int(credential.Pid), UID: credential.Uid, GID: credential.Gid, PIDNamespace: namespace}, nil
}

func failureResponse(code string, err error) Response {
	return Response{OK: false, Error: &APIError{Code: code, Message: err.Error()}}
}

func (s *Server) acquireUID(uid uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeByUID[uid] >= s.maxRequestsPerUID {
		return false
	}
	s.activeByUID[uid]++
	return true
}

func (s *Server) releaseUID(uid uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeByUID[uid]--
	if s.activeByUID[uid] <= 0 {
		delete(s.activeByUID, uid)
	}
}

func (s *Server) allowAttach(uid uint32, now time.Time) bool {
	const capacity = 4.0
	const refillPerSecond = 1.0
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket := s.attachByUID[uid]
	if bucket == nil {
		bucket = &attachBucket{tokens: capacity, last: now}
		s.attachByUID[uid] = bucket
	}
	elapsed := now.Sub(bucket.last).Seconds()
	if elapsed > 0 {
		bucket.tokens += elapsed * refillPerSecond
		if bucket.tokens > capacity {
			bucket.tokens = capacity
		}
		bucket.last = now
	}
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}
