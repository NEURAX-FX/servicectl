package cgrouptrack

import "testing"

func TestUnitKeyDirectoryRoundTrip(t *testing.T) {
	key := UnitKey{Mode: ModeUser, UID: 1000, Unit: "dbus-org.freedesktop.locale1.service"}
	directory, err := key.DirectoryName()
	if err != nil {
		t.Fatal(err)
	}
	if directory != "dbus-org.freedesktop.locale1" {
		t.Fatalf("directory = %q", directory)
	}
	decoded, err := DecodeUnitDirectory(ModeUser, 1000, directory)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != key {
		t.Fatalf("decoded = %#v, want %#v", decoded, key)
	}
}

func TestDecodeUnitDirectoryAcceptsLegacyEncoding(t *testing.T) {
	key := UnitKey{Mode: ModeSystem, Unit: "demo.service"}
	legacy, err := legacyUnitDirectory(key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeLegacyUnitDirectory(ModeSystem, 0, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != key {
		t.Fatalf("decoded=%#v", decoded)
	}
}

func TestUnitKeyRejectsTraversalAndNonCanonicalNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "..service", "...service", "../x.service", "a/b.service", `a\b.service`, "x", "x.service.service", "x\x00.service", "x\n.service"} {
		if err := (UnitKey{Mode: ModeSystem, Unit: name}).Validate(); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}

func TestUnitKeyModeAndUIDRules(t *testing.T) {
	if err := (UnitKey{Mode: ModeSystem, UID: 1, Unit: "x.service"}).Validate(); err == nil {
		t.Fatal("system key with nonzero UID accepted")
	}
	if err := (UnitKey{Mode: ModeUser, UID: 0, Unit: "x.service"}).Validate(); err != nil {
		t.Fatalf("root user key rejected: %v", err)
	}
	if err := (UnitKey{Mode: "invalid", Unit: "x.service"}).Validate(); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestDecodeUnitDirectoryRejectsInvalidName(t *testing.T) {
	if _, err := DecodeUnitDirectory(ModeSystem, 0, "bad.name.service"); err == nil {
		t.Fatal("directory with service suffix accepted")
	}
}
