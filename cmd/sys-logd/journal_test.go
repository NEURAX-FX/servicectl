package main

import (
	"bytes"
	"encoding/binary"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJournalFieldsForSystemRoute(t *testing.T) {
	fields, err := journalFields(logRoute{
		Scope:      "system",
		Unit:       "demo.service",
		Identifier: "servicectl[demo]",
	}, 6, "ready")
	if err != nil {
		t.Fatal(err)
	}
	assertJournalField(t, fields, "MESSAGE", "ready")
	assertJournalField(t, fields, "PRIORITY", "6")
	assertJournalField(t, fields, "SYSLOG_FACILITY", "3")
	assertJournalField(t, fields, "SYSLOG_IDENTIFIER", "servicectl[demo]")
	assertJournalField(t, fields, "OBJECT_SYSTEMD_UNIT", "demo.service")
	assertNoJournalField(t, fields, "OBJECT_SYSTEMD_USER_UNIT")
}

func TestJournalFieldsForUserRoute(t *testing.T) {
	fields, err := journalFields(logRoute{
		Scope:      "user",
		Unit:       "demo.service",
		Identifier: "servicectl[demo]",
	}, 5, "warning")
	if err != nil {
		t.Fatal(err)
	}
	assertJournalField(t, fields, "OBJECT_SYSTEMD_USER_UNIT", "demo.service")
	assertNoJournalField(t, fields, "OBJECT_SYSTEMD_UNIT")
}

func TestJournalFieldsRejectInvalidRoute(t *testing.T) {
	for _, route := range []logRoute{
		{Scope: "other", Unit: "demo.service", Identifier: "servicectl[demo]"},
		{Scope: "system", Unit: "demo\n.service", Identifier: "servicectl[demo]"},
		{Scope: "system", Unit: "demo.service", Identifier: "bad\nidentifier"},
	} {
		if _, err := journalFields(route, 6, "line"); err == nil {
			t.Fatalf("journalFields(%#v) succeeded", route)
		}
	}
}

func TestEncodeJournalDatagramUsesBinaryFieldForNewline(t *testing.T) {
	encoded, err := encodeJournalDatagram([]journalField{{Name: "MESSAGE", Value: "first\nsecond"}})
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := []byte("MESSAGE\n")
	if !bytes.HasPrefix(encoded, wantPrefix) {
		t.Fatalf("datagram prefix = %q", encoded)
	}
	length := binary.LittleEndian.Uint64(encoded[len(wantPrefix) : len(wantPrefix)+8])
	if length != uint64(len("first\nsecond")) {
		t.Fatalf("binary field length = %d", length)
	}
	if !bytes.Contains(encoded, []byte("first\nsecond\n")) {
		t.Fatalf("datagram = %q", encoded)
	}
}

func TestJournalWriterSendsNativeDatagram(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "journal.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	writer := newJournalWriter(socketPath)
	defer writer.Close()
	if err := writer.Write(logRoute{Scope: "system", Unit: "demo.service", Identifier: "servicectl[demo]"}, 6, "native token"); err != nil {
		t.Fatal(err)
	}
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4096)
	n, _, err := listener.ReadFromUnix(buffer)
	if err != nil {
		t.Fatal(err)
	}
	got := string(buffer[:n])
	for _, want := range []string{"MESSAGE=native token\n", "OBJECT_SYSTEMD_UNIT=demo.service\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("datagram missing %q: %q", want, got)
		}
	}
}

func assertJournalField(t *testing.T, fields []journalField, name, value string) {
	t.Helper()
	for _, field := range fields {
		if field.Name == name {
			if field.Value != value {
				t.Fatalf("field %s = %q, want %q", name, field.Value, value)
			}
			return
		}
	}
	t.Fatalf("missing journal field %s", name)
}

func assertNoJournalField(t *testing.T, fields []journalField, name string) {
	t.Helper()
	for _, field := range fields {
		if field.Name == name {
			t.Fatalf("unexpected journal field %s=%q", field.Name, field.Value)
		}
	}
}
