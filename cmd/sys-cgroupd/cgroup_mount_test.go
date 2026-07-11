package main

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const testUnifiedCgroup = "0::/\n"
const testCgroup2MountInfo = "20 1 0:19 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n"

func TestEnsureCgroup2MountUsesExistingMount(t *testing.T) {
	system := newFakeCgroupMountSystem()
	system.files[procSelfMountInfo] = []byte(testCgroup2MountInfo)
	result, err := ensureCgroup2Mount(system, cgroupMountOptions{MountPoint: defaultCgroupMountPoint, AutoMount: true})
	if err != nil {
		t.Fatal(err)
	}
	if result != defaultCgroupMountPoint || len(system.mounts) != 0 {
		t.Fatalf("result=%q mounts=%#v", result, system.mounts)
	}
}

func TestEnsureCgroup2MountDisabled(t *testing.T) {
	system := newFakeCgroupMountSystem()
	system.files[procSelfCgroup] = []byte(testUnifiedCgroup)
	_, err := ensureCgroup2Mount(system, cgroupMountOptions{MountPoint: defaultCgroupMountPoint, AutoMount: false})
	if err == nil || !strings.Contains(err.Error(), "auto-mount is disabled") {
		t.Fatalf("error = %v", err)
	}
	if len(system.mounts) != 0 {
		t.Fatalf("mounts = %#v", system.mounts)
	}
}

func TestEnsureCgroup2MountMountsEmptyCanonicalDirectory(t *testing.T) {
	system := newFakeCgroupMountSystem()
	system.files[procSelfCgroup] = []byte(testUnifiedCgroup)
	system.mountResult = func(call mountCall) error {
		system.files[procSelfMountInfo] = []byte(testCgroup2MountInfo)
		return nil
	}
	result, err := ensureCgroup2Mount(system, cgroupMountOptions{MountPoint: defaultCgroupMountPoint, AutoMount: true})
	if err != nil {
		t.Fatal(err)
	}
	if result != defaultCgroupMountPoint || len(system.mounts) != 1 {
		t.Fatalf("result=%q mounts=%#v", result, system.mounts)
	}
	call := system.mounts[0]
	wantFlags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if call.source != "cgroup2" || call.target != defaultCgroupMountPoint || call.fstype != "cgroup2" || call.flags != wantFlags || call.data != "" {
		t.Fatalf("mount call = %#v", call)
	}
}

func TestEnsureCgroup2MountCreatesMissingDirectory(t *testing.T) {
	system := newFakeCgroupMountSystem()
	delete(system.infos, defaultCgroupMountPoint)
	system.files[procSelfCgroup] = []byte(testUnifiedCgroup)
	system.mountResult = func(mountCall) error {
		system.files[procSelfMountInfo] = []byte(testCgroup2MountInfo)
		return nil
	}
	if _, err := ensureCgroup2Mount(system, cgroupMountOptions{MountPoint: defaultCgroupMountPoint, AutoMount: true}); err != nil {
		t.Fatal(err)
	}
	if !system.created[defaultCgroupMountPoint] {
		t.Fatalf("created = %#v", system.created)
	}
}

func TestEnsureCgroup2MountRejectsUnsafeTarget(t *testing.T) {
	tests := []struct {
		name    string
		info    fs.FileInfo
		entries []os.DirEntry
		want    string
	}{
		{name: "symlink", info: fakeFileInfo{name: "cgroup", mode: os.ModeSymlink}, want: "real directory"},
		{name: "regular file", info: fakeFileInfo{name: "cgroup", mode: 0o644}, want: "real directory"},
		{name: "non-empty", info: fakeFileInfo{name: "cgroup", mode: os.ModeDir | 0o755}, entries: []os.DirEntry{fakeDirEntry{name: "occupied"}}, want: "not an empty directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			system := newFakeCgroupMountSystem()
			system.files[procSelfCgroup] = []byte(testUnifiedCgroup)
			system.infos[defaultCgroupMountPoint] = test.info
			system.entries[defaultCgroupMountPoint] = test.entries
			_, err := ensureCgroup2Mount(system, cgroupMountOptions{MountPoint: defaultCgroupMountPoint, AutoMount: true})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
			if len(system.mounts) != 0 {
				t.Fatalf("mounts = %#v", system.mounts)
			}
		})
	}
}

func TestEnsureCgroup2MountRejectsWrongMountedFilesystem(t *testing.T) {
	system := newFakeCgroupMountSystem()
	system.files[procSelfMountInfo] = []byte("20 1 0:19 / /sys/fs/cgroup rw - tmpfs tmpfs rw\n")
	_, err := ensureCgroup2Mount(system, cgroupMountOptions{MountPoint: defaultCgroupMountPoint, AutoMount: true})
	if err == nil || !strings.Contains(err.Error(), "filesystem type tmpfs") {
		t.Fatalf("error = %v", err)
	}
	if len(system.mounts) != 0 {
		t.Fatalf("mounts = %#v", system.mounts)
	}
}

func TestEnsureCgroup2MountRequiresUnifiedCgroupEntry(t *testing.T) {
	system := newFakeCgroupMountSystem()
	system.files[procSelfCgroup] = []byte("5:freezer:/legacy\n")
	_, err := ensureCgroup2Mount(system, cgroupMountOptions{MountPoint: defaultCgroupMountPoint, AutoMount: true})
	if err == nil || !strings.Contains(err.Error(), "unified cgroup v2 entry") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnsureCgroup2MountReturnsMountError(t *testing.T) {
	system := newFakeCgroupMountSystem()
	system.files[procSelfCgroup] = []byte(testUnifiedCgroup)
	system.mountErr = unix.EPERM
	_, err := ensureCgroup2Mount(system, cgroupMountOptions{MountPoint: defaultCgroupMountPoint, AutoMount: true})
	if !errors.Is(err, unix.EPERM) || !strings.Contains(err.Error(), "mount cgroup2") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnsureCgroup2MountAcceptsBusyRaceAfterVerification(t *testing.T) {
	system := newFakeCgroupMountSystem()
	system.files[procSelfCgroup] = []byte(testUnifiedCgroup)
	system.mountResult = func(mountCall) error {
		system.files[procSelfMountInfo] = []byte(testCgroup2MountInfo)
		return unix.EBUSY
	}
	if _, err := ensureCgroup2Mount(system, cgroupMountOptions{MountPoint: defaultCgroupMountPoint, AutoMount: true}); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureCgroup2MountRejectsBusyWrongFilesystem(t *testing.T) {
	system := newFakeCgroupMountSystem()
	system.files[procSelfCgroup] = []byte(testUnifiedCgroup)
	system.mountResult = func(mountCall) error {
		system.files[procSelfMountInfo] = []byte("20 1 0:19 / /sys/fs/cgroup rw - tmpfs tmpfs rw\n")
		return unix.EBUSY
	}
	_, err := ensureCgroup2Mount(system, cgroupMountOptions{MountPoint: defaultCgroupMountPoint, AutoMount: true})
	if err == nil || !strings.Contains(err.Error(), "filesystem type tmpfs") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareCgroupRootRecoversAfterMountFailure(t *testing.T) {
	system := newFakeCgroupMountSystem()
	system.files[procSelfCgroup] = []byte(testUnifiedCgroup)
	system.mountErr = unix.EPERM
	if _, err := prepareCgroupRootWith(system, defaultCgroupRoot, true); !errors.Is(err, unix.EPERM) {
		t.Fatalf("first error = %v", err)
	}
	system.mountErr = nil
	system.mountResult = func(mountCall) error {
		system.files[procSelfMountInfo] = []byte(testCgroup2MountInfo)
		return nil
	}
	root, err := prepareCgroupRootWith(system, defaultCgroupRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	if root != defaultCgroupRoot || !system.created[defaultCgroupRoot] {
		t.Fatalf("root=%q created=%#v", root, system.created)
	}
}

type mountCall struct {
	source string
	target string
	fstype string
	flags  uintptr
	data   string
}

type fakeCgroupMountSystem struct {
	files       map[string][]byte
	infos       map[string]fs.FileInfo
	entries     map[string][]os.DirEntry
	created     map[string]bool
	mounts      []mountCall
	mountErr    error
	mountResult func(mountCall) error
}

func newFakeCgroupMountSystem() *fakeCgroupMountSystem {
	return &fakeCgroupMountSystem{
		files:   map[string][]byte{procSelfMountInfo: nil},
		infos:   map[string]fs.FileInfo{defaultCgroupMountPoint: fakeFileInfo{name: "cgroup", mode: os.ModeDir | 0o755}},
		entries: make(map[string][]os.DirEntry),
		created: make(map[string]bool),
	}
}

func (s *fakeCgroupMountSystem) ReadFile(path string) ([]byte, error) {
	data, ok := s.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (s *fakeCgroupMountSystem) Lstat(path string) (os.FileInfo, error) {
	info, ok := s.infos[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return info, nil
}

func (s *fakeCgroupMountSystem) Mkdir(path string, mode os.FileMode) error {
	s.created[path] = true
	s.infos[path] = fakeFileInfo{name: "cgroup", mode: os.ModeDir | mode}
	return nil
}

func (s *fakeCgroupMountSystem) ReadDir(path string) ([]os.DirEntry, error) {
	return append([]os.DirEntry(nil), s.entries[path]...), nil
}

func (s *fakeCgroupMountSystem) Mount(source, target, fstype string, flags uintptr, data string) error {
	call := mountCall{source: source, target: target, fstype: fstype, flags: flags, data: data}
	s.mounts = append(s.mounts, call)
	if s.mountResult != nil {
		return s.mountResult(call)
	}
	return s.mountErr
}

type fakeFileInfo struct {
	name string
	mode os.FileMode
}

func (i fakeFileInfo) Name() string       { return i.name }
func (i fakeFileInfo) Size() int64        { return 0 }
func (i fakeFileInfo) Mode() os.FileMode  { return i.mode }
func (i fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (i fakeFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fakeFileInfo) Sys() any           { return nil }

type fakeDirEntry struct{ name string }

func (e fakeDirEntry) Name() string               { return e.name }
func (e fakeDirEntry) IsDir() bool                { return false }
func (e fakeDirEntry) Type() os.FileMode          { return 0 }
func (e fakeDirEntry) Info() (os.FileInfo, error) { return fakeFileInfo{name: e.name}, nil }
