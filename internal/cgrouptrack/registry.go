package cgrouptrack

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const MaxRegistryBytes = 1 << 20

type Registry struct {
	Version int          `json:"version"`
	Units   []UnitRecord `json:"units"`
}

type UnitRecord struct {
	Identity      InstanceIdentity `json:"identity"`
	State         TrackingState    `json:"state"`
	LastMigration string           `json:"last_migration,omitempty"`
	RetryCount    int              `json:"retry_count,omitempty"`
	LastError     string           `json:"last_error,omitempty"`
}

func ReadRegistry(path string) (Registry, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Registry{Version: 1}, nil
	}
	if err != nil {
		return Registry{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return Registry{}, errors.New("registry must be a regular file with mode 0600")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return Registry{}, errors.New("registry has an unexpected owner")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return Registry{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return Registry{}, err
	}
	if !os.SameFile(info, openedInfo) {
		return Registry{}, errors.New("registry changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxRegistryBytes+1))
	if err != nil {
		return Registry{}, err
	}
	if len(data) > MaxRegistryBytes {
		return Registry{}, errors.New("registry exceeds size limit")
	}
	var registry Registry
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return Registry{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Registry{}, err
	}
	if err := validateRegistry(registry); err != nil {
		return Registry{}, err
	}
	return registry, nil
}

func ReadOrQuarantine(path string, now time.Time) (Registry, error) {
	registry, err := ReadRegistry(path)
	if err == nil || os.IsNotExist(err) {
		return registry, err
	}
	if _, statErr := os.Lstat(path); statErr != nil {
		return Registry{Version: 1}, err
	}
	quarantine := path + ".corrupt-" + now.UTC().Format("20060102T150405.000000000Z")
	if renameErr := os.Rename(path, quarantine); renameErr != nil {
		return Registry{Version: 1}, errors.Join(err, renameErr)
	}
	if syncErr := syncDirectory(filepath.Dir(path)); syncErr != nil {
		return Registry{Version: 1}, errors.Join(err, syncErr)
	}
	return Registry{Version: 1}, err
}

func WriteRegistry(path string, registry Registry) error {
	if err := validateRegistry(registry); err != nil {
		return err
	}
	data, err := json.Marshal(registry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxRegistryBytes {
		return errors.New("registry exceeds size limit")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if err != nil {
			return err
		}
		return errors.New("registry directory must be a real directory")
	}
	temporary, err := os.CreateTemp(directory, ".registry-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	committed = true
	return nil
}

func validateRegistry(registry Registry) error {
	if registry.Version != 1 {
		return fmt.Errorf("unsupported registry version %d", registry.Version)
	}
	seen := make(map[UnitKey]struct{}, len(registry.Units))
	for _, record := range registry.Units {
		if err := record.Identity.Validate(); err != nil {
			return err
		}
		if _, ok := seen[record.Identity.UnitKey]; ok {
			return fmt.Errorf("duplicate registry unit %s", record.Identity.Unit)
		}
		seen[record.Identity.UnitKey] = struct{}{}
		if record.State == "" || record.RetryCount < 0 {
			return errors.New("invalid registry state or retry count")
		}
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("registry contains trailing JSON")
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
