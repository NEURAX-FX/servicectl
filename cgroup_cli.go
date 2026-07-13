package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"servicectl/internal/cgrouptrack"
)

var (
	cgroupdSocketPath = "/run/servicectl/sys-cgroupd.sock"
	doCgroupRequest   = defaultCgroupRequest
)

func cgroupCommand(args []string) int {
	uid := uint32(0)
	if userMode() {
		euid := os.Geteuid()
		if euid < 0 || uint64(euid) > uint64(^uint32(0)) {
			fmt.Printf("invalid user UID %d\n", euid)
			return 1
		}
		uid = uint32(euid)
	}
	request, err := parseCgroupCommand(args, userMode(), uid)
	if err != nil {
		fmt.Println(err)
		fmt.Println("Usage: servicectl [--user] cgroup <status|list|inspect|pids|attach> [UNIT] [PID]")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := doCgroupRequest(ctx, cgroupdSocketPath, request)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	if err := printCgroupResponse(os.Stdout, request, response); err != nil {
		fmt.Println(err)
		return 1
	}
	return 0
}

func parseCgroupCommand(args []string, user bool, uid uint32) (cgrouptrack.Request, error) {
	if len(args) == 0 {
		return cgrouptrack.Request{}, errors.New("cgroup command is required")
	}
	mode := cgrouptrack.ModeSystem
	requestUID := uint32(0)
	if user {
		mode = cgrouptrack.ModeUser
		requestUID = uid
	}
	request := cgrouptrack.Request{Mode: mode, UID: requestUID}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return cgrouptrack.Request{}, errors.New("status accepts no arguments")
		}
		request.Operation = cgrouptrack.OpStatus
		if !user {
			request.Mode = ""
		}
	case "list":
		if len(args) != 1 {
			return cgrouptrack.Request{}, errors.New("list accepts no arguments")
		}
		request.Operation = cgrouptrack.OpListUnits
	case "inspect":
		if len(args) != 2 {
			return cgrouptrack.Request{}, errors.New("inspect requires one unit")
		}
		request.Operation = cgrouptrack.OpGetUnit
		request.Unit = canonicalCgroupUnit(args[1])
	case "pids":
		if len(args) != 2 {
			return cgrouptrack.Request{}, errors.New("pids requires one unit")
		}
		request.Operation = cgrouptrack.OpListPIDs
		request.Unit = canonicalCgroupUnit(args[1])
	case "attach":
		if len(args) != 3 {
			return cgrouptrack.Request{}, errors.New("attach requires one unit and one PID")
		}
		pid, err := strconv.Atoi(args[2])
		if err != nil || pid <= 0 {
			return cgrouptrack.Request{}, errors.New("attach PID must be a positive integer")
		}
		request.Operation = cgrouptrack.OpAttach
		request.Unit = canonicalCgroupUnit(args[1])
		request.PID = pid
	default:
		return cgrouptrack.Request{}, fmt.Errorf("unknown cgroup command %q", args[0])
	}
	if err := request.Validate(); err != nil {
		return cgrouptrack.Request{}, err
	}
	return request, nil
}

func defaultCgroupRequest(ctx context.Context, path string, request cgrouptrack.Request) (cgrouptrack.Response, error) {
	return cgrouptrack.NewClient(path).Do(ctx, request)
}

func queryStatusCgroupUnit(ctx context.Context, path, mode string, uid uint32, unit string, requestFn func(context.Context, string, cgrouptrack.Request) (cgrouptrack.Response, error)) (cgrouptrack.UnitStatus, error) {
	if requestFn == nil {
		requestFn = defaultCgroupRequest
	}
	requestMode := cgrouptrack.ModeSystem
	requestUID := uint32(0)
	if strings.EqualFold(strings.TrimSpace(mode), "user") {
		requestMode = cgrouptrack.ModeUser
		requestUID = uid
	}
	request := cgrouptrack.Request{
		Operation: cgrouptrack.OpGetUnit,
		Mode:      requestMode,
		UID:       requestUID,
		Unit:      canonicalCgroupUnit(unit),
	}
	if err := request.Validate(); err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	response, err := requestFn(ctx, path, request)
	if err != nil {
		return cgrouptrack.UnitStatus{}, err
	}
	if !response.OK {
		if response.Error != nil {
			return cgrouptrack.UnitStatus{}, response.Error
		}
		return cgrouptrack.UnitStatus{}, errors.New("cgroup daemon request failed")
	}
	if response.Unit == nil {
		return cgrouptrack.UnitStatus{}, os.ErrNotExist
	}
	return *response.Unit, nil
}

func printCgroupResponse(output io.Writer, request cgrouptrack.Request, response cgrouptrack.Response) error {
	if !response.OK {
		if response.Error != nil {
			return response.Error
		}
		return errors.New("cgroup daemon request failed")
	}
	switch request.Operation {
	case cgrouptrack.OpStatus:
		if response.Status == nil {
			return errors.New("cgroup daemon returned no status")
		}
		state := "degraded"
		if response.Status.Healthy {
			state = "healthy"
		}
		fmt.Fprintf(output, "State: %s\n", state)
		fmt.Fprintf(output, "Cgroup root: %s\n", response.Status.CgroupRoot)
		if response.Status.LastReconcile != "" {
			fmt.Fprintf(output, "Last reconcile: %s\n", response.Status.LastReconcile)
		}
		fmt.Fprintf(output, "Pending: %d\n", response.Status.Pending)
		fmt.Fprintf(output, "Abnormal: %d\n", response.Status.Abnormal)
	case cgrouptrack.OpListUnits:
		units := append([]cgrouptrack.UnitStatus(nil), response.Units...)
		sort.Slice(units, func(i, j int) bool {
			if units[i].Identity.Mode != units[j].Identity.Mode {
				return units[i].Identity.Mode < units[j].Identity.Mode
			}
			if units[i].Identity.UID != units[j].Identity.UID {
				return units[i].Identity.UID < units[j].Identity.UID
			}
			return units[i].Identity.Unit < units[j].Identity.Unit
		})
		writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "MODE\tUID\tUNIT\tSTATE\tMAINPID\tMEMBERS\tGENERATION")
		for _, unit := range units {
			fmt.Fprintf(writer, "%s\t%d\t%s\t%s\t%d\t%d\t%d\n",
				unit.Identity.Mode, unit.Identity.UID, unit.Identity.Unit, unit.State,
				unit.Identity.MainPID, unit.MemberCount, unit.Identity.Generation)
		}
		return writer.Flush()
	case cgrouptrack.OpGetUnit, cgrouptrack.OpAttach:
		if response.Unit == nil {
			return errors.New("cgroup daemon returned no unit")
		}
		unit := response.Unit
		fmt.Fprintf(output, "Unit: %s\n", unit.Identity.Unit)
		fmt.Fprintf(output, "Mode: %s\n", unit.Identity.Mode)
		fmt.Fprintf(output, "UID: %d\n", unit.Identity.UID)
		fmt.Fprintf(output, "State: %s\n", unit.State)
		fmt.Fprintf(output, "MainPID: %d\n", unit.Identity.MainPID)
		fmt.Fprintf(output, "MainPID starttime: %d\n", unit.Identity.MainPIDStartTime)
		fmt.Fprintf(output, "Generation: %d\n", unit.Identity.Generation)
		fmt.Fprintf(output, "Members: %d\n", unit.MemberCount)
		fmt.Fprintf(output, "Path: %s\n", unit.Path)
		if unit.LastMigration != "" {
			fmt.Fprintf(output, "Last migration: %s\n", unit.LastMigration)
		}
		if unit.LastError != "" {
			fmt.Fprintf(output, "Last error: %s\n", unit.LastError)
		}
	case cgrouptrack.OpListPIDs:
		pids := append([]cgrouptrack.ProcessStatus(nil), response.PIDs...)
		sort.Slice(pids, func(i, j int) bool { return pids[i].PID < pids[j].PID })
		writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "PID\tSTARTTIME\tUID\tCOMM\tROLE")
		for _, process := range pids {
			role := "member"
			if process.MainPID {
				role = "main"
			}
			fmt.Fprintf(writer, "%d\t%d\t%d\t%s\t%s\n", process.PID, process.StartTime, process.UID, process.Comm, role)
		}
		return writer.Flush()
	default:
		return fmt.Errorf("unsupported cgroup response for %q", request.Operation)
	}
	return nil
}

func canonicalCgroupUnit(unit string) string {
	unit = strings.TrimSpace(unit)
	if unit != "" && !strings.HasSuffix(unit, ".service") {
		unit += ".service"
	}
	return unit
}
