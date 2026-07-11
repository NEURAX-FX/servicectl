package dbusactivation

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

type NativeOptions struct {
	LookupUser func(string) (*user.User, error)
	GroupIDs   func(*user.User) ([]int, error)
	BusAddress string
}

type NativeStarter struct {
	lookupUser func(string) (*user.User, error)
	groupIDs   func(*user.User) ([]int, error)
	busAddress string
}

func NewNativeStarter(options NativeOptions) *NativeStarter {
	lookup := options.LookupUser
	if lookup == nil {
		lookup = user.Lookup
	}
	groups := options.GroupIDs
	if groups == nil {
		groups = defaultGroupIDs
	}
	address := strings.TrimSpace(options.BusAddress)
	if address == "" {
		address = "unix:path=/run/dbus/system_bus_socket"
	}
	return &NativeStarter{lookupUser: lookup, groupIDs: groups, busAddress: address}
}

func (s *NativeStarter) Start(ctx context.Context, route Route, environment []string) (StartResult, error) {
	if route.Kind != RouteNative {
		return StartResult{}, errors.New("native starter received non-native route")
	}
	cmd, err := s.command(ctx, route.Native, environment)
	if err != nil {
		return StartResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return StartResult{}, err
	}
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
		close(exited)
	}()
	return StartResult{
		Exit: exited,
		Stop: func() {
			_ = cmd.Process.Kill()
		},
	}, nil
}

func (s *NativeStarter) command(ctx context.Context, route NativeRoute, environment []string) (*exec.Cmd, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(route.Argv) == 0 || strings.TrimSpace(route.Argv[0]) == "" {
		return nil, errors.New("native activation command is empty")
	}
	if !strings.HasPrefix(route.Argv[0], "/") {
		return nil, errors.New("native activation command must use an absolute executable path")
	}
	cmd := exec.Command(route.Argv[0], route.Argv[1:]...)
	cmd.Env = activationEnvironment(environment, s.busAddress)
	cmd.Dir = "/"
	cmd.Stdin = nil
	if strings.TrimSpace(route.User) != "" {
		account, err := s.lookupUser(route.User)
		if err != nil {
			return nil, fmt.Errorf("lookup activation user %q: %w", route.User, err)
		}
		uid, err := strconv.ParseUint(account.Uid, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("parse uid %q: %w", account.Uid, err)
		}
		gid, err := strconv.ParseUint(account.Gid, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("parse gid %q: %w", account.Gid, err)
		}
		groups, err := s.groupIDs(account)
		if err != nil {
			return nil, fmt.Errorf("resolve supplementary groups for %q: %w", route.User, err)
		}
		credential := &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
		for _, group := range groups {
			if group < 0 {
				return nil, fmt.Errorf("invalid supplementary gid %d", group)
			}
			credential.Groups = append(credential.Groups, uint32(group))
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	}
	return cmd, nil
}

func activationEnvironment(values []string, busAddress string) []string {
	result := make(map[string]string, len(values)+2)
	for _, entry := range values {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			result[key] = value
		}
	}
	result["DBUS_STARTER_ADDRESS"] = busAddress
	result["DBUS_STARTER_BUS_TYPE"] = "system"
	keys := make([]string, 0, len(result))
	for key := range result {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+result[key])
	}
	return environment
}

func defaultGroupIDs(account *user.User) ([]int, error) {
	values, err := account.GroupIds()
	if err != nil {
		return nil, err
	}
	groups := make([]int, 0, len(values))
	for _, value := range values {
		gid, err := strconv.Atoi(value)
		if err != nil {
			return nil, err
		}
		groups = append(groups, gid)
	}
	return groups, nil
}

func resultFromChildExit(err error) ActivationResult {
	if err == nil {
		return ActivationResult{Code: ResultChildExited, Detail: "service exited before acquiring its D-Bus name"}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return ActivationResult{Code: ResultChildSignaled, Detail: err.Error()}
		}
	}
	return ActivationResult{Code: ResultChildExited, Detail: err.Error()}
}
