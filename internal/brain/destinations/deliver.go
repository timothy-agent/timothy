package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// deliverBackoff is the fixed retry schedule for one destination's
// delivery attempt: 2 retries (3 attempts total) with a short backoff
// — best-effort, never blocking the terminal transition it's called
// from for long.
var deliverBackoff = []time.Duration{1 * time.Second, 3 * time.Second}

// destinationStore is the narrow slice of *Store the Deliverer reads.
type destinationStore interface {
	Get(ctx context.Context, id string) (Destination, error)
}

// eventRecorder is the narrow slice of *missions.Store the Deliverer
// writes its outcome events through.
type eventRecorder interface {
	AppendEvent(ctx context.Context, missionID, kind string, payload map[string]any) error
	Events(ctx context.Context, missionID string) ([]missions.Event, error)
}

// Deliverer resolves a mission's destination_ids, renders the digest,
// and calls each destination's adapter: run SYNCHRONOUSLY from the
// driver's result phase step (D-086, see missions/driver.go). Every
// failure is logged and recorded as a mission_events row; Deliver also
// returns a non-nil error naming the failed destinations so the result
// step can park the mission and retry only those on the next round.
type Deliverer struct {
	store    destinationStore
	events   eventRecorder
	adapters map[string]Adapter
	webURL   func(ctx context.Context) string
	location func(ctx context.Context) *time.Location
	log      *slog.Logger
}

// NewDeliverer builds a Deliverer. webURL resolves the web_base_url
// setting fresh at delivery time (never cached on the struct) so an
// operator's later change applies without a restart. email/telegram
// nil (no google connectors / no secret store wired, respectively)
// leaves that kind unregistered in adapters — deliverOne's map lookup
// then reports "no adapter for kind" rather than boxing a nil adapter
// as a non-nil Adapter (which would panic on first field access
// inside Deliver). location follows the same fresh-read pattern as
// webURL; nil (or a nil *time.Location it returns) defaults to UTC.
func NewDeliverer(store destinationStore, events eventRecorder, email *EmailAdapter, webhook *WebhookAdapter, telegram *TelegramAdapter, webURL func(ctx context.Context) string, location func(ctx context.Context) *time.Location, log *slog.Logger) *Deliverer {
	adapters := map[string]Adapter{"webhook": webhook}
	if email != nil {
		adapters["email"] = email
	}
	if telegram != nil {
		adapters["telegram"] = telegram
	}
	return &Deliverer{
		store:    store,
		events:   events,
		adapters: adapters,
		webURL:   webURL,
		location: location,
		log:      log,
	}
}

// eventDelivered/eventDeliveryFailed name the mission_events rows
// Deliver appends — one per destination per mission, guarded below so
// a re-drive (Signal racing Advance) never double-delivers.
const (
	eventDelivered      = "mission.delivered"
	eventDeliveryFailed = "mission.delivery_failed"
)

// Deliver runs delivery for every id in destinationIDs, best-effort
// per destination. Recipients get the mission's generated output
// (Render's Files, from the mission's declared plan-unit artifacts)
// plus a short completion line, never the goal/plan/review process
// digest. Idempotent under a re-drive: a destination that already
// recorded mission.delivered is skipped, one that recorded
// mission.delivery_failed (or was never attempted) is retried. No-ops
// immediately for an empty destinationIDs. Returns an error naming
// every destination that failed this round, or nil if all succeeded
// (or there was nothing to deliver).
func (d *Deliverer) Deliver(ctx context.Context, m missions.Mission, destinationIDs []string) error {
	if len(destinationIDs) == 0 {
		return nil
	}
	delivered, err := d.alreadyDelivered(ctx, m.ID)
	if err != nil {
		d.log.Warn("destinations: load prior delivery events failed", "mission_id", m.ID, "error", err)
		return err
	}
	events, err := d.events.Events(ctx, m.ID)
	if err != nil {
		d.log.Warn("destinations: load events for render failed", "mission_id", m.ID, "error", err)
		events = nil
	}
	var webBaseURL string
	if d.webURL != nil {
		webBaseURL = d.webURL(ctx)
	}
	loc := time.UTC
	if d.location != nil {
		if l := d.location(ctx); l != nil {
			loc = l
		}
	}
	payload := Render(m, webBaseURL, events, loc)

	var failed []string
	for _, id := range destinationIDs {
		if delivered[id] {
			continue
		}
		if err := d.deliverOne(ctx, m.ID, id, payload); err != nil {
			failed = append(failed, id)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("delivery failed for destination(s): %s", strings.Join(failed, ", "))
	}
	return nil
}

func (d *Deliverer) deliverOne(ctx context.Context, missionID, destinationID string, payload Payload) error {
	dest, err := d.store.Get(ctx, destinationID)
	if err != nil {
		reason := "destination not found: " + err.Error()
		d.recordOutcome(ctx, missionID, destinationID, "", false, reason)
		return errors.New(reason)
	}
	if !dest.Enabled {
		reason := "destination is disabled"
		d.recordOutcome(ctx, missionID, destinationID, dest.Name, false, reason)
		return errors.New(reason)
	}
	adapter := d.adapters[dest.Kind]
	if adapter == nil {
		reason := "no adapter for kind " + dest.Kind
		d.recordOutcome(ctx, missionID, destinationID, dest.Name, false, reason)
		return errors.New(reason)
	}

	var lastErr error
	attempts := append([]time.Duration{0}, deliverBackoff...)
	for i, wait := range attempts {
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				d.recordOutcome(ctx, missionID, destinationID, dest.Name, false, ctx.Err().Error())
				return ctx.Err()
			case <-timer.C:
			}
		}
		lastErr = adapter.Deliver(ctx, dest.Config, dest.CredentialRef, payload)
		if lastErr == nil {
			d.recordOutcome(ctx, missionID, destinationID, dest.Name, true, "")
			return nil
		}
		d.log.Warn("destinations: delivery attempt failed", "mission_id", missionID, "destination_id", destinationID, "attempt", i+1, "error", lastErr)
		if errors.Is(lastErr, errMaybeDelivered) {
			// The request may have already reached the provider: retrying
			// risks sending the same message twice, so stop here rather than
			// continue the schedule.
			break
		}
	}
	d.recordOutcome(ctx, missionID, destinationID, dest.Name, false, lastErr.Error())
	return lastErr
}

func (d *Deliverer) recordOutcome(ctx context.Context, missionID, destinationID, name string, ok bool, reason string) {
	kind := eventDelivered
	payload := map[string]any{"destination_id": destinationID, "destination_name": name}
	if !ok {
		kind = eventDeliveryFailed
		payload["reason"] = reason
	}
	if err := d.events.AppendEvent(ctx, missionID, kind, payload); err != nil {
		d.log.Warn("destinations: record outcome event failed", "mission_id", missionID, "destination_id", destinationID, "error", err)
	}
}

// testPayload is the canned content a destination's test-send button
// delivers — no mission behind it, so every field is a fixed stand-in.
var testPayload = Payload{
	MissionID: "test",
	Name:      "Timothy test delivery",
	Goal:      "verify this destination is reachable",
	Body:      "This is a test delivery from Timothy to confirm this destination is configured correctly.",
}

// Test sends testPayload through id's real adapter, synchronously, no
// retry — the Settings "Test send" button's backing call. Unlike
// Deliver, this never touches mission_events (there is no mission) and
// reports its outcome directly to the caller instead of best-effort
// logging.
func (d *Deliverer) Test(ctx context.Context, id string) error {
	dest, err := d.store.Get(ctx, id)
	if err != nil {
		return err
	}
	adapter := d.adapters[dest.Kind]
	if adapter == nil {
		return fmt.Errorf("no adapter for kind %q", dest.Kind)
	}
	return adapter.Deliver(ctx, dest.Config, dest.CredentialRef, testPayload)
}

// DeliverNow sends subject+body to one destination synchronously, no
// retry — the deliver tool's backing call (chat and mission turns
// alike). Unlike Deliver/Test this never touches mission_events (the
// tool's own result string is the caller's record) and carries no
// files: attachments are the harness terminal-delivery path's concern
// only. Returns the destination's name and kind on success so the
// tool can report what it delivered to.
func (d *Deliverer) DeliverNow(ctx context.Context, id, subject, body string) (name, kind string, err error) {
	dest, err := d.store.Get(ctx, id)
	if err != nil {
		return "", "", err
	}
	if !dest.Enabled {
		return "", "", fmt.Errorf("destination %q is disabled", dest.Name)
	}
	adapter := d.adapters[dest.Kind]
	if adapter == nil {
		return "", "", fmt.Errorf("no adapter for kind %q", dest.Kind)
	}
	payload := Payload{Subject: subject, Body: body}
	if err := adapter.Deliver(ctx, dest.Config, dest.CredentialRef, payload); err != nil {
		return "", "", err
	}
	return dest.Name, dest.Kind, nil
}

// alreadyDelivered scans a mission's events for prior mission.delivered
// rows, keyed by destination_id: a destination already delivered is
// never re-sent on a result-phase retry (D-086), but one that only
// recorded mission.delivery_failed (or was never attempted) IS
// retried, so a park in result actually makes progress instead of
// replaying the same permanent failure forever.
func (d *Deliverer) alreadyDelivered(ctx context.Context, missionID string) (map[string]bool, error) {
	events, err := d.events.Events(ctx, missionID)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, ev := range events {
		if ev.Kind != eventDelivered {
			continue
		}
		var payload struct {
			DestinationID string `json:"destination_id"`
		}
		if jsonErr := json.Unmarshal(ev.Payload, &payload); jsonErr == nil && payload.DestinationID != "" {
			out[payload.DestinationID] = true
		}
	}
	return out, nil
}
