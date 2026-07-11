package cgrouptrack

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"servicectl/internal/procinfo"
)

func TestAuthorizeScopesUserPeer(t *testing.T) {
	proc := &authProc{namespace: FileIdentity{Device: 1, Inode: 2}}
	peer := Peer{PID: 40, UID: 1000, PIDNamespace: proc.namespace}
	if _, err := authorizeRequest(peer, Request{Operation: OpListUnits, Mode: ModeUser, UID: 1000}, proc.namespace, proc); err != nil {
		t.Fatal(err)
	}
	if _, err := authorizeRequest(peer, Request{Operation: OpListUnits, Mode: ModeUser, UID: 1001}, proc.namespace, proc); err == nil {
		t.Fatal("cross-uid request accepted")
	}
	if _, err := authorizeRequest(peer, Request{Operation: OpListUnits, Mode: ModeSystem}, proc.namespace, proc); err == nil {
		t.Fatal("unprivileged system request accepted")
	}
	if _, err := authorizeRequest(peer, Request{Operation: OpStatus}, proc.namespace, proc); err == nil {
		t.Fatal("unprivileged global status accepted")
	}
}

func TestAuthorizeRootMayUseGlobalAndSystemScopes(t *testing.T) {
	proc := &authProc{namespace: FileIdentity{Device: 1, Inode: 2}}
	peer := Peer{PID: 40, UID: 0, PIDNamespace: proc.namespace}
	for _, request := range []Request{
		{Operation: OpStatus},
		{Operation: OpListUnits, Mode: ModeSystem},
		{Operation: OpGetUnit, Mode: ModeUser, UID: 1000, Unit: "demo.service"},
	} {
		if _, err := authorizeRequest(peer, request, proc.namespace, proc); err != nil {
			t.Fatalf("request %#v: %v", request, err)
		}
	}
}

func TestAuthorizeRejectsDifferentPIDNamespace(t *testing.T) {
	proc := &authProc{namespace: FileIdentity{Device: 1, Inode: 2}}
	peer := Peer{PID: 40, UID: 1000, PIDNamespace: FileIdentity{Device: 1, Inode: 3}}
	if _, err := authorizeRequest(peer, Request{Operation: OpListUnits, Mode: ModeUser, UID: 1000}, proc.namespace, proc); !errors.Is(err, ErrPIDNamespaceMismatch) {
		t.Fatalf("error = %v", err)
	}
}

func TestAuthorizeUserAttachOwnershipAndCgroupBoundary(t *testing.T) {
	proc := &authProc{
		namespace: FileIdentity{Device: 1, Inode: 2},
		processes: map[int]Process{
			42: {PID: 42, UID: 1000, StartTime: 100, Cgroup: "/untracked"},
			43: {PID: 43, UID: 1001, StartTime: 101, Cgroup: "/untracked"},
			44: {PID: 44, UID: 1000, StartTime: 102, Cgroup: "/servicectl.slice/system/demo"},
			45: {PID: 45, UID: 1000, StartTime: 103, Cgroup: "/servicectl.slice/user/1001/demo"},
			46: {PID: 46, UID: 1000, StartTime: 104, Cgroup: "/servicectl.slice/user/1000/old"},
		},
	}
	peer := Peer{PID: 40, UID: 1000, PIDNamespace: proc.namespace}
	request := Request{Operation: OpAttach, Mode: ModeUser, UID: 1000, Unit: "demo.service"}
	for _, pid := range []int{42, 46} {
		request.PID = pid
		if _, err := authorizeRequest(peer, request, proc.namespace, proc); err != nil {
			t.Fatalf("PID %d: %v", pid, err)
		}
	}
	for _, pid := range []int{43, 44, 45} {
		request.PID = pid
		if _, err := authorizeRequest(peer, request, proc.namespace, proc); err == nil {
			t.Fatalf("PID %d was accepted", pid)
		}
	}
}

func TestManagedProcessScopeSupportsHierarchyRoot(t *testing.T) {
	mode, uid, managed := managedProcessScope("/system/demo", "/")
	if !managed || mode != ModeSystem || uid != 0 {
		t.Fatalf("mode=%q uid=%d managed=%v", mode, uid, managed)
	}
	mode, uid, managed = managedProcessScope("/user/1000/demo", "/")
	if !managed || mode != ModeUser || uid != 1000 {
		t.Fatalf("mode=%q uid=%d managed=%v", mode, uid, managed)
	}
}

func TestServerPreservesExplicitHierarchyRoot(t *testing.T) {
	server := NewServer(ServerOptions{ManagedCgroupPath: "/"})
	if server.managedPath != "/" {
		t.Fatalf("managed path = %q", server.managedPath)
	}
}

func TestServerClientRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sys-cgroupd.sock")
	proc := &authProc{namespace: FileIdentity{Device: 1, Inode: 2}}
	namespace := proc.namespace
	service := &fakeProtocolService{status: DaemonStatus{Healthy: true, CgroupRoot: "/test"}}
	server := NewServer(ServerOptions{
		Path: path, Service: service, Proc: proc, RequestTimeout: time.Second,
		ResolvePeer: func(*net.UnixConn, ProcFS) (Peer, error) {
			return Peer{PID: os.Getpid(), UID: 0, GID: 0, PIDNamespace: namespace}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ctx) }()
	waitForServerSocket(t, path)

	client := NewClient(path)
	response, err := client.Do(context.Background(), Request{Operation: OpStatus})
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Status == nil || response.Status.CgroupRoot != "/test" {
		t.Fatalf("response = %#v", response)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServerPerUIDAndAttachLimits(t *testing.T) {
	server := NewServer(ServerOptions{MaxRequestsPerUID: 1})
	if !server.acquireUID(1000) {
		t.Fatal("first UID request was rejected")
	}
	if server.acquireUID(1000) {
		t.Fatal("second concurrent UID request was accepted")
	}
	server.releaseUID(1000)
	if !server.acquireUID(1000) {
		t.Fatal("UID request was not released")
	}
	server.releaseUID(1000)

	now := time.Unix(100, 0)
	for i := 0; i < 4; i++ {
		if !server.allowAttach(1000, now) {
			t.Fatalf("attach token %d was rejected", i)
		}
	}
	if server.allowAttach(1000, now) {
		t.Fatal("attach burst limit was not enforced")
	}
	if !server.allowAttach(1000, now.Add(time.Second)) {
		t.Fatal("attach token did not refill")
	}
}

type authProc struct {
	namespace FileIdentity
	processes map[int]Process
}

func (p *authProc) Inspect(pid int) (Process, error) {
	process, ok := p.processes[pid]
	if !ok {
		return Process{}, errors.New("missing process")
	}
	process.PIDFD = -1
	return process, nil
}
func (p *authProc) PIDNamespace(int) (FileIdentity, error)  { return p.namespace, nil }
func (p *authProc) SelfPIDNamespace() (FileIdentity, error) { return p.namespace, nil }
func (p *authProc) ReadStat(int) (procinfo.Stat, error)     { panic("unused") }
func (p *authProc) ReadStatus(int) (ProcStatus, error)      { panic("unused") }
func (p *authProc) ReadCgroup(int) (string, error)          { panic("unused") }
func (p *authProc) ReadExecutable(int) (string, error)      { panic("unused") }
func (p *authProc) ListPIDs() ([]int, error)                { panic("unused") }
func (p *authProc) OpenPIDFD(int) (int, error)              { panic("unused") }

type fakeProtocolService struct {
	status DaemonStatus
}

func (s *fakeProtocolService) Status(context.Context, Scope) (DaemonStatus, error) {
	return s.status, nil
}
func (s *fakeProtocolService) ListUnits(context.Context, Scope) ([]UnitStatus, error) {
	return nil, nil
}
func (s *fakeProtocolService) GetUnit(context.Context, Scope, string) (UnitStatus, error) {
	return UnitStatus{}, nil
}
func (s *fakeProtocolService) ListPIDs(context.Context, Scope, string) ([]ProcessStatus, error) {
	return nil, nil
}
func (s *fakeProtocolService) Attach(context.Context, Scope, string, int) (UnitStatus, error) {
	return UnitStatus{}, nil
}

func waitForServerSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket did not appear: %s", path)
}
