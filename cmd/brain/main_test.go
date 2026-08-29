package main

import (
	"slices"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// TestIntersectReadOnlyConnectorToolsMatchesAllowlist confirms the
// resolver's matching step: only tools whose name matches an allow
// entry (direct or connector-namespaced suffix, D-036) come through,
// and an empty allowlist (agent opted into nothing) yields none.
func TestIntersectReadOnlyConnectorToolsMatchesAllowlist(t *testing.T) {
	available := []*tools.Tool{
		{Name: "gmail_gmail_search", ReadOnly: true},
		{Name: "google-calendar_list_calendar_events", ReadOnly: true},
		{Name: "gmail_gmail_send"}, // not read-only; ReadOnlyTools would never hand this in, but the matcher must not care
	}

	got := intersectReadOnlyConnectorTools([]string{"gmail_search"}, available)
	var names []string
	for _, tl := range got {
		names = append(names, tl.Name)
	}
	if !slices.Equal(names, []string{"gmail_gmail_search"}) {
		t.Fatalf("intersect = %v, want [gmail_gmail_search]", names)
	}

	if got := intersectReadOnlyConnectorTools(nil, available); got != nil {
		t.Fatalf("empty allowlist = %v, want nil", got)
	}

	got = intersectReadOnlyConnectorTools([]string{"list_calendar_events", "gmail_search"}, available)
	names = nil
	for _, tl := range got {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	want := []string{"gmail_gmail_search", "google-calendar_list_calendar_events"}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("intersect = %v, want %v", names, want)
	}
}
