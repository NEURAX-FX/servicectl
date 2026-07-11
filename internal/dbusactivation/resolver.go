package dbusactivation

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SystemdUnitResolver struct {
	paths      []string
	runtimeDir string
}

func NewSystemdUnitResolver(paths []string, runtimeDir string) *SystemdUnitResolver {
	return &SystemdUnitResolver{paths: append([]string(nil), paths...), runtimeDir: runtimeDir}
}

func (r *SystemdUnitResolver) ResolveExplicit(name string) (ManagedRoute, error) {
	unit := normalizeUnit(name)
	if unit == "" {
		return ManagedRoute{}, ErrUnitNotFound
	}
	if _, err := r.findUnit(unit); err != nil {
		return ManagedRoute{}, err
	}
	return r.route(unit), nil
}

func (r *SystemdUnitResolver) ResolveBusName(busName string) ([]ManagedRoute, error) {
	if err := ValidateBusName(busName); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var matches []ManagedRoute
	for _, directory := range r.paths {
		entries, err := os.ReadDir(directory)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".service") {
				continue
			}
			unit := normalizeUnit(entry.Name())
			if seen[unit] {
				continue
			}
			seen[unit] = true
			metadata, err := parseUnitMetadata(filepath.Join(directory, entry.Name()))
			if err != nil {
				continue
			}
			if metadata.BusName == busName {
				matches = append(matches, r.route(unit))
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Unit < matches[j].Unit })
	return matches, nil
}

func (r *SystemdUnitResolver) route(unit string) ManagedRoute {
	serviceName := unit + "-dbusd"
	return ManagedRoute{
		Unit:        unit,
		ServiceName: serviceName,
		ControlPath: filepath.Join(r.runtimeDir, serviceName, "control.sock"),
	}
}

func (r *SystemdUnitResolver) findUnit(unit string) (unitMetadata, error) {
	for _, directory := range r.paths {
		path := filepath.Join(directory, unit+".service")
		metadata, err := parseUnitMetadata(path)
		if err == nil {
			return metadata, nil
		}
		if !os.IsNotExist(err) {
			return unitMetadata{}, err
		}
	}
	return unitMetadata{}, fmt.Errorf("%w: %s.service", ErrUnitNotFound, unit)
}

type unitMetadata struct {
	Type    string
	BusName string
}

func parseUnitMetadata(path string) (unitMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return unitMetadata{}, err
	}
	defer file.Close()
	var metadata unitMetadata
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section != "Service" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Type":
			metadata.Type = strings.TrimSpace(value)
		case "BusName":
			metadata.BusName = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return unitMetadata{}, err
	}
	return metadata, nil
}

type DefinitionResolver struct {
	Index func() *Index
	Units UnitResolver
}

func (r DefinitionResolver) Resolve(busName string) (Route, error) {
	if r.Index == nil {
		return Route{}, ErrUnknownService
	}
	definition, err := r.Index().Lookup(busName)
	if err != nil {
		return Route{}, err
	}
	return SelectRoute(definition, r.Units)
}
