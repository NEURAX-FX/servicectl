package dbusactivation

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"servicectl/internal/dbusmanager"
)

type fakeRouteResolver struct {
	route Route
	err   error
	calls atomic.Int32
}

func (r *fakeRouteResolver) Resolve(string) (Route, error) {
	r.calls.Add(1)
	return r.route, r.err
}

type fakeNameMonitor struct {
	mu         sync.RWMutex
	owner      string
	ownerErr   error
	changes    chan dbusmanager.NameOwnerChanged
	watchCalls atomic.Int32
	ownerCalls atomic.Int32
	onWatch    func()
}

func (m *fakeNameMonitor) Owner(context.Context, string) (string, error) {
	m.ownerCalls.Add(1)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.owner == "" && m.ownerErr == nil {
		return "", dbusmanager.ErrNoOwner
	}
	return m.owner, m.ownerErr
}

func (m *fakeNameMonitor) setOwner(owner string) {
	m.mu.Lock()
	m.owner = owner
	m.mu.Unlock()
}

func (m *fakeNameMonitor) Watch(context.Context, string) (<-chan dbusmanager.NameOwnerChanged, func(), error) {
	m.watchCalls.Add(1)
	if m.onWatch != nil {
		m.onWatch()
	}
	if m.changes == nil {
		m.changes = make(chan dbusmanager.NameOwnerChanged, 8)
	}
	return m.changes, func() {}, nil
}

func TestEngineRechecksOwnerAfterInstallingWatch(t *testing.T) {
	monitor := &fakeNameMonitor{changes: make(chan dbusmanager.NameOwnerChanged, 1)}
	monitor.onWatch = func() { monitor.setOwner(":1.42") }
	resolver := &fakeRouteResolver{route: Route{Kind: RouteNative}}
	starter := &fakeStarter{watchCalls: &monitor.watchCalls}
	engine := NewEngine(EngineOptions{Monitor: monitor, Resolver: resolver, Starter: starter, Timeout: time.Second})

	result := engine.Activate(context.Background(), ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
	if result.Code != ResultSuccess {
		t.Fatalf("result = %#v", result)
	}
	if starter.calls.Load() != 0 {
		t.Fatalf("starter calls = %d, want 0", starter.calls.Load())
	}
}

type fakeStarter struct {
	calls       atomic.Int32
	stopCalls   atomic.Int32
	started     chan struct{}
	processExit chan error
	err         error
	watchCalls  *atomic.Int32
	environment []string
}

func (s *fakeStarter) Start(_ context.Context, _ Route, environment []string) (StartResult, error) {
	if s.watchCalls != nil && s.watchCalls.Load() == 0 {
		return StartResult{}, errors.New("start happened before owner watch")
	}
	s.calls.Add(1)
	s.environment = append([]string(nil), environment...)
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	return StartResult{
		Exit: s.processExit,
		Stop: func() { s.stopCalls.Add(1) },
	}, s.err
}

func TestEngineUsesEnvironmentBoundToActivationRequest(t *testing.T) {
	monitor := &fakeNameMonitor{changes: make(chan dbusmanager.NameOwnerChanged, 1)}
	resolver := &fakeRouteResolver{route: Route{Kind: RouteNative}}
	starter := &fakeStarter{started: make(chan struct{}, 1), watchCalls: &monitor.watchCalls}
	environments := &EnvironmentStore{}
	environments.Replace(FrontendDaemonHelper, map[string]string{"LANG": "global"})
	engine := NewEngine(EngineOptions{Monitor: monitor, Resolver: resolver, Starter: starter, Environment: environments, Timeout: time.Second})

	resultChannel := make(chan ActivationResult, 1)
	go func() {
		resultChannel <- engine.Activate(context.Background(), ActivateRequest{
			Frontend:       FrontendDaemonHelper,
			BusName:        "org.example.Service",
			environment:    []string{"LANG=bound"},
			environmentSet: true,
		})
	}()
	<-starter.started
	monitor.setOwner(":1.90")
	monitor.changes <- dbusmanager.NameOwnerChanged{Name: "org.example.Service", NewOwner: ":1.90"}
	if result := <-resultChannel; result.Code != ResultSuccess {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(starter.environment, []string{"LANG=bound"}) {
		t.Fatalf("environment = %#v", starter.environment)
	}
}

func TestEngineReturnsImmediatelyWhenNameAlreadyOwned(t *testing.T) {
	monitor := &fakeNameMonitor{owner: ":1.42"}
	resolver := &fakeRouteResolver{route: Route{Kind: RouteNative}}
	starter := &fakeStarter{}
	engine := NewEngine(EngineOptions{Monitor: monitor, Resolver: resolver, Starter: starter, Timeout: time.Second})

	result := engine.Activate(context.Background(), ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})

	if result.Code != ResultSuccess {
		t.Fatalf("result = %#v", result)
	}
	if starter.calls.Load() != 0 || resolver.calls.Load() != 0 || monitor.watchCalls.Load() != 0 {
		t.Fatalf("unexpected calls: starter=%d resolver=%d watch=%d", starter.calls.Load(), resolver.calls.Load(), monitor.watchCalls.Load())
	}
}

func TestEngineDeduplicatesConcurrentActivation(t *testing.T) {
	monitor := &fakeNameMonitor{changes: make(chan dbusmanager.NameOwnerChanged, 8)}
	resolver := &fakeRouteResolver{route: Route{Kind: RouteManaged, Managed: ManagedRoute{Unit: "example"}}}
	starter := &fakeStarter{started: make(chan struct{}, 1), watchCalls: &monitor.watchCalls}
	engine := NewEngine(EngineOptions{Monitor: monitor, Resolver: resolver, Starter: starter, Timeout: time.Second})

	const count = 10
	results := make(chan ActivationResult, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- engine.Activate(context.Background(), ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
		}()
	}
	select {
	case <-starter.started:
	case <-time.After(time.Second):
		t.Fatal("starter was not called")
	}
	monitor.setOwner(":1.77")
	monitor.changes <- dbusmanager.NameOwnerChanged{Name: "org.example.Service", NewOwner: ":1.77"}
	wg.Wait()
	close(results)
	for result := range results {
		if result.Code != ResultSuccess {
			t.Fatalf("result = %#v", result)
		}
	}
	if got := starter.calls.Load(); got != 1 {
		t.Fatalf("starter calls = %d, want 1", got)
	}
	if got := monitor.watchCalls.Load(); got != 1 {
		t.Fatalf("watch calls = %d, want 1", got)
	}
}

func TestEngineReportsStarterFailure(t *testing.T) {
	monitor := &fakeNameMonitor{changes: make(chan dbusmanager.NameOwnerChanged, 1)}
	resolver := &fakeRouteResolver{route: Route{Kind: RouteNative}}
	starter := &fakeStarter{err: errors.New("spawn failed"), watchCalls: &monitor.watchCalls}
	engine := NewEngine(EngineOptions{Monitor: monitor, Resolver: resolver, Starter: starter, Timeout: time.Second})

	result := engine.Activate(context.Background(), ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
	if result.Code != ResultExecFailed || result.Detail != "spawn failed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestEngineReportsChildExitBeforeNameAcquisition(t *testing.T) {
	monitor := &fakeNameMonitor{changes: make(chan dbusmanager.NameOwnerChanged, 1)}
	processExit := make(chan error, 1)
	processExit <- nil
	resolver := &fakeRouteResolver{route: Route{Kind: RouteNative}}
	starter := &fakeStarter{processExit: processExit, watchCalls: &monitor.watchCalls}
	engine := NewEngine(EngineOptions{Monitor: monitor, Resolver: resolver, Starter: starter, Timeout: time.Second})

	result := engine.Activate(context.Background(), ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
	if result.Code != ResultChildExited {
		t.Fatalf("result = %#v", result)
	}
}

func TestEngineRechecksOwnerWhenChildExitRacesNameAcquisition(t *testing.T) {
	monitor := &fakeNameMonitor{changes: make(chan dbusmanager.NameOwnerChanged, 1)}
	processExit := make(chan error, 1)
	resolver := &fakeRouteResolver{route: Route{Kind: RouteNative}}
	starter := &fakeStarter{started: make(chan struct{}, 1), processExit: processExit, watchCalls: &monitor.watchCalls}
	engine := NewEngine(EngineOptions{Monitor: monitor, Resolver: resolver, Starter: starter, Timeout: time.Second})

	resultChannel := make(chan ActivationResult, 1)
	go func() {
		resultChannel <- engine.Activate(context.Background(), ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
	}()
	<-starter.started
	monitor.setOwner(":1.55")
	processExit <- nil
	result := <-resultChannel
	if result.Code != ResultSuccess {
		t.Fatalf("result = %#v", result)
	}
}

func TestEngineTimesOut(t *testing.T) {
	monitor := &fakeNameMonitor{changes: make(chan dbusmanager.NameOwnerChanged)}
	resolver := &fakeRouteResolver{route: Route{Kind: RouteManaged}}
	starter := &fakeStarter{watchCalls: &monitor.watchCalls}
	engine := NewEngine(EngineOptions{Monitor: monitor, Resolver: resolver, Starter: starter, Timeout: 20 * time.Millisecond})

	result := engine.Activate(context.Background(), ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
	if result.Code != ResultTimeout {
		t.Fatalf("result = %#v", result)
	}
	if got := starter.stopCalls.Load(); got != 1 {
		t.Fatalf("stop calls = %d, want 1", got)
	}
}

func TestEngineDoesNotStopServiceAfterNameAcquisition(t *testing.T) {
	monitor := &fakeNameMonitor{changes: make(chan dbusmanager.NameOwnerChanged, 1)}
	resolver := &fakeRouteResolver{route: Route{Kind: RouteNative}}
	starter := &fakeStarter{started: make(chan struct{}, 1), watchCalls: &monitor.watchCalls}
	engine := NewEngine(EngineOptions{Monitor: monitor, Resolver: resolver, Starter: starter, Timeout: time.Second})

	resultChannel := make(chan ActivationResult, 1)
	go func() {
		resultChannel <- engine.Activate(context.Background(), ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
	}()
	<-starter.started
	monitor.setOwner(":1.99")
	monitor.changes <- dbusmanager.NameOwnerChanged{Name: "org.example.Service", NewOwner: ":1.99"}
	if result := <-resultChannel; result.Code != ResultSuccess {
		t.Fatalf("result = %#v", result)
	}
	if got := starter.stopCalls.Load(); got != 0 {
		t.Fatalf("stop calls = %d, want 0", got)
	}
}

func TestEngineCallerCancellationDoesNotCancelSharedJob(t *testing.T) {
	monitor := &fakeNameMonitor{changes: make(chan dbusmanager.NameOwnerChanged, 1)}
	resolver := &fakeRouteResolver{route: Route{Kind: RouteManaged}}
	starter := &fakeStarter{started: make(chan struct{}, 1), watchCalls: &monitor.watchCalls}
	engine := NewEngine(EngineOptions{Monitor: monitor, Resolver: resolver, Starter: starter, Timeout: time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan ActivationResult, 1)
	go func() {
		cancelled <- engine.Activate(ctx, ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
	}()
	select {
	case <-starter.started:
	case <-time.After(time.Second):
		t.Fatal("starter was not called")
	}
	cancel()
	second := make(chan ActivationResult, 1)
	go func() {
		second <- engine.Activate(context.Background(), ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
	}()
	if result := <-cancelled; result.Code != ResultFailed || result.Detail != context.Canceled.Error() {
		t.Fatalf("cancelled result = %#v", result)
	}
	monitor.setOwner(":1.88")
	monitor.changes <- dbusmanager.NameOwnerChanged{Name: "org.example.Service", NewOwner: ":1.88"}
	if result := <-second; result.Code != ResultSuccess {
		t.Fatalf("second result = %#v", result)
	}
	if got := starter.calls.Load(); got != 1 {
		t.Fatalf("starter calls = %d, want 1", got)
	}
}

func TestEngineRemovesCompletedJob(t *testing.T) {
	monitor := &fakeNameMonitor{changes: make(chan dbusmanager.NameOwnerChanged, 2)}
	resolver := &fakeRouteResolver{route: Route{Kind: RouteManaged}}
	starter := &fakeStarter{started: make(chan struct{}, 2), watchCalls: &monitor.watchCalls}
	engine := NewEngine(EngineOptions{Monitor: monitor, Resolver: resolver, Starter: starter, Timeout: time.Second})

	first := make(chan ActivationResult, 1)
	go func() {
		first <- engine.Activate(context.Background(), ActivateRequest{BusName: "org.example.Service", Frontend: FrontendDaemonHelper})
	}()
	<-starter.started
	monitor.setOwner(":1.1")
	monitor.changes <- dbusmanager.NameOwnerChanged{Name: "org.example.Service", NewOwner: ":1.1"}
	if result := <-first; result.Code != ResultSuccess {
		t.Fatal(result)
	}
	monitor.setOwner("")
	second := make(chan ActivationResult, 1)
	go func() {
		second <- engine.Activate(context.Background(), ActivateRequest{BusName: "org.example.Service", Frontend: FrontendDaemonHelper})
	}()
	<-starter.started
	monitor.setOwner(":1.2")
	monitor.changes <- dbusmanager.NameOwnerChanged{Name: "org.example.Service", NewOwner: ":1.2"}
	if result := <-second; result.Code != ResultSuccess {
		t.Fatal(result)
	}
	if got := starter.calls.Load(); got != 2 {
		t.Fatalf("starter calls = %d, want 2", got)
	}
}
