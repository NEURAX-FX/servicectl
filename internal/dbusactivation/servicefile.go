package dbusactivation

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

var (
	ErrUnknownService   = errors.New("unknown D-Bus service")
	ErrDuplicateService = errors.New("duplicate D-Bus service")
)

type ServiceDefinition struct {
	Name           string
	Argv           []string
	User           string
	SystemdService string
	Path           string
	Priority       int
}

type Index struct {
	definitions map[string]ServiceDefinition
	duplicates  map[string]error
}

func ParseServiceFile(path string, priority int) (ServiceDefinition, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ServiceDefinition{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return ServiceDefinition{}, fmt.Errorf("%s: service file must be a non-writable regular file", path)
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ServiceDefinition{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return ServiceDefinition{}, err
	}
	stat, ok := openedInfo.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(info, openedInfo) || stat.Uid != uint32(os.Geteuid()) || openedInfo.Mode().Perm()&0o022 != 0 {
		return ServiceDefinition{}, fmt.Errorf("%s: service file changed or has an unsafe owner or mode", path)
	}

	definition := ServiceDefinition{Path: path, Priority: priority}
	section := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section != "D-BUS Service" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return ServiceDefinition{}, fmt.Errorf("%s: invalid line %q", path, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "Name":
			definition.Name = value
		case "Exec":
			definition.Argv, err = splitCommandLine(value)
			if err != nil {
				return ServiceDefinition{}, fmt.Errorf("%s: Exec: %w", path, err)
			}
		case "User":
			definition.User = value
		case "SystemdService":
			definition.SystemdService = value
		}
	}
	if err := scanner.Err(); err != nil {
		return ServiceDefinition{}, err
	}
	if section == "" {
		return ServiceDefinition{}, fmt.Errorf("%s: missing [D-BUS Service] section", path)
	}
	if err := ValidateBusName(definition.Name); err != nil {
		return ServiceDefinition{}, fmt.Errorf("%s: %w", path, err)
	}
	if strings.TrimSpace(definition.User) == "" && strings.TrimSpace(definition.SystemdService) == "" {
		return ServiceDefinition{}, fmt.Errorf("%s: system service is missing User", path)
	}
	if len(definition.Argv) == 0 && strings.TrimSpace(definition.SystemdService) == "" {
		return ServiceDefinition{}, fmt.Errorf("%s: service has neither Exec nor SystemdService", path)
	}
	return definition, nil
}

func BuildIndex(directories []string) (*Index, []error) {
	index := &Index{
		definitions: make(map[string]ServiceDefinition),
		duplicates:  make(map[string]error),
	}
	var errs []error
	for priority, directory := range directories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("read service directory %s: %w", directory, err))
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".service") {
				continue
			}
			definition, err := ParseServiceFile(filepath.Join(directory, entry.Name()), priority)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if duplicate, exists := index.duplicates[definition.Name]; exists {
				_ = duplicate
				continue
			}
			existing, exists := index.definitions[definition.Name]
			if !exists {
				index.definitions[definition.Name] = definition
				continue
			}
			if existing.Priority < definition.Priority {
				continue
			}
			if existing.Priority == definition.Priority {
				duplicate := fmt.Errorf("%w: %s is defined by %s and %s", ErrDuplicateService, definition.Name, existing.Path, definition.Path)
				delete(index.definitions, definition.Name)
				index.duplicates[definition.Name] = duplicate
				errs = append(errs, duplicate)
				continue
			}
			index.definitions[definition.Name] = definition
		}
	}
	return index, errs
}

func (i *Index) Lookup(name string) (ServiceDefinition, error) {
	if i == nil {
		return ServiceDefinition{}, ErrUnknownService
	}
	if err, exists := i.duplicates[name]; exists {
		return ServiceDefinition{}, err
	}
	definition, exists := i.definitions[name]
	if !exists {
		return ServiceDefinition{}, fmt.Errorf("%w: %s", ErrUnknownService, name)
	}
	definition.Argv = append([]string(nil), definition.Argv...)
	return definition, nil
}

func (i *Index) Names() []string {
	if i == nil {
		return nil
	}
	names := make([]string, 0, len(i.definitions))
	for name := range i.definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func splitCommandLine(command string) ([]string, error) {
	var result []string
	var current strings.Builder
	var quote rune
	escaped := false
	hasToken := false
	flush := func() {
		if hasToken {
			result = append(result, current.String())
			current.Reset()
			hasToken = false
		}
	}
	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			hasToken = true
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			hasToken = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			hasToken = true
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			hasToken = true
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			current.WriteRune(r)
			hasToken = true
		}
	}
	if escaped {
		return nil, errors.New("trailing escape")
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	flush()
	if len(result) == 0 {
		return nil, errors.New("empty command")
	}
	return result, nil
}
