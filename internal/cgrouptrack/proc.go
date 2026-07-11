package cgrouptrack

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"servicectl/internal/procinfo"
)

const maxProcFileBytes = 1 << 20

var (
	ErrZombieProcess = errors.New("process is a zombie")
	ErrKernelThread  = errors.New("process has no userspace executable")
)

type FileIdentity struct {
	Device uint64
	Inode  uint64
}

type ProcStatus struct {
	UID uint32
}

type Process struct {
	PID       int
	PIDFD     int
	PPID      int
	UID       uint32
	StartTime uint64
	State     byte
	Comm      string
	Cgroup    string
}

type ProcFS interface {
	ReadStat(pid int) (procinfo.Stat, error)
	ReadStatus(pid int) (ProcStatus, error)
	ReadCgroup(pid int) (string, error)
	ReadExecutable(pid int) (string, error)
	ListPIDs() ([]int, error)
	OpenPIDFD(pid int) (int, error)
	PIDNamespace(pid int) (FileIdentity, error)
	SelfPIDNamespace() (FileIdentity, error)
	Inspect(pid int) (Process, error)
}

type LinuxProcFS struct {
	root      string
	openPIDFD func(int) (int, error)
}

func NewLinuxProcFS(root string) *LinuxProcFS {
	return &LinuxProcFS{
		root: filepath.Clean(root),
		openPIDFD: func(pid int) (int, error) {
			return unix.PidfdOpen(pid, 0)
		},
	}
}

func (p *LinuxProcFS) ReadStat(pid int) (procinfo.Stat, error) {
	return procinfo.ReadStat(p.root, pid)
}

func (p *LinuxProcFS) ReadStatus(pid int) (ProcStatus, error) {
	data, err := readBoundedFile(filepath.Join(p.root, integerString(pid), "status"), maxProcFileBytes)
	if err != nil {
		return ProcStatus{}, err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "Uid:" {
			continue
		}
		uid, err := strconv.ParseUint(fields[1], 10, 32)
		if err != nil {
			return ProcStatus{}, fmt.Errorf("parse process uid: %w", err)
		}
		return ProcStatus{UID: uint32(uid)}, nil
	}
	if err := scanner.Err(); err != nil {
		return ProcStatus{}, err
	}
	return ProcStatus{}, errors.New("process status has no UID")
}

func (p *LinuxProcFS) ReadCgroup(pid int) (string, error) {
	data, err := readBoundedFile(filepath.Join(p.root, integerString(pid), "cgroup"), maxProcFileBytes)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			if value == "" || value[0] != '/' {
				return "", errors.New("invalid unified cgroup path")
			}
			return value, nil
		}
	}
	return "", errors.New("process has no unified cgroup entry")
}

func (p *LinuxProcFS) ReadExecutable(pid int) (string, error) {
	return os.Readlink(filepath.Join(p.root, integerString(pid), "exe"))
}

func (p *LinuxProcFS) ListPIDs() ([]int, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || integerString(pid) != entry.Name() {
			continue
		}
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}

func (p *LinuxProcFS) OpenPIDFD(pid int) (int, error) {
	if pid <= 0 {
		return -1, errors.New("pid must be positive")
	}
	return p.openPIDFD(pid)
}

func (p *LinuxProcFS) PIDNamespace(pid int) (FileIdentity, error) {
	return p.pidNamespacePath(filepath.Join(p.root, integerString(pid), "ns", "pid"))
}

func (p *LinuxProcFS) SelfPIDNamespace() (FileIdentity, error) {
	return p.pidNamespacePath(filepath.Join(p.root, "self", "ns", "pid"))
}

func (p *LinuxProcFS) pidNamespacePath(path string) (FileIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileIdentity{}, errors.New("unsupported namespace stat information")
	}
	return FileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func (p *LinuxProcFS) Inspect(pid int) (Process, error) {
	stat, err := p.ReadStat(pid)
	if err != nil {
		return Process{}, err
	}
	if stat.State == 'Z' {
		return Process{}, ErrZombieProcess
	}
	if _, err := p.ReadExecutable(pid); err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOENT) {
			return Process{}, ErrKernelThread
		}
		return Process{}, err
	}
	status, err := p.ReadStatus(pid)
	if err != nil {
		return Process{}, err
	}
	cgroup, err := p.ReadCgroup(pid)
	if err != nil {
		return Process{}, err
	}
	pidfd, err := p.OpenPIDFD(pid)
	if err != nil {
		return Process{}, err
	}
	return Process{
		PID: pid, PIDFD: pidfd, PPID: stat.PPID, UID: status.UID,
		StartTime: stat.StartTime, State: stat.State, Comm: stat.Comm, Cgroup: cgroup,
	}, nil
}

func Descendants(root int, processes []Process) []int {
	children := make(map[int][]int)
	for _, process := range processes {
		if process.PID <= 0 || process.PID == root {
			continue
		}
		children[process.PPID] = append(children[process.PPID], process.PID)
	}
	for parent := range children {
		sort.Ints(children[parent])
	}
	seen := map[int]bool{root: true}
	queue := []int{root}
	result := make([]int, 0)
	for len(queue) != 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if seen[child] {
				continue
			}
			seen[child] = true
			result = append(result, child)
			queue = append(queue, child)
		}
	}
	return result
}

func readBoundedFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maximum)
	}
	return data, nil
}

func integerString(value int) string {
	return strconv.Itoa(value)
}

func uint64String(value uint64) string {
	return strconv.FormatUint(value, 10)
}
