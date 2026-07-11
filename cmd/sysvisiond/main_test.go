package main

import (
	"testing"

	"servicectl/internal/visionapi"
)

func TestParseConfigRequiresOnePlane(t *testing.T) {
	cfg, err := parseConfig([]string{"--mode=user"}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.mode != visionapi.ModeUser || cfg.uid != 1000 {
		t.Fatalf("config = %#v", cfg)
	}
	if _, err := parseConfig([]string{"--mode=invalid"}, 0); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if _, err := parseConfig([]string{"--mode=system"}, 1000); err == nil {
		t.Fatal("unprivileged system mode accepted")
	}
}

func TestMetaIncludesStableEpoch(t *testing.T) {
	d := newDaemon(config{mode: visionapi.ModeSystem, uid: 0}, "epoch-test", nil)
	first := d.meta()
	second := d.meta()
	if first.VisionEpoch != "epoch-test" || first != second {
		t.Fatalf("unstable meta: %#v %#v", first, second)
	}
	if first.Mode != visionapi.ModeSystem || first.UID != 0 {
		t.Fatalf("wrong plane metadata: %#v", first)
	}
}
