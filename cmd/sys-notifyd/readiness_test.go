package main

import (
	"os"
	"testing"
	"time"
)

func TestEmitReadyWritesSelectedDescriptor(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	srv := &server{cfg: config{readyFD: int(w.Fd())}}
	r.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	srv.emitReady()
	buf := make([]byte, 32)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "\n" {
		t.Fatalf("native readiness=%q", buf[:n])
	}
}
