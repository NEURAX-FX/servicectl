package dbusactivation

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseServiceFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "org.example.Service.service")
	content := `[D-BUS Service]
# comment
Name=org.example.Service
Exec=/usr/libexec/example --label "hello world" 'single value' escaped\ value
User=example
SystemdService=example.service
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ParseServiceFile(path, 2)
	if err != nil {
		t.Fatalf("ParseServiceFile: %v", err)
	}
	if got.Name != "org.example.Service" || got.User != "example" || got.SystemdService != "example.service" || got.Path != path || got.Priority != 2 {
		t.Fatalf("definition = %#v", got)
	}
	wantArgv := []string{"/usr/libexec/example", "--label", "hello world", "single value", "escaped value"}
	if !reflect.DeepEqual(got.Argv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", got.Argv, wantArgv)
	}
}

func TestParseServiceFileRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "missing section", content: "Name=org.example.Service\nExec=/bin/true\n"},
		{name: "missing name", content: "[D-BUS Service]\nExec=/bin/true\n"},
		{name: "missing user", content: "[D-BUS Service]\nName=org.example.Service\nExec=/bin/true\n"},
		{name: "invalid name", content: "[D-BUS Service]\nName=invalid\nExec=/bin/true\n"},
		{name: "missing activation", content: "[D-BUS Service]\nName=org.example.Service\n"},
		{name: "unterminated quote", content: "[D-BUS Service]\nName=org.example.Service\nExec=/bin/true 'bad\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.service")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ParseServiceFile(path, 0); err == nil {
				t.Fatal("ParseServiceFile unexpectedly succeeded")
			}
		})
	}
}

func TestParseServiceFileAllowsSystemdOnlyServiceWithoutUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "org.example.Service.service")
	content := "[D-BUS Service]\nName=org.example.Service\nExec=/bin/false\nSystemdService=org.example.Service.service\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := ParseServiceFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if definition.SystemdService != "org.example.Service.service" || definition.User != "" {
		t.Fatalf("definition = %#v", definition)
	}
}

func TestParseServiceFileRejectsSymlinkAndWritableFile(t *testing.T) {
	directory := t.TempDir()
	content := []byte("[D-BUS Service]\nName=org.example.Service\nExec=/bin/true\nUser=root\n")
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.service")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseServiceFile(link, 0); err == nil {
		t.Fatal("symlink service file was accepted")
	}
	writable := filepath.Join(directory, "writable.service")
	if err := os.WriteFile(writable, content, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseServiceFile(writable, 0); err == nil {
		t.Fatal("group/other-writable service file was accepted")
	}
}

func TestBuildIndexUsesDirectoryPriority(t *testing.T) {
	high := t.TempDir()
	low := t.TempDir()
	writeServiceDefinition(t, low, "low.service", "org.example.Service", "/bin/low")
	writeServiceDefinition(t, high, "high.service", "org.example.Service", "/bin/high")

	index, errs := BuildIndex([]string{high, low})
	if len(errs) != 0 {
		t.Fatalf("BuildIndex errors: %v", errs)
	}
	got, err := index.Lookup("org.example.Service")
	if err != nil {
		t.Fatal(err)
	}
	if got.Argv[0] != "/bin/high" || got.Priority != 0 {
		t.Fatalf("selected definition = %#v", got)
	}
}

func TestBuildIndexRejectsSamePriorityDuplicates(t *testing.T) {
	dir := t.TempDir()
	writeServiceDefinition(t, dir, "one.service", "org.example.Service", "/bin/one")
	writeServiceDefinition(t, dir, "two.service", "org.example.Service", "/bin/two")

	index, errs := BuildIndex([]string{dir})
	if len(errs) != 1 {
		t.Fatalf("BuildIndex errors = %v, want one duplicate error", errs)
	}
	if _, err := index.Lookup("org.example.Service"); !errors.Is(err, ErrDuplicateService) {
		t.Fatalf("Lookup error = %v, want ErrDuplicateService", err)
	}
}

func TestIndexLookupReturnsDeepCopy(t *testing.T) {
	dir := t.TempDir()
	writeServiceDefinition(t, dir, "service.service", "org.example.Service", "/bin/true")
	index, errs := BuildIndex([]string{dir})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	first, _ := index.Lookup("org.example.Service")
	first.Argv[0] = "mutated"
	second, _ := index.Lookup("org.example.Service")
	if second.Argv[0] != "/bin/true" {
		t.Fatalf("index was mutated: %#v", second)
	}
}

func writeServiceDefinition(t *testing.T, dir, filename, name, command string) {
	t.Helper()
	content := "[D-BUS Service]\nName=" + name + "\nExec=" + command + "\nUser=root\n"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
