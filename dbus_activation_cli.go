package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"servicectl/internal/dbusactivation"
)

var errDBusActivationChanged = errors.New("managed D-Bus activation file changed after enable")

var dbusActivationHelperPath = "/usr/libexec/servicectl/sys-dbusd-daemon-helper"

type dbusActivationPaths struct {
	ControlPath  string
	HelperPath   string
	DaemonConfig string
	ManifestPath string
}

type dbusActivationManifest struct {
	Version         int       `json:"version"`
	Backend         string    `json:"backend"`
	EnabledAt       time.Time `json:"enabled_at"`
	DaemonConfig    string    `json:"daemon_config"`
	CurrentHash     string    `json:"current_hash"`
	PreviousExists  bool      `json:"previous_exists"`
	PreviousMode    uint32    `json:"previous_mode,omitempty"`
	PreviousContent []byte    `json:"previous_content,omitempty"`
}

var (
	effectiveUID             = os.Geteuid
	pingDBusActivationCore   = defaultPingDBusActivationCore
	defaultActivationPathsFn = defaultDBusActivationPaths
	removeDBusActivationFile = os.Remove
)

func defaultDBusActivationPaths() dbusActivationPaths {
	return dbusActivationPaths{
		ControlPath:  "/run/servicectl/sys-dbusd/control.sock",
		HelperPath:   dbusActivationHelperPath,
		DaemonConfig: "/etc/dbus-1/system.d/50-servicectl-activation.conf",
		ManifestPath: "/var/lib/servicectl/dbus-activation/manifest.json",
	}
}

func dbusActivationCommand(args []string) int {
	if len(args) == 0 {
		fmt.Println("Usage: servicectl dbus-activation <check|enable|status|disable> [--backend=daemon]")
		return 1
	}
	backend := "daemon"
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.HasPrefix(arg, "--backend=") {
			backend = strings.TrimPrefix(arg, "--backend=")
			continue
		}
		filtered = append(filtered, arg)
	}
	if backend != "daemon" {
		fmt.Printf("Unsupported D-Bus activation backend: %s\n", backend)
		return 1
	}
	if len(filtered) != 1 {
		fmt.Println("Usage: servicectl dbus-activation <check|enable|status|disable> [--backend=daemon]")
		return 1
	}
	paths := defaultActivationPathsFn()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	var err error
	switch filtered[0] {
	case "check":
		err = checkDaemonActivation(ctx, paths, defaultDaemonPreflight)
	case "enable":
		err = enableDaemonActivation(ctx, paths, defaultDaemonPreflight)
		if err == nil {
			fmt.Println("D-Bus activation helper configured; reload or restart the system bus in a controlled maintenance window.")
		}
	case "status":
		err = printDBusActivationStatus(ctx, paths)
	case "disable":
		err = disableDBusActivation(paths)
		if err == nil {
			fmt.Println("D-Bus activation override restored; reload or restart the system bus in a controlled maintenance window.")
		}
	default:
		err = fmt.Errorf("unknown dbus-activation command %q", filtered[0])
	}
	if err != nil {
		fmt.Println(err)
		return 1
	}
	return 0
}

func checkDaemonActivation(ctx context.Context, paths dbusActivationPaths, checker func(context.Context, dbusActivationPaths) error) error {
	if checker == nil {
		return errors.New("D-Bus activation checker is not configured")
	}
	return checker(ctx, paths)
}

func enableDaemonActivation(ctx context.Context, paths dbusActivationPaths, checker func(context.Context, dbusActivationPaths) error) error {
	if err := checkDaemonActivation(ctx, paths, checker); err != nil {
		return err
	}
	if _, err := os.Lstat(paths.ManifestPath); err == nil {
		return fmt.Errorf("D-Bus activation is already enabled; manifest exists at %s", paths.ManifestPath)
	} else if !os.IsNotExist(err) {
		return err
	}

	manifest := dbusActivationManifest{
		Version:      1,
		Backend:      "daemon",
		EnabledAt:    time.Now().UTC(),
		DaemonConfig: paths.DaemonConfig,
	}
	if info, err := os.Lstat(paths.DaemonConfig); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular D-Bus activation config: %s", paths.DaemonConfig)
		}
		content, _, err := readRegularFile(paths.DaemonConfig)
		if err != nil {
			return err
		}
		manifest.PreviousExists = true
		manifest.PreviousMode = uint32(info.Mode().Perm())
		manifest.PreviousContent = content
	} else if !os.IsNotExist(err) {
		return err
	}

	content := daemonActivationConfig(paths.HelperPath)
	manifest.CurrentHash = hashBytes(content)
	if err := atomicWriteFile(paths.DaemonConfig, content, 0o644); err != nil {
		return err
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return rollbackDaemonConfig(paths, manifest, err)
	}
	manifestData = append(manifestData, '\n')
	if err := atomicWriteFile(paths.ManifestPath, manifestData, 0o600); err != nil {
		return rollbackDaemonConfig(paths, manifest, err)
	}
	return nil
}

func disableDBusActivation(paths dbusActivationPaths) error {
	manifest, err := readDBusActivationManifest(paths.ManifestPath)
	if err != nil {
		return err
	}
	if manifest.Backend != "daemon" || filepath.Clean(manifest.DaemonConfig) != filepath.Clean(paths.DaemonConfig) {
		return errors.New("D-Bus activation manifest does not describe the daemon backend")
	}
	content, info, err := readRegularFile(paths.DaemonConfig)
	if err != nil {
		return err
	}
	if hashBytes(content) != manifest.CurrentHash {
		return errDBusActivationChanged
	}
	managedContent := append([]byte(nil), content...)
	managedMode := info.Mode().Perm()
	if manifest.PreviousExists {
		mode := os.FileMode(manifest.PreviousMode)
		if mode == 0 {
			mode = 0o644
		}
		if err := atomicWriteFile(paths.DaemonConfig, manifest.PreviousContent, mode); err != nil {
			return err
		}
	} else if err := removeDBusActivationFile(paths.DaemonConfig); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := removeDBusActivationFile(paths.ManifestPath); err != nil && !os.IsNotExist(err) {
		if restoreErr := atomicWriteFile(paths.DaemonConfig, managedContent, managedMode); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore managed D-Bus activation config: %w", restoreErr))
		}
		return err
	}
	return syncDirectory(filepath.Dir(paths.ManifestPath))
}

func printDBusActivationStatus(ctx context.Context, paths dbusActivationPaths) error {
	manifest, err := readDBusActivationManifest(paths.ManifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			if _, configErr := os.Lstat(paths.DaemonConfig); configErr == nil {
				return fmt.Errorf("unmanaged D-Bus activation config exists at %s", paths.DaemonConfig)
			} else if !os.IsNotExist(configErr) {
				return configErr
			}
			fmt.Println("D-Bus activation backend: disabled")
			return nil
		}
		return err
	}
	if manifest.Backend != "daemon" || filepath.Clean(manifest.DaemonConfig) != filepath.Clean(paths.DaemonConfig) {
		return errors.New("D-Bus activation manifest does not describe the daemon backend")
	}
	content, _, err := readRegularFile(paths.DaemonConfig)
	if err != nil {
		return err
	}
	if hashBytes(content) != manifest.CurrentHash {
		return errDBusActivationChanged
	}
	if err := pingDBusActivationCore(ctx, paths.ControlPath); err != nil {
		return fmt.Errorf("D-Bus activation backend %s is configured but core is unavailable: %w", manifest.Backend, err)
	}
	fmt.Printf("D-Bus activation backend: %s\n", manifest.Backend)
	fmt.Printf("Manifest: %s\n", paths.ManifestPath)
	return nil
}

func defaultDaemonPreflight(ctx context.Context, paths dbusActivationPaths) error {
	if effectiveUID() != 0 {
		return errors.New("D-Bus activation configuration requires root")
	}
	info, err := os.Lstat(paths.HelperPath)
	if err != nil {
		return fmt.Errorf("inspect D-Bus helper: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSetuid == 0 || info.Mode()&(os.ModeSetgid|os.ModeSticky) != 0 || info.Mode().Perm() != 0o750 {
		return fmt.Errorf("D-Bus helper must be a setuid regular file with mode 4750: %s", paths.HelperPath)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("D-Bus helper is not owned by root")
	}
	group, err := user.LookupGroup("dbus")
	if err != nil {
		return err
	}
	dbusGID, err := strconv.ParseUint(group.Gid, 10, 32)
	if err != nil {
		return err
	}
	if uint64(stat.Gid) != dbusGID {
		return fmt.Errorf("D-Bus helper gid %d does not match dbus gid %d", stat.Gid, dbusGID)
	}
	if err := pingDBusActivationCore(ctx, paths.ControlPath); err != nil {
		return fmt.Errorf("sys-dbusd core is unavailable: %w", err)
	}
	return nil
}

func readRegularFile(path string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("refusing non-regular D-Bus activation config: %s", path)
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(info, openedInfo) {
		return nil, nil, fmt.Errorf("D-Bus activation file changed while opening: %s", path)
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, err
	}
	return content, openedInfo, nil
}

func defaultPingDBusActivationCore(ctx context.Context, path string) error {
	client, err := dbusactivation.DialClient(ctx, path, dbusactivation.FrontendAdmin)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Ping(ctx)
}

func daemonActivationConfig(helperPath string) []byte {
	return []byte("<busconfig>\n  <servicehelper>" + helperPath + "</servicehelper>\n</busconfig>\n")
}

func readDBusActivationManifest(path string) (dbusActivationManifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return dbusActivationManifest{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return dbusActivationManifest{}, errors.New("D-Bus activation manifest must be a regular file with mode 0600")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(effectiveUID()) {
		return dbusActivationManifest{}, errors.New("D-Bus activation manifest has an unexpected owner")
	}
	data, openedInfo, err := readRegularFile(path)
	if err != nil {
		return dbusActivationManifest{}, err
	}
	if !os.SameFile(info, openedInfo) {
		return dbusActivationManifest{}, errors.New("D-Bus activation manifest changed while opening")
	}
	var manifest dbusActivationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return dbusActivationManifest{}, err
	}
	if manifest.Version != 1 || manifest.Backend == "" || manifest.CurrentHash == "" {
		return dbusActivationManifest{}, errors.New("invalid D-Bus activation manifest")
	}
	return manifest, nil
}

func rollbackDaemonConfig(paths dbusActivationPaths, manifest dbusActivationManifest, cause error) error {
	if manifest.PreviousExists {
		mode := os.FileMode(manifest.PreviousMode)
		if mode == 0 {
			mode = 0o644
		}
		_ = atomicWriteFile(paths.DaemonConfig, manifest.PreviousContent, mode)
	} else {
		_ = os.Remove(paths.DaemonConfig)
	}
	return cause
}

func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink target: %s", path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".servicectl-dbus-activation-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
