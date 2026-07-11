package procinfo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const MaxStatBytes = 64 * 1024

type Stat struct {
	PID       int
	Comm      string
	State     byte
	PPID      int
	StartTime uint64
}

func ParseStat(line string) (Stat, error) {
	if len(line) == 0 || len(line) > MaxStatBytes {
		return Stat{}, errors.New("invalid proc stat size")
	}
	open := strings.IndexByte(line, '(')
	close := strings.LastIndexByte(line, ')')
	if open <= 0 || close <= open || close+2 >= len(line) {
		return Stat{}, errors.New("malformed proc stat command")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line[:open]))
	if err != nil || pid <= 0 {
		return Stat{}, errors.New("invalid proc stat pid")
	}
	fields := strings.Fields(line[close+1:])
	if len(fields) < 20 || len(fields[0]) != 1 {
		return Stat{}, errors.New("truncated proc stat fields")
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil || ppid < 0 {
		return Stat{}, errors.New("invalid proc stat parent pid")
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return Stat{}, errors.New("invalid proc stat start time")
	}
	return Stat{PID: pid, Comm: line[open+1 : close], State: fields[0][0], PPID: ppid, StartTime: startTime}, nil
}

func ReadStat(procRoot string, pid int) (Stat, error) {
	if pid <= 0 {
		return Stat{}, errors.New("pid must be positive")
	}
	path := filepath.Join(procRoot, strconv.Itoa(pid), "stat")
	data, err := os.ReadFile(path)
	if err != nil {
		return Stat{}, err
	}
	if len(data) > MaxStatBytes {
		return Stat{}, fmt.Errorf("proc stat exceeds %d bytes", MaxStatBytes)
	}
	return ParseStat(strings.TrimSpace(string(data)))
}
