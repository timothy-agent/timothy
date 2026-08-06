//go:build integration

package missions

import (
	"context"
	"testing"
)

func testNotifier(t *testing.T, store *Store) *Notifier {
	t.Helper()
	return NewNotifier(store.db, "", store.log)
}

func TestOnTransitionWritesNotificationOnActionableTransition(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	n := testNotifier(t, store)

	id, err := store.Create(ctx, Mission{Goal: marker + "notify-1", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := n.OnTransition(ctx, id, StatusWorking, StatusPaused); err != nil {
		t.Fatalf("OnTransition: %v", err)
	}
	notes, err := n.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, note := range notes {
		if note.MissionID == id {
			found = true
			if note.Kind != "paused" || note.Read {
				t.Fatalf("notification = %+v, want kind=paused unread", note)
			}
		}
	}
	if !found {
		t.Fatal("OnTransition on an actionable transition did not write a notification")
	}
}

func TestOnTransitionSilentOnNonActionable(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	n := testNotifier(t, store)

	id, err := store.Create(ctx, Mission{Goal: marker + "notify-2", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := n.OnTransition(ctx, id, StatusIdle, StatusWorking); err != nil {
		t.Fatalf("OnTransition: %v", err)
	}
	notes, err := n.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, note := range notes {
		if note.MissionID == id {
			t.Fatalf("non-actionable transition wrote a notification: %+v", note)
		}
	}
}

func TestSendOncePerMissionDedupes(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	n := testNotifier(t, store)

	id, err := store.Create(ctx, Mission{Goal: marker + "notify-3", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A worker re-asking permission every command still produces
	// exactly ONE inbox row, not one per re-ask.
	for i := 0; i < 3; i++ {
		if err := n.OnTransition(ctx, id, StatusWorking, StatusWaitingForInput); err != nil {
			t.Fatalf("OnTransition[%d]: %v", i, err)
		}
	}
	notes, err := n.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	count := 0
	for _, note := range notes {
		if note.MissionID == id {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("notification count for repeated identical transitions = %d, want 1", count)
	}
}

func TestClearMissionMarksUnreadRowsRead(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	n := testNotifier(t, store)

	id, err := store.Create(ctx, Mission{Goal: marker + "notify-4", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := n.OnTransition(ctx, id, StatusWorking, StatusPaused); err != nil {
		t.Fatalf("OnTransition: %v", err)
	}
	if err := n.ClearMission(ctx, id); err != nil {
		t.Fatalf("ClearMission: %v", err)
	}
	notes, err := n.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, note := range notes {
		if note.MissionID == id && !note.Read {
			t.Fatalf("ClearMission did not mark the notification read: %+v", note)
		}
	}

	// A NEW notification for the same mission after clearing is not
	// suppressed by the dedup logic (the prior one is read, so the
	// NOT EXISTS ... AND NOT read guard doesn't see it).
	if err := n.OnTransition(ctx, id, StatusWorking, StatusDone); err != nil {
		t.Fatalf("OnTransition after clear: %v", err)
	}
	notes, err = n.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	unreadDone := false
	for _, note := range notes {
		if note.MissionID == id && note.Kind == "done" && !note.Read {
			unreadDone = true
		}
	}
	if !unreadDone {
		t.Fatal("a fresh actionable transition after ClearMission did not write a new unread notification")
	}
}

func TestMarkRead(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	n := testNotifier(t, store)

	id, err := store.Create(ctx, Mission{Goal: marker + "notify-5", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := n.OnTransition(ctx, id, StatusWorking, StatusError); err != nil {
		t.Fatalf("OnTransition: %v", err)
	}
	notes, err := n.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var noteID string
	for _, note := range notes {
		if note.MissionID == id {
			noteID = note.ID
		}
	}
	if noteID == "" {
		t.Fatal("could not find the notification just created")
	}
	if err := n.MarkRead(ctx, noteID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	notes, err = n.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, note := range notes {
		if note.ID == noteID && !note.Read {
			t.Fatal("MarkRead did not mark the notification read")
		}
	}
}
