//go:build integration

package missions

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func testNotifier(t *testing.T, store *Store) *Notifier {
	t.Helper()
	return NewNotifier(store.db, "", nil, store.log)
}

func TestOnTransitionWritesNotificationOnActionableTransition(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	n := testNotifier(t, store)

	goal := marker + "notify-1"
	id, err := store.Create(ctx, Mission{Goal: goal, Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := n.OnTransition(ctx, Mission{ID: id, Goal: goal}, StatusWorking, StatusPaused, ""); err != nil {
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

// TestOnTransitionSilentOnTerminal confirms OnTransition writes no
// notification for a transition into done/error: NotifyConsumer sends
// those off the events inbox instead (D-117, issue #843).
func TestOnTransitionSilentOnTerminal(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	n := testNotifier(t, store)

	goal := marker + "notify-terminal"
	id, err := store.Create(ctx, Mission{Goal: goal, Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := n.OnTransition(ctx, Mission{ID: id, Goal: goal}, StatusWorking, StatusDone, ""); err != nil {
		t.Fatalf("OnTransition (done): %v", err)
	}
	if err := n.OnTransition(ctx, Mission{ID: id, Goal: goal}, StatusWorking, StatusError, ""); err != nil {
		t.Fatalf("OnTransition (error): %v", err)
	}
	notes, err := n.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, note := range notes {
		if note.MissionID == id {
			t.Fatalf("OnTransition into a terminal state wrote a notification: %+v", note)
		}
	}
}

func TestOnTransitionSilentOnNonActionable(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	n := testNotifier(t, store)

	goal := marker + "notify-2"
	id, err := store.Create(ctx, Mission{Goal: goal, Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := n.OnTransition(ctx, Mission{ID: id, Goal: goal}, StatusIdle, StatusWorking, ""); err != nil {
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

	goal := marker + "notify-3"
	id, err := store.Create(ctx, Mission{Goal: goal, Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A worker re-asking permission every command still produces
	// exactly ONE inbox row, not one per re-ask.
	for i := 0; i < 3; i++ {
		if err := n.OnTransition(ctx, Mission{ID: id, Goal: goal}, StatusWorking, StatusWaitingForInput, ""); err != nil {
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

	goal := marker + "notify-4"
	id, err := store.Create(ctx, Mission{Goal: goal, Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := n.OnTransition(ctx, Mission{ID: id, Goal: goal}, StatusWorking, StatusPaused, ""); err != nil {
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
	// NOT EXISTS ... AND NOT read guard doesn't see it). done is a
	// terminal state; OnTransition no longer fires on it (NotifyConsumer
	// does), so this exercises NotifyMessage directly instead.
	if err := n.NotifyMessage(ctx, id, "done", composeMessage("done", goal, "")); err != nil {
		t.Fatalf("NotifyMessage after clear: %v", err)
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

	goal := marker + "notify-5"
	id, err := store.Create(ctx, Mission{Goal: goal, Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := n.OnTransition(ctx, Mission{ID: id, Goal: goal}, StatusWorking, StatusPaused, ""); err != nil {
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

// TestNotifyOperatorOncePerWindow: operator rows carry no mission id,
// list alongside mission rows, and repeat within 10 minutes only once.
func TestNotifyOperatorOncePerWindow(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	n := testNotifier(t, store)
	msg := fmt.Sprintf("%soperator-%d", marker, time.Now().UnixNano())
	db, err := store.db.Get()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(context.Background(), `DELETE FROM notifications WHERE message LIKE $1 || '%'`, msg) })
	count := func() int {
		var c int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE mission_id IS NULL AND message = $1`, msg).Scan(&c); err != nil {
			t.Fatalf("count: %v", err)
		}
		return c
	}

	for i := range 3 {
		if err := n.NotifyOperator(ctx, "automation_trigger_disabled", msg); err != nil {
			t.Fatalf("NotifyOperator[%d]: %v", i, err)
		}
	}
	if c := count(); c != 1 {
		t.Fatalf("operator rows after 3 repeats = %d, want 1", c)
	}
	// Read rows still suppress within the window.
	if _, err := db.Exec(ctx, `UPDATE notifications SET read = true WHERE message = $1`, msg); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if err := n.NotifyOperator(ctx, "automation_trigger_disabled", msg); err != nil {
		t.Fatalf("NotifyOperator: %v", err)
	}
	if c := count(); c != 1 {
		t.Fatalf("operator rows after a read repeat = %d, want 1", c)
	}
	// Another kind with the same message is its own notification.
	if err := n.NotifyMessage(ctx, "", "automation_disabled", msg); err != nil {
		t.Fatalf("NotifyMessage without a mission: %v", err)
	}
	if c := count(); c != 2 {
		t.Fatalf("operator rows after another kind = %d, want 2", c)
	}
	// Past the window the same kind and message fire again.
	if _, err := db.Exec(ctx, `UPDATE notifications SET created_at = now() - interval '11 minutes' WHERE message = $1`, msg); err != nil {
		t.Fatalf("age rows: %v", err)
	}
	if err := n.NotifyOperator(ctx, "automation_trigger_disabled", msg); err != nil {
		t.Fatalf("NotifyOperator: %v", err)
	}
	if c := count(); c != 3 {
		t.Fatalf("operator rows after the window = %d, want 3", c)
	}
	notes, err := n.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, note := range notes {
		if note.Message == msg && !note.Read {
			found = true
			if note.MissionID != "" {
				t.Fatalf("operator notification mission_id = %q, want empty", note.MissionID)
			}
		}
	}
	if !found {
		t.Fatal("List does not return the unread operator notification")
	}
}
