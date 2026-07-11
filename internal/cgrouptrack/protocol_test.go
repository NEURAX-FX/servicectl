package cgrouptrack

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestRequestFrameRoundTrip(t *testing.T) {
	want := Request{
		Operation: OpAttach,
		Mode:      ModeUser,
		UID:       1000,
		Unit:      "demo.service",
		PID:       42,
	}
	var frame bytes.Buffer
	if err := EncodeRequest(&frame, want); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRequest(&frame)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestDecodeRejectsOversizedFrame(t *testing.T) {
	frame := make([]byte, 4)
	binary.BigEndian.PutUint32(frame, MaxRequestBytes+1)
	if _, err := DecodeRequest(bytes.NewReader(frame)); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	frame := requestFrame(t, `{"operation":"status","surprise":true}`)
	if _, err := DecodeRequest(bytes.NewReader(frame)); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	frame := requestFrame(t, `{"operation":"status"}{"operation":"status"}`)
	if _, err := DecodeRequest(bytes.NewReader(frame)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestDecodeRejectsUnknownOperation(t *testing.T) {
	frame := requestFrame(t, `{"operation":"destroy"}`)
	if _, err := DecodeRequest(bytes.NewReader(frame)); err == nil {
		t.Fatal("unknown operation accepted")
	}
}

func TestRequestValidationRequiresOperationFields(t *testing.T) {
	tests := []Request{
		{Operation: OpStatus, Mode: ModeSystem, UID: 1},
		{Operation: OpGetUnit, Mode: ModeUser, UID: 1000},
		{Operation: OpListPIDs, Mode: ModeSystem, Unit: "../demo.service"},
		{Operation: OpAttach, Mode: ModeUser, UID: 1000, Unit: "demo.service"},
		{Operation: OpStatus, PID: 42},
	}
	for _, request := range tests {
		if err := request.Validate(); err == nil {
			t.Fatalf("accepted invalid request %#v", request)
		}
	}
}

func TestResponseFrameRoundTrip(t *testing.T) {
	want := Response{
		OK: true,
		Unit: &UnitStatus{
			Identity: InstanceIdentity{UnitKey: UnitKey{Mode: ModeSystem, Unit: "demo.service"}},
			State:    StateTracked,
		},
	}
	var frame bytes.Buffer
	if err := EncodeResponse(&frame, want); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResponse(&frame)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Unit == nil || got.Unit.Identity.Unit != "demo.service" || got.Unit.State != StateTracked {
		t.Fatalf("response = %#v", got)
	}
}

func TestEncodeRejectsOversizedJSON(t *testing.T) {
	response := Response{OK: false, Error: &APIError{Code: "large", Message: strings.Repeat("x", MaxResponseBytes)}}
	if err := EncodeResponse(&bytes.Buffer{}, response); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func requestFrame(t *testing.T, body string) []byte {
	t.Helper()
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	copy(frame[4:], body)
	return frame
}
