//go:build integration

package session

import (
	"fmt"
	"testing"
)

func seqsOf(events []Event) []int64 {
	out := make([]int64, len(events))
	for i, e := range events {
		out[i] = e.Seq
	}
	return out
}

// TestEventsPageBoundaries covers issue #1113's seq keyset reads over
// a 10-event log (seq 1 is session_started).
func TestEventsPageBoundaries(t *testing.T) {
	s, id := integrationStore(t)
	ctx := t.Context()
	for i := 2; i <= 10; i++ {
		if _, err := s.Append(ctx, id, KindUserMessage, UserMessage{Text: fmt.Sprintf("m%d", i)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	cases := []struct {
		name          string
		after, before int64
		limit         int
		want          []int64
		wantMore      bool
	}{
		{name: "latest N ascending", after: -1, limit: 3, want: []int64{8, 9, 10}, wantMore: true},
		{name: "latest covers all", after: -1, limit: 10, want: []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
		{name: "latest over size", after: -1, limit: 50, want: []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
		{name: "before mid", after: -1, before: 8, limit: 3, want: []int64{5, 6, 7}, wantMore: true},
		{name: "before reaching start", after: -1, before: 4, limit: 3, want: []int64{1, 2, 3}},
		{name: "before first", after: -1, before: 1, limit: 3, want: nil},
		{name: "after zero", after: 0, limit: 2, want: []int64{1, 2}, wantMore: true},
		{name: "after mid", after: 7, limit: 2, want: []int64{8, 9}, wantMore: true},
		{name: "after exact end", after: 7, limit: 3, want: []int64{8, 9, 10}},
		{name: "after newest", after: 10, limit: 5, want: nil},
	}
	for _, c := range cases {
		got, more, err := s.EventsPage(ctx, id, c.after, c.before, c.limit)
		if err != nil {
			t.Fatalf("%s: EventsPage: %v", c.name, err)
		}
		if fmt.Sprint(seqsOf(got)) != fmt.Sprint(c.want) || more != c.wantMore {
			t.Fatalf("%s: seqs = %v more=%v, want %v more=%v", c.name, seqsOf(got), more, c.want, c.wantMore)
		}
	}
}

// TestTranscriptControlMatchesFullProjection checks a paged projection
// built from TranscriptControl equals the full one for the same seqs.
func TestTranscriptControlMatchesFullProjection(t *testing.T) {
	s, id := integrationStore(t)
	ctx := t.Context()
	appends := []struct {
		kind    string
		payload any
	}{
		{KindUserMessage, UserMessage{Text: "go"}},
		{KindPermissionRequest, PermissionRequest{ID: "p1", Tool: "shell"}},
		{KindPendingState, PendingState{Partial: "old"}},
		{KindPermissionResolved, PermissionResolved{ID: "p1", Decision: "once"}},
		{KindToolExecution, ToolExecution{CallID: "c1", Name: "shell", Status: "ok"}},
		{KindAssistantTurn, AssistantTurn{}},
		{KindUserMessage, UserMessage{Text: "again"}},
		{KindPendingState, PendingState{Partial: "live"}},
	}
	for _, a := range appends {
		if _, err := s.Append(ctx, id, a.kind, a.payload); err != nil {
			t.Fatalf("Append %s: %v", a.kind, err)
		}
	}
	all, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	full, err := UITranscript(all)
	if err != nil {
		t.Fatalf("UITranscript: %v", err)
	}
	control, err := s.TranscriptControl(ctx, id)
	if err != nil {
		t.Fatalf("TranscriptControl: %v", err)
	}
	for _, e := range control {
		if (e.Kind == KindPendingState || e.Kind == KindAssistantTurn) && e.Payload != nil {
			t.Fatalf("control carried a payload it never decodes: %+v", e)
		}
	}
	window, _, err := s.EventsPage(ctx, id, -1, 7, 4) // seqs 3..6
	if err != nil {
		t.Fatalf("EventsPage: %v", err)
	}
	page, err := UITranscriptPage(window, control)
	if err != nil {
		t.Fatalf("UITranscriptPage: %v", err)
	}
	var want []string
	for _, it := range full {
		if it.Seq >= 3 && it.Seq <= 6 {
			want = append(want, fmt.Sprint(it.Seq, it.Kind))
		}
	}
	var got []string
	for _, it := range page.Items {
		got = append(got, fmt.Sprint(it.Seq, it.Kind))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) || page.LivePendingSeq != 9 {
		t.Fatalf("page items = %v live=%d, want %v live=9", got, page.LivePendingSeq, want)
	}
}
