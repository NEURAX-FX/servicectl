package procinfo

import (
	"strings"
	"testing"
)

func TestParseStatHandlesClosingParen(t *testing.T) {
	line := "42 (worker ) name) S 7 " + strings.Repeat("0 ", 17) + "12345 0"
	stat, err := ParseStat(line)
	if err != nil {
		t.Fatal(err)
	}
	if stat.PID != 42 || stat.Comm != "worker ) name" || stat.State != 'S' || stat.PPID != 7 || stat.StartTime != 12345 {
		t.Fatalf("stat = %#v", stat)
	}
}

func TestParseStatRejectsMalformedInput(t *testing.T) {
	for _, line := range []string{"", "42 no-parens", "x (name) S 1", "42 (name) ? x", strings.Repeat("x", MaxStatBytes+1)} {
		if _, err := ParseStat(line); err == nil {
			t.Fatalf("accepted %q", line)
		}
	}
}
