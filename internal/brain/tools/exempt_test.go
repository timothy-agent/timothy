package tools

import (
	"slices"
	"testing"
)

func TestExemptNamesMatchPermissions(t *testing.T) {
	t.Parallel()
	names := ExemptNames()
	if !slices.IsSorted(names) {
		t.Fatalf("ExemptNames() not sorted: %v", names)
	}
	p := NewPermissions(nil, "/workspace")
	if len(names) != len(p.exempt) {
		t.Fatalf("ExemptNames() has %d names, Permissions exempts %d", len(names), len(p.exempt))
	}
	for _, name := range names {
		if !p.isExempt(name) {
			t.Errorf("%s listed exempt but Permissions does not exempt it", name)
		}
	}
}
