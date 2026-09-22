package cgrouptrack

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

// PrepareNative creates the leaf before Dinit forks. A user instance receives
// access only to its own plane's membership files, never to system leaves.
func (f *LinuxCgroupFS) PrepareNative(key UnitKey) error {
	if err := f.Ensure(key); err != nil {
		return err
	}
	if key.Mode != ModeUser || key.UID == 0 {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	components, err := groupComponents(key)
	if err != nil {
		return err
	}
	for _, parts := range [][]string{components[:2], components} {
		fd, err := f.ensureComponentsLocked(parts)
		if err != nil {
			return err
		}
		pfd, err := openBeneath(fd, "cgroup.procs", unix.O_WRONLY|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return err
		}
		err = unix.Fchown(pfd, int(key.UID), -1)
		if err == nil {
			err = unix.Fchmod(pfd, 0600)
		}
		unix.Close(pfd)
		if err != nil {
			return fmt.Errorf("delegate cgroup membership: %w", err)
		}
	}
	return nil
}

func SocketPath() string {
	if p := os.Getenv("SERVICECTL_CGROUP_SOCKET"); p != "" {
		return p
	}
	return "/run/servicectl/sys-cgroupd.sock"
}
