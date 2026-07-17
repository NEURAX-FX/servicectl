package dbusactivation

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var (
	ErrUnitNotFound  = errors.New("unit not found")
	ErrAmbiguousUnit = errors.New("ambiguous unit")
)

type RouteKind uint8

const (
	RouteManaged RouteKind = iota + 1
	RouteNative
)

type ManagedRoute struct {
	Unit        string
	ServiceName string
	ControlPath string
}

type NativeRoute struct {
	Argv []string
	User string
}

type Route struct {
	Kind    RouteKind
	Managed ManagedRoute
	Native  NativeRoute
}

type UnitResolver interface {
	ResolveExplicit(string) (ManagedRoute, error)
	ResolveBusName(string) ([]ManagedRoute, error)
}

func SelectRoute(definition ServiceDefinition, resolver UnitResolver) (Route, error) {
	if strings.TrimSpace(definition.SystemdService) != "" {
		route, err := resolver.ResolveExplicit(definition.SystemdService)
		if err != nil {
			return Route{}, err
		}
		return Route{Kind: RouteManaged, Managed: normalizeManagedRoute(route)}, nil
	}
	if route, ok := LegacyManagedRoute(definition.Argv); ok {
		if resolver != nil {
			resolved, err := resolver.ResolveExplicit(route.Unit)
			if err == nil {
				return Route{Kind: RouteManaged, Managed: normalizeManagedRoute(resolved)}, nil
			}
			if !errors.Is(err, ErrUnitNotFound) {
				return Route{}, err
			}
		}
		return Route{Kind: RouteManaged, Managed: route}, nil
	}
	if resolver != nil {
		matches, err := resolver.ResolveBusName(definition.Name)
		if err != nil {
			return Route{}, err
		}
		switch len(matches) {
		case 0:
		case 1:
			return Route{Kind: RouteManaged, Managed: normalizeManagedRoute(matches[0])}, nil
		default:
			return Route{}, fmt.Errorf("%w: BusName %s matched %d units", ErrAmbiguousUnit, definition.Name, len(matches))
		}
	}
	if len(definition.Argv) == 0 {
		return Route{}, fmt.Errorf("%w: %s", ErrUnknownService, definition.Name)
	}
	return Route{
		Kind: RouteNative,
		Native: NativeRoute{
			Argv: append([]string(nil), definition.Argv...),
			User: definition.User,
		},
	}, nil
}

func LegacyManagedRoute(argv []string) (ManagedRoute, bool) {
	if len(argv) != 3 || argv[1] != "activate-dbus" {
		return ManagedRoute{}, false
	}
	cleanExecutable := filepath.Clean(argv[0])
	if cleanExecutable != "/usr/bin/servicectl" && cleanExecutable != "/usr/local/bin/servicectl" {
		return ManagedRoute{}, false
	}
	unit := normalizeUnit(argv[2])
	if unit == "" {
		return ManagedRoute{}, false
	}
	return ManagedRoute{Unit: unit}, true
}

func normalizeManagedRoute(route ManagedRoute) ManagedRoute {
	route.Unit = normalizeUnit(route.Unit)
	return route
}

func normalizeUnit(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".service")
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return ""
	}
	return name
}
