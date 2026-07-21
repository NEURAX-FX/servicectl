package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const brokerPacketDeadline = 30 * time.Second

const (
	defaultBrokerSocketPath     = "/run/servicectl/logd"
	defaultMaxConnections       = 256
	defaultMaxConnectionsPerUID = 32
)

type brokerDependencies struct {
	resolveRoute func(peerCredentials) (logRoute, error)
	writeJournal func(logRoute, int, string) error
}

type brokerConfig struct {
	SocketPath           string
	JournalSocketPath    string
	ReadyFD              int
	MaxConnections       int
	MaxConnectionsPerUID int
}

type brokerConnectionLimiter struct {
	mu        sync.Mutex
	maxTotal  int
	maxPerUID int
	total     int
	perUID    map[uint32]int
}

func newBrokerConnectionLimiter(maxTotal, maxPerUID int) *brokerConnectionLimiter {
	return &brokerConnectionLimiter{maxTotal: maxTotal, maxPerUID: maxPerUID, perUID: make(map[uint32]int)}
}

func (limiter *brokerConnectionLimiter) Acquire(uid uint32) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.total >= limiter.maxTotal || limiter.perUID[uid] >= limiter.maxPerUID {
		return false
	}
	limiter.total++
	limiter.perUID[uid]++
	return true
}

func (limiter *brokerConnectionLimiter) Release(uid uint32) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.perUID[uid] == 0 {
		return
	}
	limiter.total--
	limiter.perUID[uid]--
	if limiter.perUID[uid] == 0 {
		delete(limiter.perUID, uid)
	}
}

func runBroker(ctx context.Context, cfg brokerConfig) error {
	if cfg.SocketPath == "" {
		cfg.SocketPath = defaultBrokerSocketPath
	}
	if cfg.JournalSocketPath == "" {
		cfg.JournalSocketPath = defaultJournalSocketPath
	}
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = defaultMaxConnections
	}
	if cfg.MaxConnectionsPerUID <= 0 {
		cfg.MaxConnectionsPerUID = defaultMaxConnectionsPerUID
	}
	if err := prepareBrokerSocket(cfg.SocketPath); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: cfg.SocketPath, Net: "unixpacket"})
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(cfg.SocketPath)
	if err := os.Chmod(cfg.SocketPath, 0666); err != nil {
		return err
	}
	journal := newJournalWriter(cfg.JournalSocketPath)
	defer journal.Close()
	dependencies := defaultRouteDependencies()
	brokerDeps := brokerDependencies{
		resolveRoute: func(peer peerCredentials) (logRoute, error) {
			return resolvePeerRoute(peer, dependencies)
		},
		writeJournal: journal.Write,
	}

	if err := signalBrokerReady(cfg.ReadyFD); err != nil {
		return err
	}
	connections := newBrokerConnectionLimiter(cfg.MaxConnections, cfg.MaxConnectionsPerUID)
	var workers sync.WaitGroup
	var activeMu sync.Mutex
	active := make(map[*net.UnixConn]struct{})
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		activeMu.Lock()
		defer activeMu.Unlock()
		for conn := range active {
			_ = conn.Close()
		}
	}()
	defer workers.Wait()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		peer, err := unixPeerCredentials(conn)
		if err != nil {
			_ = conn.Close()
			continue
		}
		if ctx.Err() != nil {
			_ = conn.Close()
			return nil
		}
		if !connections.Acquire(peer.UID) {
			_ = conn.Close()
			continue
		}
		activeMu.Lock()
		active[conn] = struct{}{}
		activeMu.Unlock()
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer connections.Release(peer.UID)
			defer func() {
				activeMu.Lock()
				delete(active, conn)
				activeMu.Unlock()
			}()
			defer conn.Close()
			if err := serveBrokerConnection(conn, peer, brokerDeps); err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "sys-logd: rejected broker connection from pid=%d uid=%d: %v\n", peer.PID, peer.UID, err)
			}
		}()
	}
}

func prepareBrokerSocket(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("broker socket path %q is not absolute", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("broker socket path %s exists and is not a socket", path)
	}
	if conn, dialErr := net.DialTimeout("unixpacket", path, 100*time.Millisecond); dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("broker socket %s is already active", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return fmt.Errorf("stale broker socket %s is not root-owned", path)
	}
	return os.Remove(path)
}

func unixPeerCredentials(conn *net.UnixConn) (peerCredentials, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return peerCredentials{}, err
	}
	var credentials *unix.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return peerCredentials{}, err
	}
	if socketErr != nil {
		return peerCredentials{}, socketErr
	}
	if credentials == nil {
		return peerCredentials{}, errors.New("peer credentials are unavailable")
	}
	return peerCredentials{PID: int(credentials.Pid), UID: credentials.Uid, GID: credentials.Gid}, nil
}

func signalBrokerReady(fd int) error {
	if fd <= 0 {
		return nil
	}
	file := os.NewFile(uintptr(fd), "sys-logd-ready")
	if file == nil {
		return fmt.Errorf("ready fd %d is invalid", fd)
	}
	_, err := file.Write([]byte{'\n'})
	return err
}

func serveBrokerConnection(conn *net.UnixConn, peer peerCredentials, deps brokerDependencies) error {
	if deps.resolveRoute == nil || deps.writeJournal == nil {
		return errors.New("broker dependencies are incomplete")
	}
	hello, err := readBrokerPacket(conn)
	if err != nil {
		if isQuietBrokerReadError(err) {
			return nil
		}
		_ = writeBrokerNack(conn, 0, false, err)
		return err
	}
	if hello.Kind != packetHello {
		err := fmt.Errorf("first broker packet is %q, want hello", hello.Kind)
		_ = writeBrokerNack(conn, 0, false, err)
		return err
	}
	route, err := deps.resolveRoute(peer)
	if err != nil {
		_ = writeBrokerNack(conn, 0, false, err)
		return err
	}

	for {
		packet, err := readBrokerPacket(conn)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if isQuietBrokerReadError(err) {
				return nil
			}
			_ = writeBrokerNack(conn, 0, false, err)
			return err
		}
		if packet.Kind != packetLog {
			err := fmt.Errorf("broker packet kind %q is not a log record", packet.Kind)
			_ = writeBrokerNack(conn, packet.Sequence, false, err)
			return err
		}
		if err := deps.writeJournal(route, packet.Priority, packet.Message); err != nil {
			_ = writeBrokerNack(conn, packet.Sequence, true, err)
			return err
		}
		if err := writeBrokerPacket(conn, protocolPacket{Version: protocolVersion, Kind: packetAck, Sequence: packet.Sequence}); err != nil {
			return err
		}
	}
}

func isQuietBrokerReadError(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func readBrokerPacket(conn *net.UnixConn) (protocolPacket, error) {
	if err := conn.SetReadDeadline(time.Now().Add(brokerPacketDeadline)); err != nil {
		return protocolPacket{}, err
	}
	buffer := make([]byte, maxProtocolPacketBytes+1)
	n, err := conn.Read(buffer)
	if err != nil {
		return protocolPacket{}, err
	}
	return decodeProtocolPacket(buffer[:n])
}

func writeBrokerPacket(conn *net.UnixConn, packet protocolPacket) error {
	encoded, err := encodeProtocolPacket(packet)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(brokerPacketDeadline)); err != nil {
		return err
	}
	written, err := conn.Write(encoded)
	if err != nil {
		return err
	}
	if written != len(encoded) {
		return fmt.Errorf("short broker packet write: %d of %d bytes", written, len(encoded))
	}
	return nil
}

func writeBrokerNack(conn *net.UnixConn, sequence uint64, retryable bool, err error) error {
	if err == nil {
		err = errors.New("broker request rejected")
	}
	return writeBrokerPacket(conn, protocolPacket{
		Version:   protocolVersion,
		Kind:      packetNack,
		Sequence:  sequence,
		Retryable: retryable,
		Error:     err.Error(),
	})
}
