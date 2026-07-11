package cgrouptrack

import "testing"

func TestUnitKeyRoundTrip(t *testing.T) {
	key := UnitKey{Mode: ModeUser, UID: 1000, Unit: "dbus-org.freedesktop.locale1.service"}
	encoded, err := key.EncodedUnit()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeUnit(ModeUser, 1000, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != key {
		t.Fatalf("decoded = %#v, want %#v", decoded, key)
	}
}

func TestUnitKeyRejectsTraversalAndNonCanonicalNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../x.service", "a/b.service", `a\b.service`, "x", "x.service.service", "x\x00.service", "x\n.service"} {
		if err := (UnitKey{Mode: ModeSystem, Unit: name}).Validate(); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}

func TestUnitKeyModeAndUIDRules(t *testing.T) {
	if err := (UnitKey{Mode: ModeSystem, UID: 1, Unit: "x.service"}).Validate(); err == nil {
		t.Fatal("system key with nonzero UID accepted")
	}
	if err := (UnitKey{Mode: ModeUser, UID: 0, Unit: "x.service"}).Validate(); err == nil {
		t.Fatal("user key with zero UID accepted")
	}
	if err := (UnitKey{Mode: "invalid", Unit: "x.service"}).Validate(); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestDecodeUnitRejectsNonCanonicalEncoding(t *testing.T) {
	if _, err := DecodeUnit(ModeSystem, 0, "not_base64!"); err == nil {
		t.Fatal("malformed encoding accepted")
	}
}
