package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
)

const (
	defaultJournalSocketPath = "/run/systemd/journal/socket"
	journalFacilityDaemon    = 3
)

type logRoute struct {
	Scope         string
	Unit          string
	Identifier    string
	LoggerService string
	UID           uint32
	GID           uint32
	PID           int
}

type journalField struct {
	Name  string
	Value string
}

func journalFields(route logRoute, priority int, message string) ([]journalField, error) {
	if route.Scope != "system" && route.Scope != "user" {
		return nil, fmt.Errorf("invalid journal route scope %q", route.Scope)
	}
	unit, err := canonicalJournalUnit(route.Unit)
	if err != nil {
		return nil, err
	}
	if err := validateJournalValue("SYSLOG_IDENTIFIER", route.Identifier); err != nil {
		return nil, err
	}
	if err := validateJournalValue("MESSAGE", message); err != nil {
		return nil, err
	}
	if priority < 0 || priority > 7 {
		return nil, fmt.Errorf("journal priority %d is outside 0..7", priority)
	}

	fields := []journalField{
		{Name: "MESSAGE", Value: message},
		{Name: "PRIORITY", Value: fmt.Sprintf("%d", priority)},
		{Name: "SYSLOG_FACILITY", Value: fmt.Sprintf("%d", journalFacilityDaemon)},
		{Name: "SYSLOG_IDENTIFIER", Value: route.Identifier},
	}
	if route.Scope == "system" {
		fields = append(fields, journalField{Name: "OBJECT_SYSTEMD_UNIT", Value: unit})
	} else {
		fields = append(fields, journalField{Name: "OBJECT_SYSTEMD_USER_UNIT", Value: unit})
	}
	return fields, nil
}

func canonicalJournalUnit(raw string) (string, error) {
	unit := strings.TrimSpace(raw)
	if unit == "" {
		return "", fmt.Errorf("journal unit is empty")
	}
	if !strings.HasSuffix(unit, ".service") {
		unit += ".service"
	}
	if strings.ContainsAny(unit, "\x00\r\n\t /\\") {
		return "", fmt.Errorf("journal unit %q contains an invalid character", raw)
	}
	return unit, nil
}

func validateJournalValue(field, value string) error {
	if strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("journal field %s contains a control character", field)
	}
	return nil
}

func encodeJournalDatagram(fields []journalField) ([]byte, error) {
	encoded := make([]byte, 0, 512)
	for _, field := range fields {
		if !validJournalFieldName(field.Name) {
			return nil, fmt.Errorf("invalid journal field name %q", field.Name)
		}
		if strings.ContainsAny(field.Value, "\x00\n") {
			encoded = append(encoded, field.Name...)
			encoded = append(encoded, '\n')
			var length [8]byte
			binary.LittleEndian.PutUint64(length[:], uint64(len(field.Value)))
			encoded = append(encoded, length[:]...)
			encoded = append(encoded, field.Value...)
			encoded = append(encoded, '\n')
			continue
		}
		encoded = append(encoded, field.Name...)
		encoded = append(encoded, '=')
		encoded = append(encoded, field.Value...)
		encoded = append(encoded, '\n')
	}
	if len(encoded) > maxProtocolPacketBytes {
		return nil, fmt.Errorf("journal datagram is %d bytes, limit is %d", len(encoded), maxProtocolPacketBytes)
	}
	return encoded, nil
}

func validJournalFieldName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

type journalWriter struct {
	mu       sync.Mutex
	path     string
	conn     *net.UnixConn
	dialUnix func(string) (*net.UnixConn, error)
}

func newJournalWriter(path string) *journalWriter {
	return &journalWriter{
		path: path,
		dialUnix: func(path string) (*net.UnixConn, error) {
			return net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
		},
	}
}

func (writer *journalWriter) Write(route logRoute, priority int, message string) error {
	fields, err := journalFields(route, priority, message)
	if err != nil {
		return err
	}
	datagram, err := encodeJournalDatagram(fields)
	if err != nil {
		return err
	}

	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.conn == nil {
		writer.conn, err = writer.dialUnix(writer.path)
		if err != nil {
			return err
		}
	}
	written, err := writer.conn.Write(datagram)
	if err != nil || written != len(datagram) {
		writer.closeLocked()
		if err != nil {
			return err
		}
		return fmt.Errorf("short journald datagram write: %d of %d bytes", written, len(datagram))
	}
	return nil
}

func (writer *journalWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.closeLocked()
}

func (writer *journalWriter) closeLocked() error {
	if writer.conn == nil {
		return nil
	}
	err := writer.conn.Close()
	writer.conn = nil
	return err
}
