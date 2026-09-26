package missions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

// notifiedKind marks that a mission's terminal notification has
// already fired — the append-only idempotency record NotifyConsumer
// checks before sending, mission_events has no other way to record
// "this already happened" (D-117, issue #843).
const notifiedKind = "mission.notified"

// notifyStore is the slice of Store NotifyConsumer needs; Store and
// fakeStore both satisfy it.
type notifyStore interface {
	Get(ctx context.Context, id string) (Mission, error)
	Events(ctx context.Context, id string) ([]Event, error)
	AppendEvent(ctx context.Context, id, kind string, payload map[string]any) error
}

// alreadyNotified reports whether events already carries a
// mission.notified record.
func alreadyNotified(events []Event) bool {
	for _, ev := range events {
		if ev.Kind == notifiedKind {
			return true
		}
	}
	return false
}

// NotifyConsumer is the events consumer that sends a mission's
// terminal notification for every mission.done/mission.failed event
// (D-117, issue #843): the send OnTransition used to do synchronously
// now rides the durable inbox, so a crash between ApplyTransition's
// commit and the notification no longer loses it.
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
	return []string{events.KindMissionDone, events.KindMissionFailed}
}

// Handle sends the terminal notification then records notifiedKind —
// send before mark, so a crash between the two only risks a duplicate
// webhook fan-out (at-least-once), never a lost notification. The
// marker is appended outside the drain tx: appendEventTx takes FOR
// UPDATE on the missions row, which would hold the lock for the whole
// drain batch if run inside it.
func (c NotifyConsumer) Handle(ctx context.Context, _ pgx.Tx, ev events.Event) error {
	p, err := events.DecodeMission(ev)
	if err != nil {
		return err
	}
	kind := string(StatusDone)
	if p.Phase == "failed" {
		kind = string(StatusError)
	}
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
	evs, err := c.store.Events(ctx, p.MissionID)
	if err != nil {
		return fmt.Errorf("notify: load events: %w", err)
	}
	if alreadyNotified(evs) {
		return nil
	}
	message := composeMessage(kind, PRTitle(m), p.Reason)
	if err := c.notify(ctx, p.MissionID, kind, message); err != nil {
		return fmt.Errorf("notify: send: %w", err)
	}
	if err := c.store.AppendEvent(ctx, p.MissionID, notifiedKind, map[string]any{"kind": kind}); err != nil {
		return fmt.Errorf("notify: record event: %w", err)
	}
	return nil
}
