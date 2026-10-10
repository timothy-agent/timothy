package connectors

import (
	"slices"
	"testing"
)

func TestKindsSortedAndComplete(t *testing.T) {
	t.Parallel()
	got := Kinds()
	if !slices.IsSorted(got) {
		t.Fatalf("Kinds() not sorted: %v", got)
	}
	if len(got) != len(kinds) {
		t.Fatalf("Kinds() = %v, want %d kinds", got, len(kinds))
	}
	for _, k := range got {
		if !kinds[k] {
			t.Errorf("Kinds() lists unknown kind %q", k)
		}
	}
}
