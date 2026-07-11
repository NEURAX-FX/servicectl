package dbusactivation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"servicectl/internal/dbusmanager"
)

type RouteResolver interface {
	Resolve(string) (Route, error)
}

type NameMonitor interface {
	Owner(context.Context, string) (string, error)
	Watch(context.Context, string) (<-chan dbusmanager.NameOwnerChanged, func(), error)
}

type Starter interface {
	Start(context.Context, Route, []string) (StartResult, error)
}

type StartResult struct {
	Exit <-chan error
	Stop func()
}

type EngineOptions struct {
	Monitor     NameMonitor
	Resolver    RouteResolver
	Starter     Starter
	Environment *EnvironmentStore
	Timeout     time.Duration
}

type activationJob struct {
	done   chan struct{}
	result ActivationResult
}

type Engine struct {
	monitor     NameMonitor
	resolver    RouteResolver
	starter     Starter
	environment *EnvironmentStore
	timeout     time.Duration

	mu   sync.Mutex
	jobs map[string]*activationJob
}

func NewEngine(options EngineOptions) *Engine {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	environment := options.Environment
	if environment == nil {
		environment = &EnvironmentStore{}
	}
	return &Engine{
		monitor:     options.Monitor,
		resolver:    options.Resolver,
		starter:     options.Starter,
		environment: environment,
		timeout:     timeout,
		jobs:        make(map[string]*activationJob),
	}
}

func (e *Engine) Activate(ctx context.Context, request ActivateRequest) ActivationResult {
	if err := ValidateBusName(request.BusName); err != nil {
		return ActivationResult{Code: ResultInvalidBusName, Detail: err.Error()}
	}
	if e.monitor == nil || e.resolver == nil || e.starter == nil {
		return ActivationResult{Code: ResultBackendUnavailable, Detail: "activation engine is not configured"}
	}
	owner, err := e.monitor.Owner(ctx, request.BusName)
	if err == nil && owner != "" {
		return ActivationResult{Code: ResultSuccess}
	}
	if err != nil && !errors.Is(err, dbusmanager.ErrNoOwner) {
		return ActivationResult{Code: ResultBackendUnavailable, Detail: err.Error()}
	}

	e.mu.Lock()
	job := e.jobs[request.BusName]
	if job == nil {
		job = &activationJob{done: make(chan struct{})}
		e.jobs[request.BusName] = job
		go e.runJob(request, job)
	}
	e.mu.Unlock()

	select {
	case <-job.done:
		return job.result
	case <-ctx.Done():
		return ActivationResult{Code: ResultFailed, Detail: ctx.Err().Error()}
	}
}

func (e *Engine) runJob(request ActivateRequest, job *activationJob) {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()

	result := e.activateJob(ctx, request)
	e.mu.Lock()
	job.result = result
	close(job.done)
	delete(e.jobs, request.BusName)
	e.mu.Unlock()
}

func (e *Engine) activateJob(ctx context.Context, request ActivateRequest) ActivationResult {
	busName := request.BusName
	route, err := e.resolver.Resolve(busName)
	if err != nil {
		return resultFromError(err)
	}
	changes, stop, err := e.monitor.Watch(ctx, busName)
	if err != nil {
		return ActivationResult{Code: ResultBackendUnavailable, Detail: err.Error()}
	}
	defer stop()
	owner, err := e.monitor.Owner(ctx, busName)
	if err == nil && owner != "" {
		return ActivationResult{Code: ResultSuccess}
	}
	if err != nil && !errors.Is(err, dbusmanager.ErrNoOwner) {
		return ActivationResult{Code: ResultBackendUnavailable, Detail: err.Error()}
	}

	environment := append([]string(nil), request.environment...)
	if !request.environmentSet {
		_, environment = e.environment.Snapshot(request.Frontend)
	}
	started, err := e.starter.Start(ctx, route, environment)
	if err != nil {
		return ActivationResult{Code: ResultExecFailed, Detail: err.Error()}
	}
	succeeded := false
	defer func() {
		if !succeeded && started.Stop != nil {
			started.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ActivationResult{Code: ResultTimeout, Detail: ctx.Err().Error()}
			}
			return ActivationResult{Code: ResultFailed, Detail: ctx.Err().Error()}
		case change, ok := <-changes:
			if !ok {
				return ActivationResult{Code: ResultBackendUnavailable, Detail: "D-Bus name monitor stopped"}
			}
			if change.Name == busName && change.NewOwner != "" {
				succeeded = true
				return ActivationResult{Code: ResultSuccess}
			}
		case exitErr, ok := <-started.Exit:
			owner, ownerErr := e.monitor.Owner(ctx, busName)
			if ownerErr == nil && owner != "" {
				succeeded = true
				return ActivationResult{Code: ResultSuccess}
			}
			if ownerErr != nil && !errors.Is(ownerErr, dbusmanager.ErrNoOwner) {
				return ActivationResult{Code: ResultBackendUnavailable, Detail: ownerErr.Error()}
			}
			if !ok || exitErr == nil {
				return resultFromChildExit(nil)
			}
			return resultFromChildExit(exitErr)
		}
	}
}

func resultFromError(err error) ActivationResult {
	switch {
	case errors.Is(err, ErrUnknownService):
		return ActivationResult{Code: ResultUnknownService, Detail: err.Error()}
	case errors.Is(err, ErrDuplicateService):
		return ActivationResult{Code: ResultDuplicateService, Detail: err.Error()}
	case errors.Is(err, ErrUnitNotFound):
		return ActivationResult{Code: ResultUnitNotFound, Detail: err.Error()}
	case errors.Is(err, ErrAmbiguousUnit):
		return ActivationResult{Code: ResultServiceInvalid, Detail: err.Error()}
	default:
		return ActivationResult{Code: ResultFailed, Detail: fmt.Sprint(err)}
	}
}
