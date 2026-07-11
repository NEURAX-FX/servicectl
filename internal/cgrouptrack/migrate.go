package cgrouptrack

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"golang.org/x/sys/unix"
)

var (
	ErrProcessIdentityChanged = errors.New("process identity changed")
	ErrMembershipNotObserved  = errors.New("cgroup membership was not observed")
)

type Migrator struct {
	Proc      ProcFS
	Groups    CgroupFS
	MaxRounds int
	Deadline  time.Duration
}

type MigrationResult struct {
	State   TrackingState
	Moved   []int
	Skipped []PIDError
	Err     error
}

type PIDError struct {
	PID   int    `json:"pid"`
	Error string `json:"error"`
}

func (m Migrator) Migrate(ctx context.Context, identity InstanceIdentity) MigrationResult {
	if err := identity.Validate(); err != nil {
		return MigrationResult{State: StateDegraded, Err: err}
	}
	if m.Proc == nil || m.Groups == nil {
		return MigrationResult{State: StateDegraded, Err: errors.New("migrator is not configured")}
	}
	maxRounds := m.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 8
	}
	deadline := m.Deadline
	if deadline <= 0 {
		deadline = 250 * time.Millisecond
	}
	workCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	main, err := m.inspectStable(identity.MainPID)
	if err != nil {
		return MigrationResult{State: StateDegraded, Err: err}
	}
	if main.StartTime != identity.MainPIDStartTime || (identity.Mode == ModeUser && main.UID != identity.UID) {
		return MigrationResult{State: StateDegraded, Err: ErrProcessIdentityChanged}
	}
	if err := m.Groups.Ensure(identity.UnitKey); err != nil {
		return MigrationResult{State: StateDegraded, Err: err}
	}

	result := MigrationResult{State: StateTracked}
	moved := make(map[int]bool)
	if err := m.moveAndVerify(workCtx, identity.UnitKey, main); err != nil {
		result.State = StateDegraded
		result.Err = err
		return result
	}
	result.Moved = append(result.Moved, main.PID)
	moved[main.PID] = true

	for round := 0; round < maxRounds; round++ {
		if err := workCtx.Err(); err != nil {
			result.State = StatePartial
			result.Err = err
			return result
		}
		processes, skipped, err := m.processSnapshot(identity)
		result.Skipped = append(result.Skipped, skipped...)
		if err != nil {
			result.State = StateDegraded
			result.Err = err
			return result
		}
		byPID := make(map[int]Process, len(processes))
		for _, process := range processes {
			byPID[process.PID] = process
		}
		newPIDs := make([]int, 0)
		for _, pid := range Descendants(identity.MainPID, processes) {
			if !moved[pid] {
				newPIDs = append(newPIDs, pid)
			}
		}
		if len(newPIDs) == 0 {
			return result
		}
		for _, pid := range newPIDs {
			process := byPID[pid]
			if err := m.moveAndVerify(workCtx, identity.UnitKey, process); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					result.State = StatePartial
					result.Err = err
					return result
				}
				result.Skipped = append(result.Skipped, PIDError{PID: pid, Error: err.Error()})
				continue
			}
			moved[pid] = true
			result.Moved = append(result.Moved, pid)
		}
	}
	result.State = StatePartial
	result.Err = errors.New("migration did not converge within round limit")
	return result
}

func (m Migrator) processSnapshot(identity InstanceIdentity) ([]Process, []PIDError, error) {
	pids, err := m.Proc.ListPIDs()
	if err != nil {
		return nil, nil, err
	}
	processes := make([]Process, 0, len(pids))
	skipped := make([]PIDError, 0)
	for _, pid := range pids {
		process, err := m.inspectStable(pid)
		if err != nil {
			if pid == identity.MainPID {
				return nil, skipped, err
			}
			skipped = append(skipped, PIDError{PID: pid, Error: err.Error()})
			continue
		}
		if identity.Mode == ModeUser && process.UID != identity.UID {
			skipped = append(skipped, PIDError{PID: pid, Error: "process UID does not match user service"})
			continue
		}
		processes = append(processes, process)
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].PID < processes[j].PID })
	return processes, skipped, nil
}

func (m Migrator) inspectStable(pid int) (Process, error) {
	process, err := m.Proc.Inspect(pid)
	if err != nil {
		return Process{}, err
	}
	if process.PIDFD >= 0 {
		defer unix.Close(process.PIDFD)
		process.PIDFD = -1
	}
	return process, nil
}

func (m Migrator) moveAndVerify(ctx context.Context, key UnitKey, before Process) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.Groups.MovePID(key, before.PID); err != nil {
		return err
	}
	after, err := m.inspectStable(before.PID)
	if err != nil {
		return err
	}
	if after.StartTime != before.StartTime || after.UID != before.UID {
		return fmt.Errorf("%w for PID %d", ErrProcessIdentityChanged, before.PID)
	}
	pids, err := m.Groups.PIDs(key)
	if err != nil {
		return err
	}
	index := sort.SearchInts(pids, before.PID)
	if index == len(pids) || pids[index] != before.PID {
		return fmt.Errorf("%w for PID %d", ErrMembershipNotObserved, before.PID)
	}
	return nil
}
