package channels

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
	for _, k := range []string{KindTelegram, KindSlack, KindEmail} {
		if !slices.Contains(got, k) {
			t.Errorf("Kinds() missing %q", k)
		}
	}
}
