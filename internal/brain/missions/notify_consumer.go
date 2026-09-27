package missions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

// notifiedKind marks that a mission notification has already fired:
// the append-only idempotency record NotifyConsumer checks before
// sending, mission_events has no other way to record "this already
// happened" (D-117, issue #843). A terminal marker carries only
// "kind"; an actionable marker also carries "event", the dedup key of
// the inbox event it answered (issue #922).
const notifiedKind = "mission.notified"

// notifyStore is the slice of Store NotifyConsumer needs; Store and
// fakeStore both satisfy it.
type notifyStore interface {
	Get(ctx context.Context, id string) (Mission, error)
	Events(ctx context.Context, id string) ([]Event, error)
	AppendEvent(ctx context.Context, id, kind string, payload map[string]any) error
}

// markerEvent returns a mission.notified record's "event" field, empty
// for a terminal marker.
func markerEvent(ev Event) string {
	var p struct {
		Event string `json:"event"`
	}
	_ = json.Unmarshal(ev.Payload, &p)
	return p.Event
}

// alreadyNotified reports whether events already carries a terminal
// mission.notified record. Actionable markers do not count.
func alreadyNotified(events []Event) bool {
	for _, ev := range events {
		if ev.Kind == notifiedKind && markerEvent(ev) == "" {
			return true
		}
	}
	return false
}

// notifiedFor reports whether events carries a mission.notified record
// for the actionable inbox event with dedupKey.
func notifiedFor(events []Event, dedupKey string) bool {
	for _, ev := range events {
		if ev.Kind == notifiedKind && markerEvent(ev) == dedupKey {
			return true
		}
	}
	return false
}

// NotifyConsumer is the events consumer that sends every mission
// transition notification: terminal (mission.done/mission.failed,
// D-117, issue #843) and actionable (mission.paused/
// mission.waiting_for_input, issue #922). A crash between
// ApplyTransition's commit and the send no longer loses it.
type NotifyConsumer struct {
	store  notifyStore
	notify func(ctx context.Context, missionID, kind, message string) error
	log    *slog.Logger
}

// NewNotifyConsumer wires the consumer. notify is Notifier.NotifyMessage;
// a nil notify (no notifier configured) makes Handle a no-op.
func NewNotifyConsumer(store notifyStore, notify func(ctx context.Context, missionID, kind, message string) error, log *slog.Logger) NotifyConsumer {
	return NotifyConsumer{store: store, notify: notify, log: log}
}

func (NotifyConsumer) Name() string { return "mission-notify" }

func (NotifyConsumer) Kinds() []string {
	return []string{events.KindMissionDone, events.KindMissionFailed, events.KindMissionPaused, events.KindMissionWaitingForInput}
}

// notifyKind maps an inbox event kind to the notification kind.
func notifyKind(eventKind string) (Status, bool) {
	switch eventKind {
	case events.KindMissionDone:
		return StatusDone, false
	case events.KindMissionFailed:
		return StatusError, false
	case events.KindMissionPaused:
		return StatusPaused, true
	case events.KindMissionWaitingForInput:
		return StatusWaitingForInput, true
	default:
		return "", false
	}
}

// Handle sends the notification then records notifiedKind: send before
// mark, so a crash between the two only risks a duplicate webhook
// fan-out (at-least-once), never a lost notification. A terminal event
// is sent once per mission; an actionable one once per inbox event, so
// a second pause notifies again. The marker is appended outside the
// drain tx: appendEventTx takes FOR UPDATE on the missions row, which
// would hold the lock for the whole drain batch if run inside it.
func (c NotifyConsumer) Handle(ctx context.Context, _ pgx.Tx, ev events.Event) error {
	p, err := events.DecodeMission(ev)
	if err != nil {
		return err
	}
	status, actionable := notifyKind(ev.Kind)
	if status == "" {
		return fmt.Errorf("notify: unexpected event kind %q", ev.Kind)
	}
	kind := string(status)
	if c.notify == nil {
		return nil
	}
	m, err := c.store.Get(ctx, p.MissionID)
	if errors.Is(err, ErrNotFound) {
		c.log.Info("notify: mission deleted, skipping", "mission_id", p.MissionID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("notify: load mission: %w", err)
	}
	// A mission that left the state before the drain ran needs no
	// notification: its resume already cleared the inbox, and a late row
	// would stay unread and mute the next pause (issue #935).
	if actionable && m.Status != status {
		return nil
	}
	evs, err := c.store.Events(ctx, p.MissionID)
	if err != nil {
		return fmt.Errorf("notify: load events: %w", err)
	}
	marker := map[string]any{"kind": kind}
	if actionable {
		if notifiedFor(evs, ev.DedupKey) {
			return nil
		}
		marker["event"] = ev.DedupKey
	} else if alreadyNotified(evs) {
		return nil
	}
	message := composeMessage(kind, PRTitle(m), p.Reason)
	if err := c.notify(ctx, p.MissionID, kind, message); err != nil {
		return fmt.Errorf("notify: send: %w", err)
	}
	if err := c.store.AppendEvent(ctx, p.MissionID, notifiedKind, marker); err != nil {
		return fmt.Errorf("notify: record event: %w", err)
	}
	return nil
}
