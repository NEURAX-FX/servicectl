package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFallbackSinkUsesSyslogWhenBrokerUnavailable(t *testing.T) {
	broker := &recordingSink{err: errors.New("connect: no such file")}
	syslog := &recordingSink{}
	sink := newFallbackSink(broker, syslog, time.Minute)

	if err := sink.WriteMessage("compat-token"); err != nil {
		t.Fatal(err)
	}
	if len(broker.messages) != 0 || len(syslog.messages) != 1 || syslog.messages[0] != "compat-token" {
		t.Fatalf("broker=%#v syslog=%#v", broker.messages, syslog.messages)
	}
}

func TestFallbackSinkReturnsCombinedErrorWhenBothPathsFail(t *testing.T) {
	broker := &recordingSink{err: errors.New("broker down")}
	syslog := &recordingSink{err: errors.New("syslog down")}
	sink := newFallbackSink(broker, syslog, time.Minute)

	err := sink.WriteMessage("token")
	if err == nil || !strings.Contains(err.Error(), "broker down") || !strings.Contains(err.Error(), "syslog down") {
		t.Fatalf("error = %v", err)
	}
}

func TestFallbackSinkRetriesBrokerAfterBackoff(t *testing.T) {
	broker := &recordingSink{err: errors.New("temporary")}
	syslog := &recordingSink{}
	now := time.Unix(100, 0)
	sink := newFallbackSinkWithClock(broker, syslog, time.Second, func() time.Time { return now })

	if err := sink.WriteMessage("first"); err != nil {
		t.Fatal(err)
	}
	if err := sink.WriteMessage("second"); err != nil {
		t.Fatal(err)
	}
	if broker.calls != 1 {
		t.Fatalf("broker calls during backoff = %d", broker.calls)
	}
	broker.err = nil
	now = now.Add(time.Second)
	if err := sink.WriteMessage("third"); err != nil {
		t.Fatal(err)
	}
	if broker.calls != 2 || len(broker.messages) != 1 || broker.messages[0] != "third" {
		t.Fatalf("broker calls/messages = %d/%#v", broker.calls, broker.messages)
	}
}
