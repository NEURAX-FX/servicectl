package main

import (
	"errors"
	"reflect"
	"testing"
)

type recordingSink struct {
	messages []string
	err      error
	closed   bool
	calls    int
}

func (sink *recordingSink) WriteMessage(message string) error {
	sink.calls++
	if sink.err != nil {
		return sink.err
	}
	sink.messages = append(sink.messages, message)
	return nil
}

func (sink *recordingSink) Close() error {
	sink.closed = true
	return nil
}

func TestWriteMessageUsesConfiguredSink(t *testing.T) {
	sink := &recordingSink{}
	if err := writeMessage(sink, "token"); err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(sink, "   "); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sink.messages, []string{"token"}) {
		t.Fatalf("messages = %#v", sink.messages)
	}
}

func TestReplaySpillUsesConfiguredSink(t *testing.T) {
	spill := newSpillManager("demo", t.TempDir(), 1024, 2)
	for _, message := range []string{"one", "two"} {
		if err := spill.WriteLine(message); err != nil {
			t.Fatal(err)
		}
	}
	sink := &recordingSink{}
	if err := replaySpill(sink, spill, &statusReporter{service: "demo"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sink.messages, []string{"one", "two"}) {
		t.Fatalf("messages = %#v", sink.messages)
	}
	if spill.HasSpill() {
		t.Fatal("spill files remain after successful replay")
	}
}

func TestWriteMessageReturnsSinkError(t *testing.T) {
	want := errors.New("unavailable")
	if err := writeMessage(&recordingSink{err: want}, "token"); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}
