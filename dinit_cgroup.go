package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"servicectl/internal/cgrouptrack"
)

// prepareNativeCgroup reserves the leaf before publishing run-in-cgroup. Only
// absence of the optional daemon permits the existing untracked runtime mode.
func prepareNativeCgroup(unit string) (string, error) {
	if !config.NativeCgroups {
		return "", nil
	}
	mode := cgrouptrack.ModeSystem
	uid := uint32(0)
	if userMode() {
		mode = cgrouptrack.ModeUser
		v, err := strconv.ParseUint(currentUID(), 10, 32)
		if err != nil {
			return "", err
		}
		uid = uint32(v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := cgrouptrack.NewClient(cgrouptrack.SocketPath()).Do(ctx, cgrouptrack.Request{Operation: cgrouptrack.OpPrepare, Mode: mode, UID: uid, Unit: strings.TrimSuffix(unit, ".service") + ".service"})
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if response.Unit == nil || response.Unit.Path == "" {
		return "", errors.New("cgroup preparation returned no path")
	}
	relative, err := filepath.Rel("/sys/fs/cgroup", response.Unit.Path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", fmt.Errorf("cgroup path %q is outside Dinit's hierarchy", response.Unit.Path)
	}
	return "/" + filepath.ToSlash(relative), nil
}
