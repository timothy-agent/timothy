//go:build integration

package missions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

type inboxRow struct {
	kind    string
	payload map[string]any
}

func inboxRows(t *testing.T, s *Store, missionID string) []inboxRow {
	t.Helper()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rows, err := db.Query(t.Context(), `SELECT kind, payload FROM events WHERE source = 'mission' AND dedup_key = $1 ORDER BY id`, missionID)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()
	var out []inboxRow
	for rows.Next() {
		var r inboxRow
		var raw []byte
		if err := rows.Scan(&r.kind, &raw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		_ = json.Unmarshal(raw, &r.payload)
		out = append(out, r)
	}
	return out
}

// TestApplyTransitionTerminalWritesOneEvent covers D-117's producer: the
// terminal transition leaves exactly one events row, readable the moment
// ApplyTransition returns, and non-terminal transitions leave none.
func TestApplyTransitionTerminalWritesOneEvent(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	id, err := s.Create(ctx, Mission{Goal: marker + "events-done", Kind: "general", OriginKind: OriginAutomation, Unattended: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{Next: StepState{Phase: PhaseBuild, Status: StatusWorking, MaxIterations: 8}}); err != nil {
		t.Fatalf("ApplyTransition (non-terminal): %v", err)
	}
	if got := inboxRows(t, s, id); len(got) != 0 {
		t.Fatalf("events after non-terminal transition = %+v, want none", got)
	}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseDone, Status: StatusDone, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.done", Payload: map[string]any{}}},
	}); err != nil {
		t.Fatalf("ApplyTransition (done): %v", err)
	}
	got := inboxRows(t, s, id)
	if len(got) != 1 || got[0].kind != events.KindMissionDone {
		t.Fatalf("events after done = %+v, want one mission.done", got)
	}
	p := got[0].payload
	if p["mission_id"] != id || p["phase"] != "done" || p["origin_kind"] != OriginAutomation || p["unattended"] != true {
		t.Fatalf("payload = %+v", p)
	}
	if _, ok := p["reason"]; ok {
		t.Fatalf("payload = %+v, want empty reason omitted", p)
	}
	if _, ok := p["workflow_run_id"]; ok {
		t.Fatalf("payload = %+v, want empty workflow_run_id omitted", p)
	}
}

func TestApplyTransitionFailedEventCarriesReason(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	id, err := s.Create(ctx, Mission{Goal: marker + "events-failed", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseFailed, Status: StatusError, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.failed", Payload: map[string]any{"reason": "cancelled"}}},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}
	got := inboxRows(t, s, id)
	if len(got) != 1 || got[0].kind != events.KindMissionFailed || got[0].payload["reason"] != "cancelled" || got[0].payload["origin_kind"] != OriginAPI {
		t.Fatalf("events = %+v, want one mission.failed with reason and origin", got)
	}
}

// TestApplyTransitionCommitFailureLeavesNoEvent makes the transition's
// commit fail through a deferred constraint trigger on its own events
// row: the mission stays where it was and no events row survives.
func TestApplyTransitionCommitFailureLeavesNoEvent(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	id, err := s.Create(ctx, Mission{Goal: marker + "events-rollback", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db, _ := s.db.Get()
	if _, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION itest_events_reject() RETURNS trigger LANGUAGE plpgsql AS
		$$BEGIN RAISE EXCEPTION 'itest: reject commit'; END$$`); err != nil {
		t.Fatalf("create function: %v", err)
	}
	if _, err := db.Exec(ctx, fmt.Sprintf(`CREATE CONSTRAINT TRIGGER itest_events_reject AFTER INSERT ON events
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.dedup_key = '%s') EXECUTE FUNCTION itest_events_reject()`, id)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DROP TRIGGER IF EXISTS itest_events_reject ON events`)
		_, _ = db.Exec(context.Background(), `DROP FUNCTION IF EXISTS itest_events_reject()`)
	})

	err = s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseDone, Status: StatusDone, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.done", Payload: map[string]any{}}},
	})
	if err == nil {
		t.Fatal("ApplyTransition = nil error, want the commit failure")
	}
	if got := inboxRows(t, s, id); len(got) != 0 {
		t.Fatalf("events after failed commit = %+v, want none", got)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Phase.Terminal() {
		t.Fatalf("mission phase = %s after failed commit, want non-terminal", m.Phase)
	}
	evs, _ := s.Events(ctx, id)
	for _, ev := range evs {
		if ev.Kind == "mission.done" {
			t.Fatal("mission.done mission_event survived a failed commit")
		}
	}
}

// notificationRows returns a mission's notification rows for assertions.
func notificationRows(t *testing.T, s *Store, missionID string) []Notification {
	t.Helper()
	notes, err := (&Notifier{db: s.db}).List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var out []Notification
	for _, n := range notes {
		if n.MissionID == missionID {
			out = append(out, n)
		}
	}
	return out
}

// TestNotifyConsumerDeliversAfterCrashBeforeNotify simulates a crash
// between ApplyTransition's commit and the (removed) synchronous
// notify call: the terminal event sits in the inbox unconsumed, and a
// fresh drainer with only the notify consumer delivers it (D-117,
// issue #843).
func TestNotifyConsumerDeliversAfterCrashBeforeNotify(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	id, err := s.Create(ctx, Mission{Goal: marker + "events-notify", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseDone, Status: StatusDone, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.done", Payload: map[string]any{}}},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}
	if len(notificationRows(t, s, id)) != 0 {
		t.Fatal("no notify consumer has run yet, want no notification rows")
	}
	db, _ := s.db.Get()
	// Only this test's event is pending while it drains.
	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = now() WHERE processed_at IS NULL AND NOT (source = 'mission' AND dedup_key = $1)`, id); err != nil {
		t.Fatalf("settle other events: %v", err)
	}

	notify := &Notifier{db: s.db, log: log}
	drainer := events.NewDrainer(events.NewStore(s.db), []events.Consumer{NewNotifyConsumer(s, notify.NotifyMessage, log)}, nil, log)
	if _, err := drainer.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}

	notes := notificationRows(t, s, id)
	if len(notes) != 1 || notes[0].Kind != "done" {
		t.Fatalf("notifications = %+v, want one kind=done", notes)
	}
	evs, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Kind == notifiedKind {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("mission.notified events = %d, want 1", n)
	}
	var processed bool
	var lastError *string
	if err := db.QueryRow(ctx, `SELECT processed_at IS NOT NULL, last_error FROM events WHERE source = 'mission' AND dedup_key = $1`, id).Scan(&processed, &lastError); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if !processed || lastError != nil {
		t.Fatalf("event processed=%v last_error=%v, want processed cleanly", processed, lastError)
	}
}

// TestNotifyConsumerRedeliveryNotifiesOnceViaDrainer drains a terminal
// mission's event through the real drainer, marks the notification
// read, then makes the row unprocessed again (a crash before the drain
// committed): the second drain must not send a duplicate notification
// row.
func TestNotifyConsumerRedeliveryNotifiesOnceViaDrainer(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	id, err := s.Create(ctx, Mission{Goal: marker + "events-notify-redeliver", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseDone, Status: StatusDone, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.done", Payload: map[string]any{}}},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}
	db, _ := s.db.Get()
	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = now() WHERE processed_at IS NULL AND NOT (source = 'mission' AND dedup_key = $1)`, id); err != nil {
		t.Fatalf("settle other events: %v", err)
	}

	notify := &Notifier{db: s.db, log: log}
	consumer := NewNotifyConsumer(s, notify.NotifyMessage, log)
	drainer := events.NewDrainer(events.NewStore(s.db), []events.Consumer{consumer}, nil, log)

	if _, err := drainer.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	notes := notificationRows(t, s, id)
	if len(notes) != 1 {
		t.Fatalf("notifications after first drain = %+v, want 1", notes)
	}
	if err := notify.MarkRead(ctx, notes[0].ID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = NULL WHERE source = 'mission' AND dedup_key = $1`, id); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := drainer.Drain(ctx); err != nil {
		t.Fatalf("second drain: %v", err)
	}

	notes = notificationRows(t, s, id)
	if len(notes) != 1 {
		t.Fatalf("notifications after redelivery = %+v, want still 1 (idempotency guard must suppress the second send)", notes)
	}
}

// TestTerminalMissionNotifiesExactlyOnce covers the full path: a
// Signal(InputCancel) transition to failed writes zero notification
// rows synchronously (OnTransition no longer handles terminal states),
// and two drains of the mission.failed event send exactly one
// notification, with cancelled wording.
func TestTerminalMissionNotifiesExactlyOnce(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	id, err := s.Create(ctx, Mission{Goal: marker + "events-cancel", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{Next: StepState{Phase: PhaseBuild, Status: StatusPaused, MaxIterations: 8}}); err != nil {
		t.Fatalf("ApplyTransition (pause): %v", err)
	}

	notify := &Notifier{db: s.db, log: log}
	d := NewDriver(s, nil, nil, notify, nil, nil, nil, nil, log)
	if err := d.Signal(ctx, id, InputCancel); err != nil {
		t.Fatalf("Signal cancel: %v", err)
	}
	if len(notificationRows(t, s, id)) != 0 {
		t.Fatal("Signal(InputCancel) wrote a notification synchronously, want none (NotifyConsumer handles terminal states)")
	}

	db, _ := s.db.Get()
	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = now() WHERE processed_at IS NULL AND NOT (source = 'mission' AND dedup_key = $1)`, id); err != nil {
		t.Fatalf("settle other events: %v", err)
	}
	drainer := events.NewDrainer(events.NewStore(s.db), []events.Consumer{NewNotifyConsumer(s, notify.NotifyMessage, log)}, nil, log)
	if _, err := drainer.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, err := drainer.Drain(ctx); err != nil {
		t.Fatalf("second drain: %v", err)
	}

	notes := notificationRows(t, s, id)
	if len(notes) != 1 || notes[0].Kind != "error" || !strings.Contains(notes[0].Message, "cancelled") {
		t.Fatalf("notifications = %+v, want exactly one kind=error with cancelled wording", notes)
	}
}

// TestMemoryConsumerRedeliveryExtractsOnce drains a terminal mission's
// event through the real drainer, then makes the row unprocessed again
// (a crash before the drain committed): the second drain must not
// extract twice.
func TestMemoryConsumerRedeliveryExtractsOnce(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	id, err := s.Create(ctx, Mission{Goal: marker + "events-memory", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db, _ := s.db.Get()
	var sessionID string
	if err := db.QueryRow(ctx, `INSERT INTO sessions (title) VALUES ($1) RETURNING id`, marker+"events-memory").Scan(&sessionID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.SetSession(ctx, id, sessionID); err != nil {
		t.Fatalf("SetSession: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseDone, Status: StatusDone, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.done", Payload: map[string]any{}}},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}
	// Only this test's event is pending while it drains.
	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = now() WHERE processed_at IS NULL AND NOT (source = 'mission' AND dedup_key = $1)`, id); err != nil {
		t.Fatalf("settle other events: %v", err)
	}

	d := NewDriver(s, nil, nil, nil, nil, nil, nil, nil, log)
	var calls atomic.Int32
	d.SetMemoryExtract(func(ctx context.Context, sessionID string, seq int64, text, route string) { calls.Add(1) })
	drainer := events.NewDrainer(events.NewStore(s.db), []events.Consumer{NewMemoryConsumer(d)}, nil, log)

	if _, err := drainer.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = NULL WHERE source = 'mission' AND dedup_key = $1`, id); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := drainer.Drain(ctx); err != nil {
		t.Fatalf("second drain: %v", err)
	}

	evs, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Kind == memoryExtractedKind {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("mission.memory_extracted events = %d, want 1", n)
	}
	var processed bool
	var lastError *string
	if err := db.QueryRow(ctx, `SELECT processed_at IS NOT NULL, last_error FROM events WHERE source = 'mission' AND dedup_key = $1`, id).Scan(&processed, &lastError); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if !processed || lastError != nil {
		t.Fatalf("event processed=%v last_error=%v, want processed cleanly", processed, lastError)
	}
}
