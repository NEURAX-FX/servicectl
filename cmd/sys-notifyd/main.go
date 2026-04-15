package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"servicectl/internal/visionapi"
)

type config struct {
	serviceName  string
	serviceType  string
	command      string
	stopCommand  string
	userMode     bool
	verbose      bool
	readyFD      int
	startNow     bool
	stateFile    string
	mode         string
	socketUser   string
	socketGroup  string
	readyTimeout time.Duration
	stopTimeout  time.Duration
	killSignal   string
	notify       bool
	notifyPath   string
	listens      listenSpecs
	fdNames      stringList
}

type listenSpecs []string

type stringList []string

func (l *listenSpecs) String() string {
	return strings.Join(*l, ",")
}

func (l *listenSpecs) Set(value string) error {
	if value == "" {
		return fmt.Errorf("empty listen spec")
	}
	*l = append(*l, value)
	return nil
}

func (l *stringList) String() string {
	return strings.Join(*l, ",")
}

func (l *stringList) Set(value string) error {
	if value == "" {
		return fmt.Errorf("empty list value")
	}
	*l = append(*l, value)
	return nil
}

type activationSocket struct {
	file     *os.File
	closer   func() error
	cleanup  func()
	describe string
	unixPath string
	packet   bool
	netType  string
	addr     string
}

type server struct {
	cfg         config
	logger      *log.Logger
	sockets     []activationSocket
	notifyConn  *net.UnixConn
	childReady  chan struct{}
	childReadyN uint64
	cmd         *exec.Cmd
	cmdExit     chan childExitEvent
	cmdDone     chan struct{}
	cmdErr      error
	status      string
	failureText string
	mainPID     int
	phase       string
	childState  string
	epollFD     int
	mu          sync.Mutex
	extendStart chan time.Duration
	extendStop  chan time.Duration
	watchdogSet chan time.Duration
	watchdogHit chan struct{}
	watchdogErr chan error
	startReq    chan string
	startErr    chan error
	shutdownCh  chan struct{}
	debug       *startupDebugger
}

type childExitEvent struct {
	err error
}

func main() {
	cfg := parseFlags()
	logger := log.New(os.Stdout, "sys-notifyd: ", log.LstdFlags)

	srv, err := newServer(cfg, logger)
	if err != nil {
		logger.Fatal(err)
	}
	defer srv.cleanup()

	if err := srv.armActivation(); err != nil {
		logger.Fatal(err)
	}

	if cfg.command != "" && cfg.startNow {
		if err := srv.startChild(); err != nil {
			logger.Fatal(err)
		}
	}

	if err := srv.waitUntilReady(); err != nil {
		logger.Fatal(err)
	}
	srv.emitReady()
	logger.Printf("ready: service=%s sockets=%d", cfg.serviceName, len(srv.sockets))
	srv.writeState()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case sig := <-sigCh:
			logger.Printf("stopping on signal %s", sig)
			if err := srv.stopChild(); err != nil {
				logger.Fatal(err)
			}
			return
		case event := <-srv.cmdExit:
			if err := srv.handleChildExit(event.err); err != nil {
				logger.Fatal(err)
			}
		case reason := <-srv.startReq:
			if err := srv.startChildIfNeeded(reason); err != nil {
				logger.Fatal(err)
			}
		case err := <-srv.startErr:
			logger.Print(err)
		case err := <-srv.watchdogErr:
			if err := srv.handleWatchdogTimeout(err); err != nil {
				logger.Fatal(err)
			}
		}
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.serviceName, "service", "service", "logical service name for logs")
	flag.StringVar(&cfg.serviceType, "service-type", "simple", "service type: simple, exec, forking, oneshot, notify")
	flag.StringVar(&cfg.command, "command", "", "backend command launched under /bin/sh -c")
	flag.StringVar(&cfg.stopCommand, "stop-command", "", "optional stop command launched under /bin/sh -c with MAINPID set")
	flag.BoolVar(&cfg.userMode, "user", false, "publish runtime events to the user-mode control plane")
	flag.BoolVar(&cfg.verbose, "verbose", false, "enable backend launch logging")
	flag.IntVar(&cfg.readyFD, "ready-fd", 1, "file descriptor used for readiness message, 1 for stdout, 2 for stderr")
	flag.BoolVar(&cfg.startNow, "start-now", false, "start backend immediately after activation sockets are prepared")
	flag.StringVar(&cfg.stateFile, "state-file", "", "path to a runtime state file written by sys-notifyd")
	flag.StringVar(&cfg.mode, "socket-mode", "0660", "unix socket file mode in octal")
	flag.StringVar(&cfg.socketUser, "socket-user", "", "owner for unix activation sockets")
	flag.StringVar(&cfg.socketGroup, "socket-group", "", "group for unix activation sockets")
	flag.DurationVar(&cfg.readyTimeout, "ready-timeout", 30*time.Second, "maximum time to wait for READY=1 on notify services")
	flag.DurationVar(&cfg.stopTimeout, "stop-timeout", 15*time.Second, "maximum time to wait for service stop before escalation")
	flag.StringVar(&cfg.killSignal, "kill-signal", "TERM", "signal used to stop the main process after ExecStop or when no stop command is set")
	flag.BoolVar(&cfg.notify, "notify", false, "create NOTIFY_SOCKET and receive sd_notify messages")
	flag.StringVar(&cfg.notifyPath, "notify-path", "", "unixgram socket path for sd_notify messages")
	flag.Var(&cfg.listens, "listen", "activation socket in the form network:address, may be repeated")
	flag.Var(&cfg.fdNames, "fdname", "activation fd name, may be repeated")
	flag.Parse()

	if cfg.readyFD != 1 && cfg.readyFD != 2 {
		fmt.Fprintln(os.Stderr, "-ready-fd must be 1 or 2")
		os.Exit(2)
	}
	if cfg.startNow && cfg.command == "" {
		fmt.Fprintln(os.Stderr, "-command is required when -start-now is set")
		os.Exit(2)
	}
	if strings.EqualFold(cfg.serviceType, "notify") {
		cfg.notify = true
	}
	if cfg.notify && cfg.notifyPath == "" {
		cfg.notifyPath = filepath.Join(os.TempDir(), cfg.serviceName+".notify.sock")
	}
	return cfg
}

func newServer(cfg config, logger *log.Logger) (*server, error) {
	srv := &server{
		cfg:         cfg,
		logger:      logger,
		cmdExit:     make(chan childExitEvent, 4),
		status:      "starting",
		phase:       "starting",
		childState:  "idle",
		epollFD:     -1,
		extendStart: make(chan time.Duration, 8),
		extendStop:  make(chan time.Duration, 8),
		watchdogSet: make(chan time.Duration, 4),
		watchdogHit: make(chan struct{}, 16),
		watchdogErr: make(chan error, 1),
		startReq:    make(chan string, 8),
		startErr:    make(chan error, 4),
		shutdownCh:  make(chan struct{}),
		debug:       newStartupDebugger(),
	}

	for _, spec := range cfg.listens {
		sock, err := openActivationSocket(spec, cfg.mode, cfg.socketUser, cfg.socketGroup)
		if err != nil {
			return nil, err
		}
		srv.sockets = append(srv.sockets, sock)
	}

	if cfg.notify {
		if err := srv.openNotifySocket(); err != nil {
			return nil, err
		}
		go srv.readNotify()
	}

	go srv.watchdogLoop()
	srv.writeState()

	return srv, nil
}

func (s *server) armActivation() error {
	if len(s.sockets) == 0 || s.cfg.command == "" || s.cfg.startNow {
		return nil
	}
	epollFD, err := syscall.EpollCreate1(0)
	if err != nil {
		return fmt.Errorf("create epoll instance: %w", err)
	}
	s.epollFD = epollFD
	for _, sock := range s.sockets {
		event := &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(sock.file.Fd())}
		if err := syscall.EpollCtl(epollFD, syscall.EPOLL_CTL_ADD, int(sock.file.Fd()), event); err != nil {
			return fmt.Errorf("watch activation socket %s: %w", sock.describe, err)
		}
	}
	go s.activationLoop()
	return nil
}

func (s *server) activationLoop() {
	events := make([]syscall.EpollEvent, len(s.sockets))
	for {
		n, err := syscall.EpollWait(s.epollFD, events, -1)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			select {
			case <-s.shutdownCh:
				return
			case s.startErr <- fmt.Errorf("activation loop failed: %w", err):
			default:
			}
			return
		}
		if n == 0 {
			continue
		}
		s.tryQueueStart("incoming traffic")
	}
}

func openActivationSocket(spec string, mode string, socketUser string, socketGroup string) (activationSocket, error) {
	network, address, err := splitListenSpec(spec)
	if err != nil {
		return activationSocket{}, err
	}
	parsedMode := os.FileMode(0660)
	if mode != "" {
		if value, parseErr := parseFileMode(mode); parseErr == nil {
			parsedMode = value
		}
	}

	if isPacketNetwork(network) {
		if isUnixNetwork(network) {
			if err := prepareUnixSocketPath(address); err != nil {
				return activationSocket{}, err
			}
		}
		pc, err := net.ListenPacket(network, address)
		if err != nil {
			return activationSocket{}, fmt.Errorf("listen on %s %s: %w", network, address, err)
		}
		file, err := packetConnFile(pc)
		if err != nil {
			_ = pc.Close()
			return activationSocket{}, err
		}
		if isUnixNetwork(network) {
			if err := os.Chmod(address, parsedMode); err != nil {
				_ = file.Close()
				_ = pc.Close()
				return activationSocket{}, fmt.Errorf("chmod unix socket: %w", err)
			}
			if err := applySocketOwnership(address, socketUser, socketGroup); err != nil {
				_ = file.Close()
				_ = pc.Close()
				return activationSocket{}, err
			}
		}
		return activationSocket{file: file, closer: pc.Close, cleanup: func() {
			if isUnixNetwork(network) {
				_ = os.Remove(address)
			}
		}, describe: spec, unixPath: address, packet: true, netType: network, addr: address}, nil
	}

	if isUnixNetwork(network) {
		if err := prepareUnixSocketPath(address); err != nil {
			return activationSocket{}, err
		}
	}
	ln, err := net.Listen(network, address)
	if err != nil {
		return activationSocket{}, fmt.Errorf("listen on %s %s: %w", network, address, err)
	}
	file, err := listenerFile(ln)
	if err != nil {
		_ = ln.Close()
		return activationSocket{}, err
	}
	if isUnixNetwork(network) {
		if err := os.Chmod(address, parsedMode); err != nil {
			_ = file.Close()
			_ = ln.Close()
			return activationSocket{}, fmt.Errorf("chmod unix socket: %w", err)
		}
		if err := applySocketOwnership(address, socketUser, socketGroup); err != nil {
			_ = file.Close()
			_ = ln.Close()
			return activationSocket{}, err
		}
	}
	return activationSocket{file: file, closer: ln.Close, cleanup: func() {
		if isUnixNetwork(network) {
			_ = os.Remove(address)
		}
	}, describe: spec, unixPath: address, netType: network, addr: address}, nil
}

func applySocketOwnership(path string, socketUser string, socketGroup string) error {
	if socketUser == "" && socketGroup == "" {
		return nil
	}
	uid := -1
	gid := -1
	if socketUser != "" {
		u, err := user.Lookup(socketUser)
		if err != nil {
			return fmt.Errorf("lookup socket user %q: %w", socketUser, err)
		}
		parsedUID, err := strconv.Atoi(u.Uid)
		if err != nil {
			return fmt.Errorf("parse uid for %q: %w", socketUser, err)
		}
		uid = parsedUID
	}
	if socketGroup != "" {
		g, err := user.LookupGroup(socketGroup)
		if err != nil {
			return fmt.Errorf("lookup socket group %q: %w", socketGroup, err)
		}
		parsedGID, err := strconv.Atoi(g.Gid)
		if err != nil {
			return fmt.Errorf("parse gid for %q: %w", socketGroup, err)
		}
		gid = parsedGID
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("chown unix socket: %w", err)
	}
	return nil
}

func parseFileMode(mode string) (os.FileMode, error) {
	var value uint32
	_, err := fmt.Sscanf(mode, "%o", &value)
	if err != nil {
		return 0, fmt.Errorf("invalid socket mode %q", mode)
	}
	return os.FileMode(value), nil
}

func splitListenSpec(spec string) (string, string, error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid listen spec %q", spec)
	}
	return parts[0], parts[1], nil
}

func isPacketNetwork(network string) bool {
	return network == "udp" || network == "udp4" || network == "udp6" || network == "unixgram"
}

func isUnixNetwork(network string) bool {
	return network == "unix" || network == "unixgram"
}

func prepareUnixSocketPath(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create unix socket directory: %w", err)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect unix socket path: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("existing path is not a socket: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale unix socket: %w", err)
	}
	return nil
}

func (s *server) openNotifySocket() error {
	if err := prepareUnixSocketPath(s.cfg.notifyPath); err != nil {
		return err
	}
	addr := &net.UnixAddr{Name: s.cfg.notifyPath, Net: "unixgram"}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return fmt.Errorf("listen notify socket: %w", err)
	}
	if err := s.ensureNotifySocketOwnership(s.cfg.notifyPath); err != nil {
		conn.Close()
		return err
	}
	s.notifyConn = conn
	return nil
}

func (s *server) ensureNotifySocketOwnership(path string) error {
	if err := os.Chmod(path, 0o660); err != nil {
		return fmt.Errorf("chmod notify socket: %w", err)
	}
	if err := applySocketOwnership(path, s.cfg.socketUser, s.cfg.socketGroup); err != nil {
		return err
	}
	return nil
}

func (s *server) startChild() error {
	s.mu.Lock()
	if s.cmd != nil {
		s.mu.Unlock()
		return fmt.Errorf("backend already running")
	}
	drainDurations(s.extendStart)
	drainDurations(s.extendStop)
	drainSignals(s.watchdogHit)
	s.trySendDuration(s.watchdogSet, 0)
	ready := make(chan struct{})
	seq := atomic.AddUint64(&s.childReadyN, 1)
	cmdDone := make(chan struct{})
	s.childReady = ready
	s.cmdDone = cmdDone
	s.childState = "starting"
	s.cmdErr = nil
	s.failureText = ""
	s.status = "child-started"
	s.phase = "starting"
	s.mu.Unlock()
	s.writeState()

	cmd := exec.Command("/bin/sh", "-c", s.buildShellCommand())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = nil
	cmd.ExtraFiles = make([]*os.File, 0, len(s.sockets))
	for _, sock := range s.sockets {
		cmd.ExtraFiles = append(cmd.ExtraFiles, sock.file)
	}
	if s.cfg.verbose {
		s.logger.Printf("starting backend: %s", s.cfg.command)
	}
	if err := cmd.Start(); err != nil {
		s.mu.Lock()
		s.cmdDone = nil
		s.childReady = nil
		s.childState = "idle"
		s.resetForIdleLocked()
		s.mu.Unlock()
		s.writeState()
		return fmt.Errorf("start backend: %w", err)
	}
	s.debug.event("notifyd.exec-child", map[string]any{"service": s.cfg.serviceName, "service_type": s.cfg.serviceType, "pid": cmd.Process.Pid, "command": s.cfg.command, "user_mode": s.cfg.userMode})

	s.mu.Lock()
	s.cmd = cmd
	s.mainPID = cmd.Process.Pid
	s.mu.Unlock()
	s.writeState()

	if !strings.EqualFold(s.cfg.serviceType, "notify") {
		s.markChildReady(seq)
	}
	go s.monitorChildStartup(seq, ready, cmdDone)
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		tracked := s.cmd == cmd
		if tracked {
			s.cmdErr = err
			s.resetForIdleLocked()
			close(cmdDone)
		}
		s.mu.Unlock()
		if tracked {
			s.writeState()
		}
		s.cmdExit <- childExitEvent{err: err}
	}()
	return nil
}

func (s *server) buildShellCommand() string {
	parts := []string{
		fmt.Sprintf("LISTEN_FDS=%d", len(s.sockets)),
		"LISTEN_PID=$$",
		"export LISTEN_FDS LISTEN_PID",
	}
	if names := s.effectiveFDNames(); len(names) > 0 {
		parts = append(parts, fmt.Sprintf("LISTEN_FDNAMES=%s", shellEscape(strings.Join(names, ":"))), "export LISTEN_FDNAMES")
	}
	if s.cfg.notify {
		parts = append(parts, fmt.Sprintf("NOTIFY_SOCKET=%s", shellEscape(s.cfg.notifyPath)), "export NOTIFY_SOCKET")
	}
	parts = append(parts, "exec "+s.cfg.command)
	return strings.Join(parts, "; ")
}

func (s *server) effectiveFDNames() []string {
	if len(s.cfg.fdNames) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.sockets))
	for i := range s.sockets {
		if i < len(s.cfg.fdNames) {
			names = append(names, s.cfg.fdNames[i])
		} else {
			names = append(names, fmt.Sprintf("fd%d", i))
		}
	}
	return names
}

func shellEscape(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func (s *server) waitUntilReady() error {
	if s.cfg.command == "" {
		return nil
	}
	if !s.cfg.startNow {
		s.status = "listening"
		s.setPhase("ready")
		s.writeState()
		return nil
	}

	ready, cmdDone, seq := s.currentChildWatchers()
	if cmdDone == nil {
		return nil
	}
	if !strings.EqualFold(s.cfg.serviceType, "notify") {
		s.markChildReady(seq)
		return nil
	}
	return s.waitForReady(seq, ready, cmdDone)
}

func (s *server) waitForReady(seq uint64, ready <-chan struct{}, cmdDone <-chan struct{}) error {
	deadline := time.NewTimer(s.cfg.readyTimeout)
	defer deadline.Stop()
	for {
		select {
		case <-ready:
			s.markChildReady(seq)
			return nil
		case <-cmdDone:
			err := s.currentChildErr()
			if err == nil {
				return fmt.Errorf("backend exited before READY=1")
			}
			return fmt.Errorf("backend exited before READY=1: %w", err)
		case extend := <-s.extendStart:
			if !deadline.Stop() {
				select {
				case <-deadline.C:
				default:
				}
			}
			deadline.Reset(extend)
			s.logger.Printf("extended start timeout by %s", extend)
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for READY=1 after %s", s.cfg.readyTimeout)
		}
	}
}

func (s *server) emitReady() {
	fd := os.Stdout
	if s.cfg.readyFD == 2 {
		fd = os.Stderr
	}
	_, _ = fd.Write([]byte("READY=1\n"))
}

func (s *server) readNotify() {
	buf := make([]byte, 4096)
	for {
		n, _, err := s.notifyConn.ReadFromUnix(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.logger.Printf("notify socket read error: %v", err)
			return
		}
		payload := string(buf[:n])
		for _, line := range strings.Split(payload, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			s.handleNotifyLine(line)
		}
	}
}

func (s *server) handleNotifyLine(line string) {
	switch {
	case line == "READY=1":
		if s.currentPhase() == "reloading" {
			s.logger.Printf("reload complete")
		}
		s.markChildReady(atomic.LoadUint64(&s.childReadyN))
	case strings.HasPrefix(line, "STATUS="):
		s.status = strings.TrimPrefix(line, "STATUS=")
		s.logger.Printf("status: %s", s.status)
		s.writeState()
	case strings.HasPrefix(line, "MAINPID="):
		pidValue := strings.TrimPrefix(line, "MAINPID=")
		pid, err := strconv.Atoi(pidValue)
		if err != nil {
			s.logger.Printf("invalid mainpid: %s", pidValue)
			return
		}
		s.mainPID = pid
		s.logger.Printf("mainpid: %d", s.mainPID)
		s.writeState()
	case strings.HasPrefix(line, "ERRNO="):
		s.failureText = strings.TrimPrefix(line, "ERRNO=")
		s.logger.Printf("notify errno: %s", s.failureText)
		s.writeState()
	case line == "RELOADING=1":
		s.setPhase("reloading")
		s.logger.Printf("backend entered reload state")
		s.writeState()
	case line == "STOPPING=1":
		s.setPhase("stopping")
		s.logger.Printf("backend requested stop")
		s.writeState()
	case strings.HasPrefix(line, "EXTEND_TIMEOUT_USEC="):
		usec, err := strconv.ParseUint(strings.TrimPrefix(line, "EXTEND_TIMEOUT_USEC="), 10, 64)
		if err != nil {
			s.logger.Printf("invalid extend timeout: %s", strings.TrimPrefix(line, "EXTEND_TIMEOUT_USEC="))
			return
		}
		s.handleExtendTimeout(time.Duration(usec) * time.Microsecond)
	case strings.HasPrefix(line, "WATCHDOG_USEC="):
		usec, err := strconv.ParseUint(strings.TrimPrefix(line, "WATCHDOG_USEC="), 10, 64)
		if err != nil {
			s.logger.Printf("invalid watchdog timeout: %s", strings.TrimPrefix(line, "WATCHDOG_USEC="))
			return
		}
		duration := time.Duration(usec) * time.Microsecond
		s.logger.Printf("watchdog interval set to %s", duration)
		s.trySendDuration(s.watchdogSet, duration)
	case line == "WATCHDOG=1":
		s.trySendSignal(s.watchdogHit)
	}
}

func (s *server) handleExtendTimeout(duration time.Duration) {
	switch s.currentPhase() {
	case "starting", "reloading":
		s.trySendDuration(s.extendStart, duration)
	case "stopping":
		s.trySendDuration(s.extendStop, duration)
	}
}

func (s *server) trySendDuration(ch chan time.Duration, value time.Duration) {
	select {
	case ch <- value:
	default:
	}
}

func (s *server) trySendSignal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func drainDurations(ch chan time.Duration) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func drainSignals(ch chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func (s *server) currentChildWatchers() (chan struct{}, chan struct{}, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.childReady, s.cmdDone, atomic.LoadUint64(&s.childReadyN)
}

func (s *server) currentChildErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmdErr
}

func (s *server) markChildReady(seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq != atomic.LoadUint64(&s.childReadyN) {
		return
	}
	if s.childReady != nil {
		select {
		case <-s.childReady:
		default:
			close(s.childReady)
		}
	}
	s.childState = "running"
	if s.cfg.startNow || len(s.sockets) == 0 {
		s.status = "ready"
	}
	s.phase = "ready"
	if s.cmd != nil && s.mainPID == 0 {
		s.mainPID = s.cmd.Process.Pid
	}
	if s.cmd != nil && s.mainPID > 0 {
		s.logger.Printf("mainpid: %d", s.mainPID)
	}
	s.debug.event("notifyd.ready", map[string]any{"service": s.cfg.serviceName, "service_type": s.cfg.serviceType, "main_pid": s.mainPID, "status": s.status, "phase": s.phase, "user_mode": s.cfg.userMode})
	s.writeStateLocked()
}

func (s *server) monitorChildStartup(seq uint64, ready <-chan struct{}, cmdDone <-chan struct{}) {
	if !strings.EqualFold(s.cfg.serviceType, "notify") {
		return
	}
	if err := s.waitForReady(seq, ready, cmdDone); err != nil {
		if s.isLazySocketService() {
			select {
			case s.startErr <- err:
			default:
			}
			if killErr := s.abortChild(err); killErr != nil {
				select {
				case s.startErr <- killErr:
				default:
				}
			}
		}
	}
}

func (s *server) isLazySocketService() bool {
	return len(s.sockets) > 0 && !s.cfg.startNow
}

func (s *server) tryQueueStart(reason string) {
	if s.cfg.command == "" {
		return
	}
	select {
	case s.startReq <- reason:
	default:
	}
}

func (s *server) startChildIfNeeded(reason string) error {
	s.mu.Lock()
	busy := s.cmd != nil || s.childState == "starting" || s.childState == "stopping"
	s.mu.Unlock()
	if busy {
		return nil
	}
	s.logger.Printf("activation trigger: %s", reason)
	s.writeState()
	return s.startChild()
}

func (s *server) handleChildExit(err error) error {
	if s.isLazySocketService() {
		if err != nil {
			s.logger.Printf("backend exited: %v", err)
			s.debug.event("notifyd.child-exit", map[string]any{"service": s.cfg.serviceName, "ok": false, "error": err.Error(), "lazy": true, "user_mode": s.cfg.userMode})
		} else {
			s.logger.Printf("backend exited, sockets remain armed")
			s.debug.event("notifyd.child-exit", map[string]any{"service": s.cfg.serviceName, "ok": true, "lazy": true, "user_mode": s.cfg.userMode})
		}
		s.writeState()
		return nil
	}
	if err != nil {
		s.debug.event("notifyd.child-exit", map[string]any{"service": s.cfg.serviceName, "ok": false, "error": err.Error(), "lazy": false, "user_mode": s.cfg.userMode})
		s.writeState()
		return err
	}
	s.debug.event("notifyd.child-exit", map[string]any{"service": s.cfg.serviceName, "ok": true, "lazy": false, "user_mode": s.cfg.userMode})
	s.writeState()
	return fmt.Errorf("backend exited")
}

func (s *server) handleWatchdogTimeout(err error) error {
	if s.isLazySocketService() {
		return s.abortChild(err)
	}
	return s.abortChild(err)
}

func (s *server) resetForIdleLocked() {
	s.cmd = nil
	s.cmdDone = nil
	s.childReady = nil
	s.mainPID = 0
	s.childState = "idle"
	s.failureText = ""
	s.trySendDuration(s.watchdogSet, 0)
	if s.isLazySocketService() {
		s.status = "listening"
		s.phase = "ready"
	} else {
		s.phase = "stopped"
	}
}

func (s *server) setPhase(phase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = phase
}

func (s *server) currentPhase() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase
}

func (s *server) stopChild() error {
	s.mu.Lock()
	cmdDone := s.cmdDone
	hasCmd := s.cmd != nil
	if hasCmd {
		s.childState = "stopping"
	}
	s.mu.Unlock()
	if !hasCmd {
		return nil
	}
	s.setPhase("stopping")
	s.writeState()
	if s.cfg.stopCommand != "" {
		if err := s.runStopCommand(); err != nil {
			return err
		}
	} else {
		if err := s.signalMainProcess(s.cfg.killSignal); err != nil {
			return err
		}
	}

	if s.waitForExit(cmdDone, s.cfg.stopTimeout) {
		return nil
	}

	s.logger.Printf("stop timeout exceeded, escalating to SIGKILL")
	if err := s.signalMainProcess("KILL"); err != nil {
		return err
	}
	if cmdDone != nil {
		<-cmdDone
	}
	return nil
}

func (s *server) runStopCommand() error {
	if s.cfg.verbose {
		s.logger.Printf("running stop command: %s", s.cfg.stopCommand)
	}
	cmd := exec.Command("/bin/sh", "-c", s.cfg.stopCommand)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), fmt.Sprintf("MAINPID=%d", s.mainPID))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run stop command: %w", err)
	}
	return nil
}

func (s *server) signalMainProcess(name string) error {
	if s.mainPID <= 0 {
		return fmt.Errorf("main pid unavailable for signal %s", name)
	}
	sig, err := parseSignal(name)
	if err != nil {
		return err
	}
	if err := syscall.Kill(s.mainPID, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal main process %d with %s: %w", s.mainPID, name, err)
	}
	return nil
}

func parseSignal(name string) (syscall.Signal, error) {
	clean := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(name)), "SIG")
	switch clean {
	case "TERM":
		return syscall.SIGTERM, nil
	case "INT":
		return syscall.SIGINT, nil
	case "QUIT":
		return syscall.SIGQUIT, nil
	case "HUP":
		return syscall.SIGHUP, nil
	case "KILL":
		return syscall.SIGKILL, nil
	default:
		return 0, fmt.Errorf("unsupported signal %q", name)
	}
}

func (s *server) waitForExit(cmdDone <-chan struct{}, timeout time.Duration) bool {
	if cmdDone == nil {
		return true
	}
	if timeout <= 0 {
		<-cmdDone
		return true
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-cmdDone:
			return true
		case extend := <-s.extendStop:
			if !deadline.Stop() {
				select {
				case <-deadline.C:
				default:
				}
			}
			deadline.Reset(extend)
			s.logger.Printf("extended stop timeout by %s", extend)
		case <-deadline.C:
			return false
		}
	}
}

func (s *server) watchdogLoop() {
	var (
		interval time.Duration
		timer    *time.Timer
		fired    <-chan time.Time
	)
	stopTimer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		fired = nil
	}
	resetTimer := func() {
		if interval <= 0 {
			stopTimer()
			return
		}
		if timer == nil {
			timer = time.NewTimer(interval)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(interval)
		}
		fired = timer.C
	}
	for {
		select {
		case <-s.shutdownCh:
			stopTimer()
			return
		case dur := <-s.watchdogSet:
			interval = dur
			resetTimer()
		case <-s.watchdogHit:
			if interval > 0 {
				resetTimer()
			}
		case <-fired:
			select {
			case s.watchdogErr <- fmt.Errorf("watchdog timeout after %s", interval):
			default:
			}
			stopTimer()
		}
	}
}

func (s *server) abortChild(cause error) error {
	s.logger.Printf("%v", cause)
	s.mu.Lock()
	cmdDone := s.cmdDone
	hasCmd := s.cmd != nil
	s.mu.Unlock()
	if !hasCmd {
		return cause
	}
	if err := s.signalMainProcess("KILL"); err != nil {
		return err
	}
	if cmdDone != nil {
		<-cmdDone
	}
	return cause
}

func listenerFile(ln net.Listener) (*os.File, error) {
	type fileListener interface {
		File() (*os.File, error)
	}
	fl, ok := ln.(fileListener)
	if !ok {
		return nil, fmt.Errorf("listener type %T does not expose File", ln)
	}
	file, err := fl.File()
	if err != nil {
		return nil, fmt.Errorf("copy listener file descriptor: %w", err)
	}
	return file, nil
}

func packetConnFile(pc net.PacketConn) (*os.File, error) {
	type filePacketConn interface {
		File() (*os.File, error)
	}
	fpc, ok := pc.(filePacketConn)
	if !ok {
		return nil, fmt.Errorf("packet listener type %T does not expose File", pc)
	}
	file, err := fpc.File()
	if err != nil {
		return nil, fmt.Errorf("copy packet socket file descriptor: %w", err)
	}
	return file, nil
}

func (s *server) cleanup() {
	close(s.shutdownCh)
	if s.cfg.stateFile != "" {
		_ = os.Remove(s.cfg.stateFile)
	}
	if s.epollFD >= 0 {
		_ = syscall.Close(s.epollFD)
	}
	if s.notifyConn != nil {
		_ = s.notifyConn.Close()
		_ = os.Remove(s.cfg.notifyPath)
	}
	for _, sock := range s.sockets {
		if sock.closer != nil {
			_ = sock.closer()
		}
		if sock.file != nil {
			_ = sock.file.Close()
		}
		if sock.cleanup != nil {
			sock.cleanup()
		}
	}
}

func sanitizeStateValue(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.TrimSpace(value)
}

func (s *server) writeState() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeStateLocked()
}

func (s *server) writeStateLocked() {
	if s.cfg.stateFile == "" {
		s.publishVisionEventLocked("state")
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.stateFile), 0755); err != nil {
		s.publishVisionEventLocked("state")
		return
	}
	lines := []string{
		"service=" + sanitizeStateValue(s.cfg.serviceName),
		"service_type=" + sanitizeStateValue(s.cfg.serviceType),
		"phase=" + sanitizeStateValue(s.phase),
		"child_state=" + sanitizeStateValue(s.childState),
		"status=" + sanitizeStateValue(s.status),
		"failure=" + sanitizeStateValue(s.failureText),
		"main_pid=" + strconv.Itoa(s.mainPID),
		"socket_count=" + strconv.Itoa(len(s.sockets)),
	}
	tmpPath := s.cfg.stateFile + ".tmp"
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
		s.publishVisionEventLocked("state")
		return
	}
	_ = os.Rename(tmpPath, s.cfg.stateFile)
	s.publishVisionEventLocked("state")
}

func (s *server) publishVisionEventLocked(event string) {
	payload := map[string]string{
		"event":        sanitizeStateValue(event),
		"service_type": sanitizeStateValue(s.cfg.serviceType),
		"phase":        sanitizeStateValue(s.phase),
		"child_state":  sanitizeStateValue(s.childState),
		"status":       sanitizeStateValue(s.status),
		"failure":      sanitizeStateValue(s.failureText),
		"main_pid":     strconv.Itoa(s.mainPID),
		"socket_count": strconv.Itoa(len(s.sockets)),
	}
	envelope := visionapi.NewEvent(visionapi.ModeForUser(s.cfg.userMode), visionapi.SourceSysNotifyd, visionapi.KindUnitRuntime, s.cfg.serviceName, payload)
	data, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	addr := &net.UnixAddr{Name: visionapi.SysvisionIngressSocketPath(s.cfg.userMode, os.Getenv("XDG_RUNTIME_DIR")), Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write(data)
}
