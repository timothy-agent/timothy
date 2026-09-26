package missions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

var errBoom = errors.New("boom")

// discardLog is a logger consumeNotify's deleted-mission path can call
// safely.
var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// recordingNotify is a notify func that records every call it
// receives, like recordingExtract does for memory extraction.
type recordingNotify struct {
	mu    sync.Mutex
	calls []struct{ missionID, kind, message string }
	err   error // returned by fn, simulating a send failure
}

func (r *recordingNotify) fn() func(ctx context.Context, missionID, kind, message string) error {
	return func(ctx context.Context, missionID, kind, message string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, struct{ missionID, kind, message string }{missionID, kind, message})
		return r.err
	}
}

func (r *recordingNotify) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// consumeNotify delivers the mission's terminal event to the notify
// consumer, the way the events drainer does after commit (D-117).
func consumeNotify(t *testing.T, store notifyStore, notify func(ctx context.Context, missionID, kind, message string) error, id string, phase, reason string) error {
	t.Helper()
	ev, err := events.MissionTerminal(events.MissionPayload{MissionID: id, Phase: phase, Reason: reason})
	if err != nil {
		t.Fatalf("MissionTerminal: %v", err)
	}
	return NewNotifyConsumer(store, notify, discardLog).Handle(context.Background(), nil, ev)
}

func TestNotifyConsumerKinds(t *testing.T) {
	c := NewNotifyConsumer(nil, nil, nil)
	if c.Name() == "" {
		t.Fatal("consumer name is empty")
	}
	got := strings.Join(c.Kinds(), ",")
	if got != events.KindMissionDone+","+events.KindMissionFailed {
		t.Fatalf("Kinds() = %s, want mission.done and mission.failed", got)
	}
}

func TestNotifyConsumerRejectsBadPayload(t *testing.T) {
	rec := &recordingNotify{}
	c := NewNotifyConsumer(newFakeStore(), rec.fn(), nil)
	if err := c.Handle(context.Background(), nil, events.Event{Payload: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("Handle with no mission_id = nil error, want error")
	}
}

func TestNotifyConsumerSendsDone(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Goal: "add a widget", Phase: PhaseDone, Status: StatusDone})
	rec := &recordingNotify{}

	if err := consumeNotify(t, store, rec.fn(), "m1", "done", ""); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("notify calls = %d, want 1", got)
	}
	rec.mu.Lock()
	call := rec.calls[0]
	rec.mu.Unlock()
	if call.missionID != "m1" || call.kind != "done" {
		t.Fatalf("call = %+v, want mission_id=m1 kind=done", call)
	}
	want := composeMessage("done", PRTitle(Mission{ID: "m1", Goal: "add a widget"}), "")
	if call.message != want {
		t.Fatalf("message = %q, want %q", call.message, want)
	}
	evs, _ := store.Events(context.Background(), "m1")
	n := 0
	for _, ev := range evs {
		if ev.Kind == notifiedKind {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("mission.notified events = %d, want 1", n)
	}
}

func TestNotifyConsumerSendsFailedAndCancelled(t *testing.T) {
	tests := []struct {
		name       string
		reason     string
		wantKind   string
		wantSubstr string
	}{
		{"failed with no reason", "", "error", "is failed"},
		{"cancelled", "cancelled", "error", "is cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			store.put("m1", Mission{ID: "m1", Goal: "risky task", Phase: PhaseFailed, Status: StatusError})
			rec := &recordingNotify{}

			if err := consumeNotify(t, store, rec.fn(), "m1", "failed", tt.reason); err != nil {
				t.Fatalf("consume: %v", err)
			}
			if got := rec.count(); got != 1 {
				t.Fatalf("notify calls = %d, want 1", got)
			}
			rec.mu.Lock()
			call := rec.calls[0]
			rec.mu.Unlock()
			if call.kind != tt.wantKind {
				t.Fatalf("kind = %q, want %q", call.kind, tt.wantKind)
			}
			if !strings.Contains(call.message, tt.wantSubstr) {
				t.Fatalf("message = %q, want to contain %q", call.message, tt.wantSubstr)
			}
		})
	}
}

func TestNotifyConsumerRedeliveryNotifiesOnce(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Goal: "add a widget", Phase: PhaseDone, Status: StatusDone})
	rec := &recordingNotify{}

	if err := consumeNotify(t, store, rec.fn(), "m1", "done", ""); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if err := consumeNotify(t, store, rec.fn(), "m1", "done", ""); err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("notify calls after redelivery = %d, want 1", got)
	}

	// A pre-seeded marker (a crash right after the first Handle's
	// AppendEvent, before the drain batch committed) suppresses the
	// send entirely on the next delivery.
	store2 := newFakeStore()
	store2.put("m2", Mission{ID: "m2", Goal: "add a widget", Phase: PhaseDone, Status: StatusDone})
	if err := store2.AppendEvent(context.Background(), "m2", notifiedKind, map[string]any{"kind": "done"}); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	rec2 := &recordingNotify{}
	if err := consumeNotify(t, store2, rec2.fn(), "m2", "done", ""); err != nil {
		t.Fatalf("consume with pre-seeded marker: %v", err)
	}
	if got := rec2.count(); got != 0 {
		t.Fatalf("notify calls with a pre-seeded marker = %d, want 0", got)
	}
}

func TestNotifyConsumerSendErrorWritesNoMarker(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Goal: "add a widget", Phase: PhaseDone, Status: StatusDone})
	failing := &recordingNotify{err: errBoom}

	if err := consumeNotify(t, store, failing.fn(), "m1", "done", ""); err == nil {
		t.Fatal("consume with a failing notify = nil error, want error")
	}
	evs, _ := store.Events(context.Background(), "m1")
	if alreadyNotified(evs) {
		t.Fatal("a failed send must not write mission.notified")
	}

	// Retry with a working notify sends exactly once.
	ok := &recordingNotify{}
	if err := consumeNotify(t, store, ok.fn(), "m1", "done", ""); err != nil {
		t.Fatalf("retry consume: %v", err)
	}
	if got := ok.count(); got != 1 {
		t.Fatalf("notify calls on retry = %d, want 1", got)
	}
}

func TestNotifyConsumerSkipsDeletedMission(t *testing.T) {
	rec := &recordingNotify{}
	if err := consumeNotify(t, newFakeStore(), rec.fn(), "missing", "done", ""); err != nil {
		t.Fatalf("consume for a deleted mission = %v, want nil so the event is processed, not retried", err)
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("notify calls = %d, want 0", got)
	}
}
