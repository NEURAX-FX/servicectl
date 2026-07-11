package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	defaultCgroupMountPoint = "/sys/fs/cgroup"
	procSelfMountInfo       = "/proc/self/mountinfo"
	procSelfCgroup          = "/proc/self/cgroup"
)

type cgroupMountSystem interface {
	ReadFile(string) ([]byte, error)
	Lstat(string) (os.FileInfo, error)
	Mkdir(string, os.FileMode) error
	ReadDir(string) ([]os.DirEntry, error)
	Mount(source, target, fstype string, flags uintptr, data string) error
}

type linuxCgroupMountSystem struct{}

func (linuxCgroupMountSystem) ReadFile(path string) ([]byte, error)       { return os.ReadFile(path) }
func (linuxCgroupMountSystem) Lstat(path string) (os.FileInfo, error)     { return os.Lstat(path) }
func (linuxCgroupMountSystem) Mkdir(path string, mode os.FileMode) error  { return os.Mkdir(path, mode) }
func (linuxCgroupMountSystem) ReadDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }
func (linuxCgroupMountSystem) Mount(source, target, fstype string, flags uintptr, data string) error {
	return unix.Mount(source, target, fstype, flags, data)
}

type cgroupMountOptions struct {
	MountPoint string
	AutoMount  bool
}

type mountInfoEntry struct {
	root       string
	mountPoint string
	fsType     string
}

func ensureCgroup2Mount(system cgroupMountSystem, options cgroupMountOptions) (string, error) {
	mountPoint := filepath.Clean(options.MountPoint)
	if mountPoint == "." || !filepath.IsAbs(mountPoint) {
		return "", errors.New("cgroup mount point must be absolute")
	}
	mountinfo, err := system.ReadFile(procSelfMountInfo)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", procSelfMountInfo, err)
	}
	entries := parseMountInfo(string(mountinfo))
	if entry, ok := exactMount(entries, mountPoint); ok {
		if entry.fsType != "cgroup2" {
			return "", fmt.Errorf("%s is already mounted with filesystem type %s", mountPoint, entry.fsType)
		}
		return mountPoint, nil
	}
	if existing := firstCgroup2Mount(entries); existing != "" {
		return existing, nil
	}
	if !options.AutoMount {
		return "", errors.New("cgroup v2 is not mounted and auto-mount is disabled")
	}
	selfCgroup, err := system.ReadFile(procSelfCgroup)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", procSelfCgroup, err)
	}
	if !hasUnifiedCgroupEntry(string(selfCgroup)) {
		return "", errors.New("process has no unified cgroup v2 entry")
	}
	info, err := system.Lstat(mountPoint)
	if errors.Is(err, fs.ErrNotExist) {
		if err := system.Mkdir(mountPoint, 0o755); err != nil {
			return "", fmt.Errorf("create cgroup mount point %s: %w", mountPoint, err)
		}
		info, err = system.Lstat(mountPoint)
	}
	if err != nil {
		return "", fmt.Errorf("inspect cgroup mount point %s: %w", mountPoint, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("cgroup mount point %s must be a real directory", mountPoint)
	}
	directoryEntries, err := system.ReadDir(mountPoint)
	if err != nil {
		return "", fmt.Errorf("read cgroup mount point %s: %w", mountPoint, err)
	}
	if len(directoryEntries) != 0 {
		return "", fmt.Errorf("cgroup mount point %s is not an empty directory", mountPoint)
	}
	flags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	mountErr := system.Mount("cgroup2", mountPoint, "cgroup2", flags, "")
	if mountErr != nil && !errors.Is(mountErr, unix.EBUSY) {
		return "", fmt.Errorf("mount cgroup2 at %s: %w", mountPoint, mountErr)
	}
	mountinfo, err = system.ReadFile(procSelfMountInfo)
	if err != nil {
		return "", fmt.Errorf("verify cgroup2 mount at %s: %w", mountPoint, err)
	}
	entry, ok := exactMount(parseMountInfo(string(mountinfo)), mountPoint)
	if !ok {
		return "", fmt.Errorf("mounted filesystem at %s was not found", mountPoint)
	}
	if entry.fsType != "cgroup2" {
		return "", fmt.Errorf("%s is already mounted with filesystem type %s", mountPoint, entry.fsType)
	}
	return mountPoint, nil
}

func prepareCgroupRootWith(system cgroupMountSystem, configuredRoot string, autoMount bool) (string, error) {
	mountPoint, err := ensureCgroup2Mount(system, cgroupMountOptions{
		MountPoint: defaultCgroupMountPoint,
		AutoMount:  autoMount,
	})
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(configuredRoot)
	if clean != defaultCgroupRoot {
		info, err := system.Lstat(clean)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("configured cgroup root must be a real directory")
		}
		return clean, nil
	}
	root := filepath.Join(mountPoint, "servicectl.slice")
	info, err := system.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		if err := system.Mkdir(root, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		return root, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("configured cgroup root must be a real directory")
	}
	return root, nil
}

func parseMountInfo(mountinfo string) []mountInfoEntry {
	entries := make([]mountInfoEntry, 0)
	for _, line := range strings.Split(mountinfo, "\n") {
		parts := strings.Split(line, " - ")
		if len(parts) != 2 {
			continue
		}
		left := strings.Fields(parts[0])
		right := strings.Fields(parts[1])
		if len(left) < 5 || len(right) < 1 {
			continue
		}
		entries = append(entries, mountInfoEntry{
			root:       filepath.Clean(unescapeMountPath(left[3])),
			mountPoint: filepath.Clean(unescapeMountPath(left[4])),
			fsType:     right[0],
		})
	}
	return entries
}

func exactMount(entries []mountInfoEntry, mountPoint string) (mountInfoEntry, bool) {
	for _, entry := range entries {
		if entry.mountPoint == mountPoint {
			return entry, true
		}
	}
	return mountInfoEntry{}, false
}

func firstCgroup2Mount(entries []mountInfoEntry) string {
	for _, entry := range entries {
		if entry.fsType == "cgroup2" {
			return entry.mountPoint
		}
	}
	return ""
}

func hasUnifiedCgroupEntry(selfCgroup string) bool {
	for _, line := range strings.Split(selfCgroup, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "0::") {
			return true
		}
	}
	return false
}
