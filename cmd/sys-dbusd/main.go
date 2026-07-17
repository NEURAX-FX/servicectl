package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"servicectl/internal/dbusactivation"
	"servicectl/internal/dbusmanager"
)

type pathList []string

func (p *pathList) String() string { return strings.Join(*p, ",") }
func (p *pathList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("path is empty")
	}
	*p = append(*p, value)
	return nil
}

type daemonConfig struct {
	controlPath       string
	systemConfig      string
	serviceDirs       pathList
	systemdPaths      pathList
	helperPath        string
	daemonPID         int32
	adminPath         string
	servicectlPath    string
	dinitctlPath      string
	managedRuntimeDir string
	busAddress        string
	activationTimeout time.Duration
	stateFile         string
	verbose           bool
}

func defaultConfig() daemonConfig {
	return daemonConfig{
		controlPath:       "/run/servicectl/sys-dbusd/control.sock",
		systemConfig:      "/usr/share/dbus-1/system.conf",
		serviceDirs:       pathList{"/etc/dbus-1/system-services", "/run/dbus-1/system-services", "/usr/local/share/dbus-1/system-services", "/usr/share/dbus-1/system-services", "/lib/dbus-1/system-services"},
		systemdPaths:      pathList{"/etc/systemd/system", "/run/systemd/system", "/usr/local/lib/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"},
		helperPath:        "/usr/libexec/servicectl/sys-dbusd-daemon-helper",
		adminPath:         "/usr/bin/servicectl",
		servicectlPath:    "/usr/bin/servicectl",
		dinitctlPath:      "/usr/bin/dinitctl",
		managedRuntimeDir: "/run/servicectl/managed",
		activationTimeout: 30 * time.Second,
		stateFile:         "/run/servicectl/sys-dbusd/state.json",
	}
}

func parseConfig(args []string) (daemonConfig, error) {
	cfg := defaultConfig()
	serviceDirsSet := false
	systemdPathsSet := false
	fs := flag.NewFlagSet("sys-dbusd", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.controlPath, "control-path", cfg.controlPath, "Unix seqpacket control socket")
	fs.StringVar(&cfg.systemConfig, "system-config", cfg.systemConfig, "system bus configuration")
	fs.Var(pathListValue{target: &cfg.serviceDirs, touched: &serviceDirsSet}, "service-dir", "D-Bus system service directory, repeatable")
	fs.Var(pathListValue{target: &cfg.systemdPaths, touched: &systemdPathsSet}, "systemd-path", "systemd unit search path, repeatable")
	fs.StringVar(&cfg.helperPath, "helper-path", cfg.helperPath, "authorized dbus-daemon helper executable")
	fs.StringVar(&cfg.adminPath, "admin-path", cfg.adminPath, "authorized administrative client executable")
	fs.StringVar(&cfg.servicectlPath, "servicectl-path", cfg.servicectlPath, "servicectl executable")
	fs.StringVar(&cfg.dinitctlPath, "dinitctl-path", cfg.dinitctlPath, "dinitctl executable")
	fs.StringVar(&cfg.managedRuntimeDir, "managed-runtime-dir", cfg.managedRuntimeDir, "managed runtime root")
	fs.StringVar(&cfg.busAddress, "bus-address", "", "explicit D-Bus address instead of the system bus")
	fs.DurationVar(&cfg.activationTimeout, "activation-timeout", cfg.activationTimeout, "maximum time to acquire a D-Bus name")
	fs.StringVar(&cfg.stateFile, "state-file", cfg.stateFile, "runtime status file")
	fs.BoolVar(&cfg.verbose, "verbose", false, "enable verbose logs")
	if err := fs.Parse(args); err != nil {
		return daemonConfig{}, err
	}
	if fs.NArg() != 0 {
		return daemonConfig{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if serviceDirsSet && len(cfg.serviceDirs) == 0 || systemdPathsSet && len(cfg.systemdPaths) == 0 {
		return daemonConfig{}, errors.New("search path list is empty")
	}
	if cfg.activationTimeout <= 0 {
		return daemonConfig{}, errors.New("activation timeout must be positive")
	}
	if cfg.controlPath == "" || cfg.servicectlPath == "" || cfg.dinitctlPath == "" || cfg.managedRuntimeDir == "" {
		return daemonConfig{}, errors.New("required path is empty")
	}
	return cfg, nil
}

type pathListValue struct {
	target  *pathList
	touched *bool
}

func (v pathListValue) String() string { return v.target.String() }
func (v pathListValue) Set(value string) error {
	if !*v.touched {
		*v.target = nil
		*v.touched = true
	}
	return v.target.Set(value)
}

type indexStore struct {
	mu     sync.RWMutex
	index  *dbusactivation.Index
	errors []string
	gen    uint64
}

func (s *indexStore) load(directories []string) error {
	index, buildErrors := dbusactivation.BuildIndex(directories)
	errorsText := make([]string, 0, len(buildErrors))
	for _, err := range buildErrors {
		errorsText = append(errorsText, err.Error())
	}
	s.mu.Lock()
	s.errors = errorsText
	if len(buildErrors) == 0 {
		s.index = index
		s.gen++
	}
	s.mu.Unlock()
	if len(buildErrors) != 0 {
		return errors.Join(buildErrors...)
	}
	return nil
}

func (s *indexStore) get() *dbusactivation.Index {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.index
}

func (s *indexStore) status() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	services := 0
	if s.index != nil {
		services = len(s.index.Names())
	}
	errorsText := append([]string{}, s.errors...)
	payload, _ := json.Marshal(map[string]any{
		"healthy":    s.index != nil,
		"generation": s.gen,
		"services":   services,
		"errors":     errorsText,
	})
	return payload
}

type executableIdentity struct {
	device uint64
	inode  uint64
}

func fileIdentity(path string) (executableIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return executableIdentity{}, err
	}
	if !info.Mode().IsRegular() {
		return executableIdentity{}, fmt.Errorf("executable is not a regular file: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return executableIdentity{}, fmt.Errorf("unsupported executable stat information: %s", path)
	}
	return executableIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func peerIdentity(peer dbusactivation.Peer) (executableIdentity, error) {
	if peer.Device != 0 || peer.Inode != 0 {
		return executableIdentity{device: peer.Device, inode: peer.Inode}, nil
	}
	return fileIdentity(peer.Executable)
}

func peerAuthorizer(cfg daemonConfig) (func(dbusactivation.Frontend, dbusactivation.Peer) error, error) {
	helperIdentity, err := fileIdentity(cfg.helperPath)
	if err != nil {
		return nil, fmt.Errorf("inspect daemon helper: %w", err)
	}
	return func(frontend dbusactivation.Frontend, peer dbusactivation.Peer) error {
		var expected string
		switch frontend {
		case dbusactivation.FrontendDaemonHelper:
			if peer.UID != 0 {
				return fmt.Errorf("daemon helper uid %d is not root", peer.UID)
			}
			if cfg.daemonPID <= 0 || !containsPID(peer.Ancestors, cfg.daemonPID) {
				return fmt.Errorf("daemon helper ancestry %v does not contain bus daemon pid %d", peer.Ancestors, cfg.daemonPID)
			}
			expected = cfg.helperPath
		case dbusactivation.FrontendAdmin:
			if peer.UID != 0 {
				return fmt.Errorf("admin uid %d is not root", peer.UID)
			}
			expected = cfg.adminPath
		default:
			return errors.New("unsupported frontend")
		}
		if filepath.Clean(peer.Executable) != filepath.Clean(expected) {
			return fmt.Errorf("peer executable %q does not match %q", peer.Executable, expected)
		}
		if frontend == dbusactivation.FrontendDaemonHelper {
			identity, err := peerIdentity(peer)
			if err != nil {
				return fmt.Errorf("inspect daemon helper peer: %w", err)
			}
			if identity != helperIdentity {
				return errors.New("daemon helper executable identity changed")
			}
		}
		return nil
	}, nil
}

func containsPID(pids []int32, expected int32) bool {
	for _, pid := range pids {
		if pid == expected {
			return true
		}
	}
	return false
}

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	logger := log.New(os.Stdout, "sys-dbusd: ", log.LstdFlags)
	if err := run(cfg, logger); err != nil {
		logger.Fatal(err)
	}
}

func run(cfg daemonConfig, logger *log.Logger) error {
	var bus *dbusmanager.Godbus
	var err error
	if cfg.busAddress != "" {
		bus, err = dbusmanager.NewBus(cfg.busAddress)
	} else {
		bus, err = dbusmanager.NewSystemBus()
	}
	if err != nil {
		return err
	}
	defer bus.Close()
	pidContext, cancelPID := context.WithTimeout(context.Background(), 5*time.Second)
	busPID, err := bus.GetConnectionUnixProcessID(pidContext, "org.freedesktop.DBus")
	cancelPID()
	if err != nil {
		return fmt.Errorf("resolve bus daemon pid: %w", err)
	}
	if busPID == 0 || uint64(busPID) > uint64(^uint32(0)>>1) {
		return fmt.Errorf("invalid bus daemon pid %d", busPID)
	}
	cfg.daemonPID = int32(busPID)
	store := &indexStore{}
	if err := store.load(cfg.serviceDirs); err != nil {
		return err
	}
	units := dbusactivation.NewSystemdUnitResolver(cfg.systemdPaths, cfg.managedRuntimeDir)
	resolver := dbusactivation.DefinitionResolver{Index: store.get, Units: units}
	environments := &dbusactivation.EnvironmentStore{}
	managed := dbusactivation.NewManagedStarter(dbusactivation.ManagedOptions{
		Install: func(ctx context.Context, unit string) error {
			return prepareManagedUnit(ctx, cfg.servicectlPath, unit)
		},
		Start: func(ctx context.Context, service string) error {
			return runCommand(ctx, cfg.dinitctlPath, "start", service)
		},
		Activate: dbusactivation.ActivateControl,
	})
	busAddress := cfg.busAddress
	if busAddress == "" {
		busAddress = "unix:path=/run/dbus/system_bus_socket"
	}
	native := dbusactivation.NewNativeStarter(dbusactivation.NativeOptions{BusAddress: busAddress})
	engine := dbusactivation.NewEngine(dbusactivation.EngineOptions{
		Monitor:     dbusactivation.NewGodbusMonitor(bus),
		Resolver:    resolver,
		Starter:     dbusactivation.CompositeStarter{Managed: managed, Native: native},
		Environment: environments,
		Timeout:     cfg.activationTimeout,
	})
	authorize, err := peerAuthorizer(cfg)
	if err != nil {
		return err
	}
	server := dbusactivation.NewServer(dbusactivation.ServerOptions{
		Path:        cfg.controlPath,
		Mode:        0o600,
		GID:         -1,
		Activator:   engine,
		Environment: environments,
		Authorize:   authorize,
		Reload: func() error {
			return store.load(cfg.serviceDirs)
		},
		Status: store.status,
	})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				if err := store.load(cfg.serviceDirs); err != nil {
					logger.Printf("reload failed: %v", err)
				} else if cfg.verbose {
					logger.Printf("service index reloaded")
				}
			}
		}
	}()
	if cfg.verbose {
		logger.Printf("listening on %s with %d service directories", cfg.controlPath, len(cfg.serviceDirs))
	}
	return server.Serve(ctx)
}

func prepareManagedUnit(ctx context.Context, servicectlPath, unit string) error {
	return runCommand(ctx, servicectlPath, "start", unit)
}

func runCommand(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, detail)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func managedControlPath(runtimeDir, serviceName string) string {
	return filepath.Join(runtimeDir, serviceName, "control.sock")
}
