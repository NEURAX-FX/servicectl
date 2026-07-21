package main

import (
	"strings"
	"testing"
)

func TestProtocolLogPacketRoundTrip(t *testing.T) {
	input := protocolPacket{
		Version:  protocolVersion,
		Kind:     packetLog,
		Sequence: 7,
		Priority: 6,
		Message:  "ready",
	}

	encoded, err := encodeProtocolPacket(input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeProtocolPacket(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != input {
		t.Fatalf("packet = %#v, want %#v", got, input)
	}
}

func TestProtocolPacketKindsValidateShape(t *testing.T) {
	tests := []struct {
		name    string
		packet  protocolPacket
		wantErr bool
	}{
		{name: "hello", packet: protocolPacket{Version: protocolVersion, Kind: packetHello}},
		{name: "log", packet: protocolPacket{Version: protocolVersion, Kind: packetLog, Sequence: 1, Priority: 6, Message: "line"}},
		{name: "ack", packet: protocolPacket{Version: protocolVersion, Kind: packetAck, Sequence: 1}},
		{name: "nack", packet: protocolPacket{Version: protocolVersion, Kind: packetNack, Error: "unavailable", Retryable: true}},
		{name: "log missing sequence", packet: protocolPacket{Version: protocolVersion, Kind: packetLog, Priority: 6, Message: "line"}, wantErr: true},
		{name: "log missing message", packet: protocolPacket{Version: protocolVersion, Kind: packetLog, Sequence: 1, Priority: 6}, wantErr: true},
		{name: "bad priority", packet: protocolPacket{Version: protocolVersion, Kind: packetLog, Sequence: 1, Priority: 9, Message: "line"}, wantErr: true},
		{name: "ack error", packet: protocolPacket{Version: protocolVersion, Kind: packetAck, Sequence: 1, Error: "unexpected"}, wantErr: true},
		{name: "nack missing error", packet: protocolPacket{Version: protocolVersion, Kind: packetNack}, wantErr: true},
		{name: "unknown kind", packet: protocolPacket{Version: protocolVersion, Kind: "other"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := encodeProtocolPacket(tt.packet)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeProtocolPacketRejectsUnknownAndTrailingFields(t *testing.T) {
	for _, input := range []string{
		`{"version":1,"kind":"hello","unit":"spoof"}`,
		`{"version":1,"kind":"hello"}{"version":1,"kind":"hello"}`,
	} {
		if _, err := decodeProtocolPacket([]byte(input)); err == nil {
			t.Fatalf("decode(%q) succeeded", input)
		}
	}
}

func TestProtocolPacketSizeLimit(t *testing.T) {
	packet := protocolPacket{
		Version:  protocolVersion,
		Kind:     packetLog,
		Sequence: 1,
		Priority: 6,
		Message:  strings.Repeat("x", maxProtocolPacketBytes),
	}
	if _, err := encodeProtocolPacket(packet); err == nil {
		t.Fatal("oversized packet encoded successfully")
	}
}

func TestDecodeProtocolPacketRejectsUnsupportedVersion(t *testing.T) {
	if _, err := decodeProtocolPacket([]byte(`{"version":99,"kind":"hello"}`)); err == nil {
		t.Fatal("unsupported version decoded successfully")
	}
}
