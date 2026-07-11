package main

import (
	"reflect"
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

func TestLifecycleNormalizerDistinguishesDirectNotifyAndLazy(t *testing.T) {
	n := newNormalizer("epoch-a", 1000)
	direct := visionapi.UnitSnapshot{Name: "direct.service", Mode: visionapi.ModeUser, ManagedBy: "dinit", State: "STARTED", MainPID: "10"}
	notify := visionapi.UnitSnapshot{Name: "notify.service", Mode: visionapi.ModeUser, ManagedBy: "sys-notifyd", State: "STARTED", Phase: "starting", MainPID: "11"}
	lazy := visionapi.UnitSnapshot{Name: "lazy.service", Mode: visionapi.ModeUser, ManagedBy: "sys-notifyd", State: "STARTED", Phase: "ready", ManagerPID: "12"}

	snapshots, events := n.Update([]visionapi.UnitSnapshot{direct, notify, lazy}, map[int]uint64{10: 101, 11: 102})
	assertLifecycleEvent(t, events, visionapi.KindUnitReady, "direct.service", 1)
	if eventFor(events, "notify.service") != nil || eventFor(events, "lazy.service") != nil {
		t.Fatalf("unready service emitted lifecycle event: %#v", events)
	}
	byName := snapshotsByName(snapshots)
	if byName["notify.service"].Lifecycle != visionapi.LifecycleStopped || byName["lazy.service"].Lifecycle != visionapi.LifecycleStopped {
		t.Fatalf("snapshots = %#v", snapshots)
	}
}

func TestLifecycleNormalizerEmitsMainPIDChangeAndStop(t *testing.T) {
	n := newNormalizer("epoch-a", 0)
	first := visionapi.UnitSnapshot{Name: "demo.service", Mode: visionapi.ModeSystem, ManagedBy: "dinit", State: "STARTED", MainPID: "10"}
	_, _ = n.Update([]visionapi.UnitSnapshot{first}, map[int]uint64{10: 100})

	second := first
	second.MainPID = "11"
	_, events := n.Update([]visionapi.UnitSnapshot{second}, map[int]uint64{11: 200})
	assertLifecycleEvent(t, events, visionapi.KindUnitMainPIDChanged, "demo.service", 2)

	second.State = "STOPPED"
	second.MainPID = ""
	_, events = n.Update([]visionapi.UnitSnapshot{second}, nil)
	assertLifecycleEvent(t, events, visionapi.KindUnitStopped, "demo.service", 3)

	_, events = n.Update([]visionapi.UnitSnapshot{second}, nil)
	if len(events) != 0 {
		t.Fatalf("unchanged snapshot emitted events: %#v", events)
	}
}

func TestLifecycleNormalizerStopsMissingUnit(t *testing.T) {
	n := newNormalizer("epoch-a", 0)
	unit := visionapi.UnitSnapshot{Name: "demo.service", Mode: visionapi.ModeSystem, ManagedBy: "dinit", State: "STARTED", MainPID: "10"}
	_, _ = n.Update([]visionapi.UnitSnapshot{unit}, map[int]uint64{10: 100})
	_, events := n.Update(nil, nil)
	assertLifecycleEvent(t, events, visionapi.KindUnitStopped, "demo.service", 2)
	unit.MainPID = "11"
	_, events = n.Update([]visionapi.UnitSnapshot{unit}, map[int]uint64{11: 200})
	assertLifecycleEvent(t, events, visionapi.KindUnitReady, "demo.service", 3)
}

func TestBroadcastClosesSlowSubscriber(t *testing.T) {
	d := newDaemon(config{mode: visionapi.ModeSystem}, "epoch-a", nil)
	d.subscriberBuffer = 1
	id, ch := d.subscribe(visionapi.WatchFilter{})
	d.broadcast(visionapi.EventEnvelope{Kind: "first"})
	d.broadcast(visionapi.EventEnvelope{Kind: "second"})
	first, ok := <-ch
	if !ok || first.Kind != "first" {
		t.Fatalf("first event = %#v, %v", first, ok)
	}
	if _, ok := <-ch; ok {
		t.Fatal("slow subscriber remained open")
	}
	d.unsubscribe(id)
}

func eventFor(events []visionapi.EventEnvelope, unit string) *visionapi.EventEnvelope {
	for i := range events {
		if events[i].Unit == unit {
			return &events[i]
		}
	}
	return nil
}

func assertLifecycleEvent(t *testing.T, events []visionapi.EventEnvelope, kind string, unit string, generation uint64) {
	t.Helper()
	event := eventFor(events, unit)
	if event == nil || event.Kind != kind || event.Generation != generation {
		t.Fatalf("event for %s = %#v, all=%#v", unit, event, events)
	}
}

func snapshotsByName(snapshots []visionapi.UnitSnapshot) map[string]visionapi.UnitSnapshot {
	result := make(map[string]visionapi.UnitSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		result[snapshot.Name] = snapshot
	}
	return result
}

func TestLifecycleNormalizerOutputIsStable(t *testing.T) {
	n := newNormalizer("epoch-a", 0)
	input := []visionapi.UnitSnapshot{
		{Name: "z.service", ManagedBy: "dinit", State: "STARTED", MainPID: "11"},
		{Name: "a.service", ManagedBy: "dinit", State: "STARTED", MainPID: "10"},
	}
	first, _ := n.Update(input, map[int]uint64{10: 100, 11: 101})
	second, events := n.Update(input, map[int]uint64{10: 100, 11: 101})
	if len(events) != 0 || !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%#v second=%#v events=%#v", first, second, events)
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
