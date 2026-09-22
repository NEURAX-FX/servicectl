package main

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"

	"servicectl/internal/cgrouptrack"
)

func (s *scheduler) Prepare(_ context.Context, scope cgrouptrack.Scope, unit string) (cgrouptrack.UnitStatus, error) {
	key := cgrouptrack.UnitKey{Mode: scope.Mode, UID: scope.UID, Unit: canonicalUnit(unit)}
	if err := key.Validate(); err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	groups := s.currentGroups()
	var err error
	if p, ok := groups.(interface {
		PrepareNative(cgrouptrack.UnitKey) error
	}); ok {
		err = p.PrepareNative(key)
	} else {
		err = groups.Ensure(key)
	}
	if err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	path := groups.Path(key)
	if _, real := groups.(*cgrouptrack.LinuxCgroupFS); real && path != "" {
		relative, e := filepath.Rel("/sys/fs/cgroup", path)
		if e != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			return cgrouptrack.UnitStatus{}, errors.New("native Dinit cgroup must be visible below /sys/fs/cgroup")
		}
	}
	s.mu.Lock()
	s.native[key] = true
	s.mu.Unlock()
	if err = s.saveRegistry(); err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	return cgrouptrack.UnitStatus{Identity: cgrouptrack.InstanceIdentity{UnitKey: key}, State: cgrouptrack.StateStopped, Path: path}, nil
}
func (s *scheduler) Release(_ context.Context, scope cgrouptrack.Scope, unit string) error {
	key := cgrouptrack.UnitKey{Mode: scope.Mode, UID: scope.UID, Unit: canonicalUnit(unit)}
	if err := key.Validate(); err != nil {
		return err
	}
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	groups := s.currentGroups()
	pids, err := groups.PIDs(key)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(pids) > 0 {
		return fmt.Errorf("cgroup %s is still populated", unit)
	}
	s.mu.Lock()
	declared := s.native[key]
	delete(s.native, key)
	s.mu.Unlock()
	if err = s.saveRegistry(); err != nil {
		s.mu.Lock()
		if declared {
			s.native[key] = true
		}
		s.mu.Unlock()
		return err
	}
	_, err = groups.RemoveIfEmpty(key)
	return err
}
func (s *scheduler) nativeDeclared(key cgrouptrack.UnitKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.native[key]
}
func (s *scheduler) removeUndeclaredEmpty(key cgrouptrack.UnitKey) {
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	if !s.nativeDeclared(key) {
		_, _ = s.currentGroups().RemoveIfEmpty(key)
	}
}
func (s *scheduler) verifyNative(identity cgrouptrack.InstanceIdentity) cgrouptrack.MigrationResult {
	groups := s.currentGroups()
	pids, err := groups.PIDs(identity.UnitKey)
	if err != nil {
		return cgrouptrack.MigrationResult{State: cgrouptrack.StateDegraded, Err: err}
	}
	process, err := s.proc.Inspect(identity.MainPID)
	if err == nil && process.PIDFD >= 0 {
		defer unix.Close(process.PIDFD)
	}
	if err == nil && (process.StartTime != identity.MainPIDStartTime || !containsPID(pids, identity.MainPID)) {
		err = errors.New("native process is not in its declared cgroup")
	}
	if err != nil {
		return cgrouptrack.MigrationResult{State: cgrouptrack.StateDegraded, Err: err}
	}
	return cgrouptrack.MigrationResult{State: cgrouptrack.StateTracked}
}
