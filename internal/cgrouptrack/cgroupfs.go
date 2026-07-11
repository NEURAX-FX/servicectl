package cgrouptrack

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const cgroup2SuperMagic = 0x63677270

type GroupSnapshot struct {
	Key  UnitKey
	PIDs []int
}

type CgroupFS interface {
	Ensure(UnitKey) error
	MovePID(UnitKey, int) error
	PIDs(UnitKey) ([]int, error)
	RemoveIfEmpty(UnitKey) (bool, error)
	Scan() ([]GroupSnapshot, error)
	Path(UnitKey) string
}

type LinuxCgroupFS struct {
	root      string
	rootFD    int
	synthetic bool
	mu        sync.Mutex
}

type magicCheck func(string) error

func OpenCgroupFS(root string) (*LinuxCgroupFS, error) {
	return openCgroupFS(root, requireCgroup2)
}

func openCgroupFS(root string, check magicCheck) (*LinuxCgroupFS, error) {
	clean := filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(clean) {
		return nil, errors.New("cgroup root must be absolute")
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("cgroup root must be a real directory")
	}
	if err := check(clean); err != nil {
		return nil, err
	}
	for _, component := range []string{"system", "user"} {
		child, err := os.Lstat(filepath.Join(clean, component))
		if err == nil && (child.Mode()&os.ModeSymlink != 0 || !child.IsDir()) {
			return nil, fmt.Errorf("cgroup root component %s is not a real directory", component)
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	rootFD, err := unix.Open(clean, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Statfs_t
	if err := unix.Fstatfs(rootFD, &stat); err != nil {
		unix.Close(rootFD)
		return nil, err
	}
	return &LinuxCgroupFS{root: clean, rootFD: rootFD, synthetic: uint64(stat.Type) != cgroup2SuperMagic}, nil
}

func requireCgroup2(path string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return err
	}
	if uint64(stat.Type) != cgroup2SuperMagic {
		return fmt.Errorf("%s is not on cgroup v2", path)
	}
	return nil
}

func skipMagicCheck(string) error { return nil }

func (f *LinuxCgroupFS) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rootFD < 0 {
		return nil
	}
	err := unix.Close(f.rootFD)
	f.rootFD = -1
	return err
}

func (f *LinuxCgroupFS) Ensure(key UnitKey) error {
	components, err := groupComponents(key)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fd, err := f.ensureComponentsLocked(components)
	if err == nil {
		unix.Close(fd)
	}
	return err
}

func (f *LinuxCgroupFS) MovePID(key UnitKey, pid int) error {
	if pid <= 0 {
		return errors.New("pid must be positive")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	leaf, err := f.openGroupLocked(key)
	if err != nil {
		return err
	}
	defer unix.Close(leaf)
	fd, err := openBeneath(leaf, "cgroup.procs", unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "cgroup.procs")
	if file == nil {
		unix.Close(fd)
		return errors.New("open cgroup.procs")
	}
	defer file.Close()
	_, err = file.WriteString(strconv.Itoa(pid) + "\n")
	return err
}

func (f *LinuxCgroupFS) PIDs(key UnitKey) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pidsLocked(key)
}

func (f *LinuxCgroupFS) pidsLocked(key UnitKey) ([]int, error) {
	leaf, err := f.openGroupLocked(key)
	if err != nil {
		return nil, err
	}
	defer unix.Close(leaf)
	fd, err := openBeneath(leaf, "cgroup.procs", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "cgroup.procs")
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("open cgroup.procs")
	}
	defer file.Close()
	data, err := readLimited(file, 8<<20)
	if err != nil {
		return nil, err
	}
	return parsePIDs(data)
}

func (f *LinuxCgroupFS) RemoveIfEmpty(key UnitKey) (bool, error) {
	components, err := groupComponents(key)
	if err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	pids, err := f.pidsLocked(key)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, unix.ENOENT) {
			return true, nil
		}
		return false, err
	}
	if len(pids) != 0 {
		return false, nil
	}
	if f.synthetic {
		pathComponents := append([]string{f.root}, components...)
		_ = os.Remove(filepath.Join(append(pathComponents, "cgroup.procs")...))
	}
	parent, err := f.openParentLocked(components)
	if err != nil {
		return false, err
	}
	defer unix.Close(parent)
	if err := unix.Unlinkat(parent, components[len(components)-1], unix.AT_REMOVEDIR); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return true, nil
		}
		if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EBUSY) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (f *LinuxCgroupFS) MigrateLegacy(key UnitKey) error {
	components, err := groupComponents(key)
	if err != nil {
		return err
	}
	legacy, err := legacyGroupComponents(key)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	parent, err := f.openParentLocked(components)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	defer unix.Close(parent)
	if err := unix.Renameat2(parent, legacy[len(legacy)-1], parent, components[len(components)-1], unix.RENAME_NOREPLACE); err == nil {
		return nil
	} else if errors.Is(err, unix.ENOENT) {
		return nil
	} else if !errors.Is(err, unix.EEXIST) {
		return err
	}
	pids, err := f.pidsAtComponentsLocked(legacy)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || os.IsNotExist(err) {
			return nil
		}
		return err
	}
	leaf, err := f.openGroupLocked(key)
	if err != nil {
		return err
	}
	defer unix.Close(leaf)
	if f.synthetic {
		current, err := f.pidsLocked(key)
		if err != nil {
			return err
		}
		all := append(current, pids...)
		sort.Ints(all)
		all = compactPIDs(all)
		data := make([]byte, 0, len(all)*8)
		for _, pid := range all {
			data = strconv.AppendInt(data, int64(pid), 10)
			data = append(data, '\n')
		}
		if err := os.WriteFile(filepath.Join(f.root, filepath.Join(components...), "cgroup.procs"), data, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(f.root, filepath.Join(legacy...), "cgroup.procs"), nil, 0o644); err != nil {
			return err
		}
	} else {
		for _, pid := range pids {
			if err := writePIDAt(leaf, pid); err != nil {
				return err
			}
		}
	}
	remaining, err := f.pidsAtComponentsLocked(legacy)
	if err != nil && !errors.Is(err, unix.ENOENT) && !os.IsNotExist(err) {
		return err
	}
	if len(remaining) != 0 {
		return errors.New("legacy cgroup remained populated after migration")
	}
	if f.synthetic {
		legacyPath := append([]string{f.root}, legacy...)
		_ = os.Remove(filepath.Join(append(legacyPath, "cgroup.procs")...))
	}
	return unix.Unlinkat(parent, legacy[len(legacy)-1], unix.AT_REMOVEDIR)
}

func (f *LinuxCgroupFS) Scan() ([]GroupSnapshot, error) {
	groups := make([]GroupSnapshot, 0)
	systemEntries, err := os.ReadDir(filepath.Join(f.root, "system"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range systemEntries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		key, err := DecodeUnitDirectory(ModeSystem, 0, entry.Name())
		if err != nil {
			continue
		}
		pids, err := f.PIDs(key)
		if err != nil {
			continue
		}
		groups = append(groups, GroupSnapshot{Key: key, PIDs: pids})
	}
	userEntries, err := os.ReadDir(filepath.Join(f.root, "user"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, uidEntry := range userEntries {
		if !uidEntry.IsDir() || uidEntry.Type()&os.ModeSymlink != 0 {
			continue
		}
		uid, err := strconv.ParseUint(uidEntry.Name(), 10, 32)
		if err != nil || strconv.FormatUint(uid, 10) != uidEntry.Name() {
			continue
		}
		unitEntries, err := os.ReadDir(filepath.Join(f.root, "user", uidEntry.Name()))
		if err != nil {
			continue
		}
		for _, entry := range unitEntries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			key, err := DecodeUnitDirectory(ModeUser, uint32(uid), entry.Name())
			if err != nil {
				continue
			}
			pids, err := f.PIDs(key)
			if err != nil {
				continue
			}
			groups = append(groups, GroupSnapshot{Key: key, PIDs: pids})
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Key.Mode != groups[j].Key.Mode {
			return groups[i].Key.Mode < groups[j].Key.Mode
		}
		if groups[i].Key.UID != groups[j].Key.UID {
			return groups[i].Key.UID < groups[j].Key.UID
		}
		return groups[i].Key.Unit < groups[j].Key.Unit
	})
	return groups, nil
}

func (f *LinuxCgroupFS) Path(key UnitKey) string {
	components, err := groupComponents(key)
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{f.root}, components...)...)
}

func (f *LinuxCgroupFS) Root() string { return f.root }

func groupComponents(key UnitKey) ([]string, error) {
	directory, err := key.DirectoryName()
	if err != nil {
		return nil, err
	}
	if key.Mode == ModeSystem {
		return []string{"system", directory}, nil
	}
	return []string{"user", strconv.FormatUint(uint64(key.UID), 10), directory}, nil
}

func legacyGroupComponents(key UnitKey) ([]string, error) {
	directory, err := legacyUnitDirectory(key)
	if err != nil {
		return nil, err
	}
	if key.Mode == ModeSystem {
		return []string{"system", directory}, nil
	}
	return []string{"user", strconv.FormatUint(uint64(key.UID), 10), directory}, nil
}

func (f *LinuxCgroupFS) ensureComponentsLocked(components []string) (int, error) {
	current, err := duplicateFD(f.rootFD)
	if err != nil {
		return -1, err
	}
	for _, component := range components {
		if err := unix.Mkdirat(current, component, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
			unix.Close(current)
			return -1, err
		}
		next, err := openBeneath(current, component, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		unix.Close(current)
		if err != nil {
			return -1, err
		}
		current = next
	}
	return current, nil
}

func (f *LinuxCgroupFS) openGroupLocked(key UnitKey) (int, error) {
	components, err := groupComponents(key)
	if err != nil {
		return -1, err
	}
	current, err := duplicateFD(f.rootFD)
	if err != nil {
		return -1, err
	}
	for _, component := range components {
		next, err := openBeneath(current, component, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		unix.Close(current)
		if err != nil {
			return -1, err
		}
		current = next
	}
	return current, nil
}

func (f *LinuxCgroupFS) pidsAtComponentsLocked(components []string) ([]int, error) {
	current, err := duplicateFD(f.rootFD)
	if err != nil {
		return nil, err
	}
	for _, component := range components {
		next, openErr := openBeneath(current, component, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		unix.Close(current)
		if openErr != nil {
			return nil, openErr
		}
		current = next
	}
	defer unix.Close(current)
	fd, err := openBeneath(current, "cgroup.procs", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "cgroup.procs")
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("open cgroup.procs")
	}
	defer file.Close()
	data, err := readLimited(file, 8<<20)
	if err != nil {
		return nil, err
	}
	return parsePIDs(data)
}

func writePIDAt(leaf int, pid int) error {
	fd, err := openBeneath(leaf, "cgroup.procs", unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "cgroup.procs")
	if file == nil {
		unix.Close(fd)
		return errors.New("open cgroup.procs")
	}
	defer file.Close()
	_, err = file.WriteString(strconv.Itoa(pid) + "\n")
	return err
}

func parsePIDs(data []byte) ([]int, error) {
	seen := make(map[int]struct{})
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("invalid PID %q in cgroup.procs", field)
		}
		seen[pid] = struct{}{}
	}
	pids := make([]int, 0, len(seen))
	for pid := range seen {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}

func compactPIDs(pids []int) []int {
	if len(pids) < 2 {
		return pids
	}
	write := 1
	for read := 1; read < len(pids); read++ {
		if pids[read] == pids[write-1] {
			continue
		}
		pids[write] = pids[read]
		write++
	}
	return pids[:write]
}

func (f *LinuxCgroupFS) openParentLocked(components []string) (int, error) {
	current, err := duplicateFD(f.rootFD)
	if err != nil {
		return -1, err
	}
	for _, component := range components[:len(components)-1] {
		next, err := openBeneath(current, component, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		unix.Close(current)
		if err != nil {
			return -1, err
		}
		current = next
	}
	return current, nil
}

func openBeneath(dirfd int, path string, flags uint64, mode uint64) (int, error) {
	how := &unix.OpenHow{
		Flags:   flags,
		Mode:    mode,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	return unix.Openat2(dirfd, path, how)
}

func duplicateFD(fd int) (int, error) {
	return unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
}

func readLimited(file *os.File, maximum int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, errors.New("cgroup file exceeds size limit")
	}
	return data, nil
}
