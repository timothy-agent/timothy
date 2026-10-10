package destinations

import (
	"slices"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
)

func TestKindsSortedAndComplete(t *testing.T) {
	t.Parallel()
	got := Kinds()
	if !slices.IsSorted(got) {
		t.Fatalf("Kinds() not sorted: %v", got)
	}
	want := []string{"channel", "email", "webhook"}
	for _, k := range gitprovider.Kinds() {
		want = append(want, string(k))
	}
	for _, k := range want {
		if !slices.Contains(got, k) {
			t.Errorf("Kinds() missing %q", k)
		}
	}
	if len(got) != len(want) {
		t.Errorf("Kinds() = %v, want %v", got, want)
	}
}
