package dbusactivation

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

func TestPacketRoundTrip(t *testing.T) {
	tests := []Packet{
		{Type: MessageHello, RequestID: 1, Payload: []byte{byte(FrontendDaemonHelper)}},
		{Type: MessageSetEnvironment, RequestID: 2, Payload: []byte("env")},
		{Type: MessageActivate, RequestID: 3, Payload: []byte("activate")},
		{Type: MessageActivationResult, RequestID: 4, Payload: []byte("result")},
		{Type: MessageCancel, RequestID: 5},
		{Type: MessageReload, RequestID: 6},
		{Type: MessageStatus, RequestID: 7},
		{Type: MessagePing, RequestID: 8},
	}

	for _, want := range tests {
		var buf bytes.Buffer
		if err := WritePacket(&buf, want); err != nil {
			t.Fatalf("WritePacket(%v): %v", want.Type, err)
		}
		if got := buf.Len(); got != HeaderSize+len(want.Payload) {
			t.Fatalf("packet size = %d, want %d", got, HeaderSize+len(want.Payload))
		}
		got, err := ReadPacket(&buf)
		if err != nil {
			t.Fatalf("ReadPacket(%v): %v", want.Type, err)
		}
		if got.Type != want.Type || got.RequestID != want.RequestID || got.Flags != want.Flags || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("round trip = %#v, want %#v", got, want)
		}
	}
}

func TestPacketHeaderLayout(t *testing.T) {
	var buf bytes.Buffer
	packet := Packet{Type: MessagePing, RequestID: 0x0102030405060708, Payload: []byte("abc")}
	if err := WritePacket(&buf, packet); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if !bytes.Equal(b[:8], []byte{'S', 'D', 'B', 'U', 'S', 0, 0, 1}) {
		t.Fatalf("magic = %v", b[:8])
	}
	if got := binary.BigEndian.Uint16(b[8:10]); got != ProtocolVersion {
		t.Fatalf("version = %d", got)
	}
	if got := binary.BigEndian.Uint16(b[10:12]); got != uint16(MessagePing) {
		t.Fatalf("type = %d", got)
	}
	if got := binary.BigEndian.Uint64(b[12:20]); got != packet.RequestID {
		t.Fatalf("request id = %#x", got)
	}
	if got := binary.BigEndian.Uint32(b[20:24]); got != 3 {
		t.Fatalf("payload length = %d", got)
	}
	if got := binary.BigEndian.Uint32(b[24:28]); got != 0 {
		t.Fatalf("flags = %d", got)
	}
}

func TestPacketRejectsMalformedInput(t *testing.T) {
	valid := func() []byte {
		var buf bytes.Buffer
		if err := WritePacket(&buf, Packet{Type: MessagePing}); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	tests := []struct {
		name string
		edit func([]byte) []byte
	}{
		{name: "short header", edit: func(b []byte) []byte { return b[:HeaderSize-1] }},
		{name: "bad magic", edit: func(b []byte) []byte { b[0] = 'X'; return b }},
		{name: "bad version", edit: func(b []byte) []byte { binary.BigEndian.PutUint16(b[8:10], ProtocolVersion+1); return b }},
		{name: "unknown type", edit: func(b []byte) []byte { binary.BigEndian.PutUint16(b[10:12], 99); return b }},
		{name: "unknown flags", edit: func(b []byte) []byte { binary.BigEndian.PutUint32(b[24:28], 1); return b }},
		{name: "truncated payload", edit: func(b []byte) []byte { binary.BigEndian.PutUint32(b[20:24], 1); return b }},
		{name: "oversized payload", edit: func(b []byte) []byte { binary.BigEndian.PutUint32(b[20:24], MaxPayload+1); return b }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ReadPacket(bytes.NewReader(tt.edit(valid()))); err == nil {
				t.Fatal("ReadPacket unexpectedly succeeded")
			}
		})
	}
}

func TestActivateRoundTrip(t *testing.T) {
	want := ActivateRequest{
		Frontend: FrontendDaemonHelper,
		BusName:  "org.freedesktop.locale1",
	}
	payload, err := EncodeActivate(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeActivate(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeActivate = %#v, want %#v", got, want)
	}
}

func TestActivateRejectsInvalidBusNameAndTrailingBytes(t *testing.T) {
	for _, name := range []string{"", "invalid", strings.Repeat("a", MaxBusName+1), "org.example.bad name"} {
		if _, err := EncodeActivate(ActivateRequest{Frontend: FrontendDaemonHelper, BusName: name}); err == nil {
			t.Fatalf("EncodeActivate(%q) unexpectedly succeeded", name)
		}
	}

	payload, err := EncodeActivate(ActivateRequest{Frontend: FrontendDaemonHelper, BusName: "org.example.Service"})
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, 0)
	if _, err := DecodeActivate(payload); err == nil {
		t.Fatal("DecodeActivate accepted trailing bytes")
	}
}

func TestActivationResultRoundTrip(t *testing.T) {
	want := ActivationResult{Code: ResultExecFailed, Detail: "execve: no such file"}
	payload, err := EncodeActivationResult(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeActivationResult(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DecodeActivationResult = %#v, want %#v", got, want)
	}
}

func TestEnvironmentRoundTrip(t *testing.T) {
	want := SetEnvironmentRequest{
		Frontend: FrontendDaemonHelper,
		Values: map[string]string{
			"LANG":                 "C.UTF-8",
			"DBUS_STARTER_ADDRESS": "unix:path=/run/dbus/system_bus_socket",
		},
	}
	payload, err := EncodeSetEnvironment(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeSetEnvironment(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got.Frontend != want.Frontend || len(got.Values) != len(want.Values) {
		t.Fatalf("DecodeSetEnvironment = %#v", got)
	}
	for key, value := range want.Values {
		if got.Values[key] != value {
			t.Fatalf("environment[%q] = %q, want %q", key, got.Values[key], value)
		}
	}
}
