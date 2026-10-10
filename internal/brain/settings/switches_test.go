package settings

import (
	"slices"
	"strings"
	"testing"
)

func TestSwitchesMatchKnownKeys(t *testing.T) {
	t.Parallel()
	got := Switches()
	if len(got) != len(knownKeys) {
		t.Fatalf("Switches() has %d entries, want %d", len(got), len(knownKeys))
	}
	if !slices.IsSortedFunc(got, func(a, b Switch) int { return strings.Compare(a.Key, b.Key) }) {
		t.Fatalf("Switches() not sorted: %v", got)
	}
	for _, s := range got {
		if !knownKeys[s.Key] {
			t.Errorf("unknown switch %q", s.Key)
		}
		if s.Default == knownKeysOff[s.Key] {
			t.Errorf("%s default = %v, want %v", s.Key, s.Default, !knownKeysOff[s.Key])
		}
	}
}
