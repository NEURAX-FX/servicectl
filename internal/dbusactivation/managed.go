package dbusactivation

import (
	"context"
	"errors"
)

type ManagedOptions struct {
	Install  func(context.Context, string) error
	Start    func(context.Context, string) error
	Activate func(context.Context, string) error
}

type ManagedStarter struct {
	install  func(context.Context, string) error
	start    func(context.Context, string) error
	activate func(context.Context, string) error
}

func NewManagedStarter(options ManagedOptions) *ManagedStarter {
	return &ManagedStarter{install: options.Install, start: options.Start, activate: options.Activate}
}

func (s *ManagedStarter) Start(ctx context.Context, route Route, _ []string) (StartResult, error) {
	if route.Kind != RouteManaged {
		return StartResult{}, errors.New("managed starter received non-managed route")
	}
	if s.install == nil || s.start == nil {
		return StartResult{}, errors.New("managed starter is not configured")
	}
	if err := s.install(ctx, route.Managed.Unit); err != nil {
		return StartResult{}, err
	}
	serviceName := route.Managed.ServiceName
	if serviceName == "" {
		serviceName = route.Managed.Unit
	}
	if err := s.start(ctx, serviceName); err != nil {
		return StartResult{}, err
	}
	if route.Managed.ControlPath != "" {
		if s.activate == nil {
			return StartResult{}, errors.New("managed activation control is not configured")
		}
		if err := s.activate(ctx, route.Managed.ControlPath); err != nil {
			return StartResult{}, err
		}
	}
	return StartResult{}, nil
}

type CompositeStarter struct {
	Native  Starter
	Managed Starter
}

func (s CompositeStarter) Start(ctx context.Context, route Route, environment []string) (StartResult, error) {
	switch route.Kind {
	case RouteNative:
		if s.Native == nil {
			return StartResult{}, errors.New("native starter is unavailable")
		}
		return s.Native.Start(ctx, route, environment)
	case RouteManaged:
		if s.Managed == nil {
			return StartResult{}, errors.New("managed starter is unavailable")
		}
		return s.Managed.Start(ctx, route, environment)
	default:
		return StartResult{}, errors.New("unknown activation route")
	}
}
