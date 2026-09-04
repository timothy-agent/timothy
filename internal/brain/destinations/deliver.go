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
	// github, unlike the rest of adapters, delivers through
	// GitHubAdapter.DeliverMission (push/PR, no Payload rendering, no
	// retry) rather than the Adapter interface, so deliverOne special-cases
	// kind == "github" instead of a map lookup.
	github   *GitHubAdapter
	webURL   func(ctx context.Context) string
	location func(ctx context.Context) *time.Location
	log      *slog.Logger
}

// NewDeliverer builds a Deliverer. webURL resolves the web_base_url
// setting fresh at delivery time (never cached on the struct) so an
// operator's later change applies without a restart. email/telegram/
// github nil (no google connectors / no secret store / no connectors
// wired, respectively) leaves that kind unregistered in adapters, so
// deliverOne's map lookup then reports "no adapter for kind" rather
// than boxing a nil adapter as a non-nil Adapter (which would panic on
// first field access inside Deliver). location follows the same
// fresh-read pattern as webURL; nil (or a nil *time.Location it
// returns) defaults to UTC.
func NewDeliverer(store destinationStore, events eventRecorder, email *EmailAdapter, webhook *WebhookAdapter, telegram *TelegramAdapter, github *GitHubAdapter, webURL func(ctx context.Context) string, location func(ctx context.Context) *time.Location, log *slog.Logger) *Deliverer {
	d := &Deliverer{
		store:    store,
		events:   events,
		adapters: map[string]Adapter{"webhook": webhook},
		github:   github,
		webURL:   webURL,
		location: location,
		log:      log,
	}
	if email != nil {
		d.adapters["email"] = email
	}
	if telegram != nil {
		d.adapters["telegram"] = telegram
	}
	return d
}

// eventDelivered/eventDeliveryFailed name the mission_events rows
// Deliver appends, one per destination per mission, purely for the
// Timeline/history now that delivery dedup itself reads each entry's
// own delivered_at/error (issue #480).
const (
	eventDelivered      = "mission.delivered"
	eventDeliveryFailed = "mission.delivery_failed"
)

// Deliver runs delivery for every entry in entries, best-effort per
// destination. Recipients get the mission's generated output (Render's
// Files, from the mission's declared plan-unit artifacts) plus a short
// completion line, never the goal/plan/review process digest.
// Idempotent under a re-drive: an entry whose DeliveredAt is already
// set is skipped, one with Error set (or never attempted) is retried.
// Returns the entries with DeliveredAt/Error updated in place (the
// caller persists them, see missions.Driver.deliverToDestinations)
// plus an error naming every destination that failed this round, or
// nil if all succeeded. No-ops immediately for an empty entries.
func (d *Deliverer) Deliver(ctx context.Context, m missions.Mission, entries []missions.DestinationEntry) ([]missions.DestinationEntry, error) {
	if len(entries) == 0 {
		return nil, nil
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

	out := make([]missions.DestinationEntry, len(entries))
	var failed []string
	for i, e := range entries {
		if e.DeliveredAt != "" {
			out[i] = e
			continue
		}
		if err := d.deliverOne(ctx, m, &e, payload); err != nil {
			failed = append(failed, e.DestinationID)
		}
		out[i] = e
	}
	if len(failed) > 0 {
		return out, fmt.Errorf("delivery failed for destination(s): %s", strings.Join(failed, ", "))
	}
	return out, nil
}

// deliverOne attempts one entry's delivery, mutating it in place with
// the outcome (DeliveredAt on success, Error on failure) and recording
// the same outcome as a mission_events row for the Timeline.
func (d *Deliverer) deliverOne(ctx context.Context, m missions.Mission, e *missions.DestinationEntry, payload Payload) error {
	missionID := m.ID
	destinationID := e.DestinationID
	dest, err := d.store.Get(ctx, destinationID)
	if err != nil {
		reason := "destination not found: " + err.Error()
		d.recordOutcome(ctx, missionID, e, "", reason)
		return errors.New(reason)
	}
	if !dest.Enabled {
		reason := "destination is disabled"
		d.recordOutcome(ctx, missionID, e, dest.Name, reason)
		return errors.New(reason)
	}

	if dest.Kind == "github" {
		// github delivery is push/PR, not a rendered Payload send: a
		// single attempt, no deliverBackoff retries (a push retry against
		// a half-pushed branch is a different risk profile than re-POSTing
		// a webhook).
		if d.github == nil {
			reason := "no adapter for kind github"
			d.recordOutcome(ctx, missionID, e, dest.Name, reason)
			return errors.New(reason)
		}
		var cfg GitHubConfig
		if err := json.Unmarshal(dest.Config, &cfg); err != nil {
			reason := "github config: " + err.Error()
			d.recordOutcome(ctx, missionID, e, dest.Name, reason)
			return errors.New(reason)
		}
		if err := d.github.DeliverMission(ctx, cfg, m, e); err != nil {
			d.recordOutcome(ctx, missionID, e, dest.Name, err.Error())
			return err
		}
		d.recordOutcome(ctx, missionID, e, dest.Name, "")
		return nil
	}

	adapter := d.adapters[dest.Kind]
	if adapter == nil {
		reason := "no adapter for kind " + dest.Kind
		d.recordOutcome(ctx, missionID, e, dest.Name, reason)
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
				d.recordOutcome(ctx, missionID, e, dest.Name, ctx.Err().Error())
				return ctx.Err()
			case <-timer.C:
			}
		}
		lastErr = adapter.Deliver(ctx, dest.Config, dest.CredentialRef, payload)
		if lastErr == nil {
			d.recordOutcome(ctx, missionID, e, dest.Name, "")
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
	d.recordOutcome(ctx, missionID, e, dest.Name, lastErr.Error())
	return lastErr
}

// recordOutcome sets entry's DeliveredAt (success, reason == "") or
// Error (failure) and appends the matching mission_events row for the
// Timeline.
func (d *Deliverer) recordOutcome(ctx context.Context, missionID string, e *missions.DestinationEntry, name, reason string) {
	kind := eventDelivered
	payload := map[string]any{"destination_id": e.DestinationID, "destination_name": name}
	if reason == "" {
		e.DeliveredAt = time.Now().UTC().Format(time.RFC3339)
		e.Error = ""
	} else {
		kind = eventDeliveryFailed
		e.Error = reason
		payload["reason"] = reason
	}
	if err := d.events.AppendEvent(ctx, missionID, kind, payload); err != nil {
		d.log.Warn("destinations: record outcome event failed", "mission_id", missionID, "destination_id", e.DestinationID, "error", err)
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
	if dest.Kind == "github" {
		return fmt.Errorf("github destinations have no test send: use the mission's push/pr actions instead")
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
	if dest.Kind == "github" {
		return "", "", fmt.Errorf("github destinations are not usable by the deliver tool")
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
