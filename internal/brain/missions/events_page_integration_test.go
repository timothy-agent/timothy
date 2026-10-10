//go:build integration

package missions

import (
	"fmt"
	"testing"
)

// TestEventsPageBoundaries covers issue #1113's seq keyset reads over
// a mission's log, relative to whatever Create itself appended.
func TestEventsPageBoundaries(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	id, err := s.Create(ctx, Mission{Goal: marker + "events-page", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := range 6 {
		if err := s.AppendEvent(ctx, id, "mission.progress", map[string]any{"i": i}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	all, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	seqs := make([]int64, len(all))
	for i, e := range all {
		seqs[i] = e.Seq
	}
	n := len(seqs)
	cases := []struct {
		name          string
		after, before int64
		limit         int
		want          []int64
		wantMore      bool
	}{
		{name: "latest N ascending", after: -1, limit: 3, want: seqs[n-3:], wantMore: true},
		{name: "latest over size", after: -1, limit: n + 5, want: seqs},
		{name: "before mid", after: -1, before: seqs[n-2], limit: 2, want: seqs[n-4 : n-2], wantMore: true},
		{name: "before first", after: -1, before: seqs[0], limit: 3, want: []int64{}},
		{name: "after zero", after: 0, limit: 2, want: seqs[:2], wantMore: true},
		{name: "after exact end", after: seqs[n-4], limit: 3, want: seqs[n-3:]},
		{name: "after newest", after: seqs[n-1], limit: 3, want: []int64{}},
	}
	for _, c := range cases {
		got, more, err := s.EventsPage(ctx, id, c.after, c.before, c.limit)
		if err != nil {
			t.Fatalf("%s: EventsPage: %v", c.name, err)
		}
		gotSeqs := make([]int64, len(got))
		for i, e := range got {
			gotSeqs[i] = e.Seq
			if e.MissionID != id || e.Kind == "" {
				t.Fatalf("%s: row not fully scanned: %+v", c.name, e)
			}
		}
		if fmt.Sprint(gotSeqs) != fmt.Sprint(c.want) || more != c.wantMore {
			t.Fatalf("%s: seqs = %v more=%v, want %v more=%v", c.name, gotSeqs, more, c.want, c.wantMore)
		}
	}
}
