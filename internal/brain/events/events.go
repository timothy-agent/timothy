// Package events is the durable inbox for side effects that must
// survive a crash (D-117): a producer inserts an events row in the same
// transaction as the state change that caused it, and the Drainer runs
// every registered Consumer for it after commit, retrying up to
// maxAttempts.
package events

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	// SourceMission marks events produced by a mission transition.
	SourceMission = "mission"

	KindMissionDone   = "mission.done"
	KindMissionFailed = "mission.failed"
	// KindMissionPaused and KindMissionWaitingForInput mark a mission
	// arriving at an actionable status.
	KindMissionPaused          = "mission.paused"
	KindMissionWaitingForInput = "mission.waiting_for_input"

	// SourceManual marks events an operator requested through the API.
	SourceManual = "manual"
	// KindRunNow asks for one run of an automation.
	KindRunNow = "run.now"

	// SourceCron marks events the automations cron ticker produced.
	SourceCron = "cron"
	// KindCronDue is one due boundary of a cron trigger.
	KindCronDue = "cron.due"

	// SourceConnector marks events a connector poller produced.
	SourceConnector = "connector"

	KindPROpened        = "pr.opened"
	KindPRLabeled       = "pr.labeled"
	KindPRReview        = "pr.review"
	KindPRReviewComment = "pr.review_comment"
	KindIssueComment    = "issue.comment"
	KindCheckCompleted  = "check.completed"

	// MaxConnectorBodyBytes caps a connector event's body.
	MaxConnectorBodyBytes = 4096

	// SourceWebhook marks events an inbound webhook delivery produced.
	SourceWebhook = "webhook"
	// KindWebhookReceived is one verified webhook delivery.
	KindWebhookReceived = "webhook.received"

	// MaxWebhookBodyBytes caps the JSON body a webhook event keeps.
	MaxWebhookBodyBytes = 64 << 10

	// SourceChannel marks events a channel message produced.
	SourceChannel = "channel"
	// KindChannelMessage is one paired sender's message that matched a
	// channel trigger.
	KindChannelMessage = "channel.message"
	// MaxChannelTextRunes caps a channel event's text, the channel
	// input cap.
	MaxChannelTextRunes = 4000
	// maxDeliveryBytes caps a delivery id in the dedup key.
	maxDeliveryBytes = 256
)

// ConnectorKinds returns every connector event kind.
func ConnectorKinds() []string {
	return []string{KindPROpened, KindPRLabeled, KindPRReview, KindPRReviewComment, KindIssueComment, KindCheckCompleted}
}

// Event is one events row.
type Event struct {
	ID        int64
	Source    string
	Kind      string
	DedupKey  string
	Payload   json.RawMessage
	CreatedAt time.Time
	Attempts  int
}

// Consumer reacts to events of the kinds it names. Handle MUST be
// idempotent: the drainer delivers at least once, so a crash after
// Handle returns but before the batch commits delivers the same event
// again, and a failing event is retried up to maxAttempts times. tx is
// the drain transaction scoped to this event's savepoint: writes made
// through it commit only when every consumer for the event succeeds.
type Consumer interface {
	Name() string
	Kinds() []string
	Handle(ctx context.Context, tx pgx.Tx, ev Event) error
}

// MissionPayload is the payload of a mission event. Status is set on
// actionable events only. Empty strings are omitted.
type MissionPayload struct {
	MissionID     string `json:"mission_id"`
	Phase         string `json:"phase"`
	Status        string `json:"status,omitempty"`
	Reason        string `json:"reason,omitempty"`
	WorkflowRunID string `json:"workflow_run_id,omitempty"`
	OriginKind    string `json:"origin_kind,omitempty"`
	Unattended    bool   `json:"unattended"`
}

// MissionTerminal builds the event for a mission reaching phase done
// or failed, deduplicated by mission id.
func MissionTerminal(p MissionPayload) (Event, error) {
	var kind string
	switch p.Phase {
	case "done":
		kind = KindMissionDone
	case "failed":
		kind = KindMissionFailed
	default:
		return Event{}, fmt.Errorf("events: phase %q is not terminal", p.Phase)
	}
	if p.MissionID == "" {
		return Event{}, fmt.Errorf("events: mission terminal event needs a mission id")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return Event{}, fmt.Errorf("events: marshal mission payload: %w", err)
	}
	return Event{Source: SourceMission, Kind: kind, DedupKey: p.MissionID, Payload: raw}, nil
}

// MissionActionable builds the event for a mission arriving at status
// paused or waiting_for_input. The dedup key carries a fresh request id:
// a mission can pause many times, and a transition with no mission
// events has no sequence number to key on.
func MissionActionable(p MissionPayload) (Event, error) {
	var kind string
	switch p.Status {
	case "paused":
		kind = KindMissionPaused
	case "waiting_for_input":
		kind = KindMissionWaitingForInput
	default:
		return Event{}, fmt.Errorf("events: status %q is not actionable", p.Status)
	}
	if p.MissionID == "" {
		return Event{}, fmt.Errorf("events: mission actionable event needs a mission id")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return Event{}, fmt.Errorf("events: marshal mission payload: %w", err)
	}
	return Event{Source: SourceMission, Kind: kind, DedupKey: p.MissionID + ":" + p.Status + ":" + newRequestID(), Payload: raw}, nil
}

// DecodeMission reads a mission event's payload.
func DecodeMission(ev Event) (MissionPayload, error) {
	var p MissionPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return MissionPayload{}, fmt.Errorf("events: decode mission payload of event %d: %w", ev.ID, err)
	}
	if p.MissionID == "" {
		return MissionPayload{}, fmt.Errorf("events: event %d has no mission_id", ev.ID)
	}
	return p, nil
}

// RunNowPayload is the payload of a run.now event.
type RunNowPayload struct {
	AutomationID string    `json:"automation_id"`
	RequestedAt  time.Time `json:"requested_at"`
}

// RunNow builds the event for an operator's run-now request on an
// automation, deduplicated by a fresh request id.
func RunNow(automationID string, at time.Time) (Event, error) {
	if automationID == "" {
		return Event{}, fmt.Errorf("events: run.now event needs an automation id")
	}
	raw, err := json.Marshal(RunNowPayload{AutomationID: automationID, RequestedAt: at.UTC().Truncate(time.Second)})
	if err != nil {
		return Event{}, fmt.Errorf("events: marshal run.now payload: %w", err)
	}
	return Event{Source: SourceManual, Kind: KindRunNow, DedupKey: newRequestID(), Payload: raw}, nil
}

// DecodeRunNow reads a run.now event's payload.
func DecodeRunNow(ev Event) (RunNowPayload, error) {
	var p RunNowPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return RunNowPayload{}, fmt.Errorf("events: decode run.now payload of event %d: %w", ev.ID, err)
	}
	if p.AutomationID == "" {
		return RunNowPayload{}, fmt.Errorf("events: event %d has no automation_id", ev.ID)
	}
	return p, nil
}

// CronDuePayload is the payload of a cron.due event. Boundary carries
// the operator timezone offset.
type CronDuePayload struct {
	AutomationID string    `json:"automation_id"`
	TriggerID    string    `json:"trigger_id"`
	Boundary     time.Time `json:"boundary"`
}

// CronDue builds the event for one due boundary of a cron trigger,
// deduplicated by trigger id and the boundary's UTC instant.
func CronDue(automationID, triggerID string, boundary time.Time) (Event, error) {
	if automationID == "" || triggerID == "" {
		return Event{}, fmt.Errorf("events: cron.due event needs an automation and a trigger id")
	}
	raw, err := json.Marshal(CronDuePayload{AutomationID: automationID, TriggerID: triggerID, Boundary: boundary})
	if err != nil {
		return Event{}, fmt.Errorf("events: marshal cron.due payload: %w", err)
	}
	return Event{Source: SourceCron, Kind: KindCronDue, DedupKey: triggerID + "|" + boundary.UTC().Format(time.RFC3339), Payload: raw}, nil
}

// DecodeCronDue reads a cron.due event's payload.
func DecodeCronDue(ev Event) (CronDuePayload, error) {
	var p CronDuePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return CronDuePayload{}, fmt.Errorf("events: decode cron.due payload of event %d: %w", ev.ID, err)
	}
	if p.AutomationID == "" || p.TriggerID == "" || p.Boundary.IsZero() {
		return CronDuePayload{}, fmt.Errorf("events: event %d is missing automation_id, trigger_id or boundary", ev.ID)
	}
	return p, nil
}

// ConnectorEventPayload is the payload of a connector event. Self is
// true when the connector's own identity authored it.
type ConnectorEventPayload struct {
	Provider        string    `json:"provider"`
	ConnectorID     string    `json:"connector_id"`
	Repo            string    `json:"repo"`
	Kind            string    `json:"kind"`
	Action          string    `json:"action,omitempty"`
	Number          int       `json:"number,omitempty"`
	Title           string    `json:"title,omitempty"`
	URL             string    `json:"url,omitempty"`
	Author          string    `json:"author,omitempty"`
	Labels          []string  `json:"labels,omitempty"`
	Body            string    `json:"body,omitempty"`
	PullRequest     bool      `json:"pull_request,omitempty"`
	Conclusion      string    `json:"conclusion,omitempty"`
	CheckName       string    `json:"check_name,omitempty"`
	Self            bool      `json:"self"`
	ProviderEventID string    `json:"provider_event_id"`
	OccurredAt      time.Time `json:"occurred_at"`
}

// ConnectorEvent builds the event for one normalized provider event,
// deduplicated by provider, connector and provider event id. Body is
// capped at MaxConnectorBodyBytes.
func ConnectorEvent(p ConnectorEventPayload) (Event, error) {
	if !slices.Contains(ConnectorKinds(), p.Kind) {
		return Event{}, fmt.Errorf("events: unknown connector event kind %q", p.Kind)
	}
	if p.Provider == "" || p.ConnectorID == "" || p.ProviderEventID == "" {
		return Event{}, fmt.Errorf("events: connector event needs a provider, a connector id and a provider event id")
	}
	p.Body = capUTF8(p.Body, MaxConnectorBodyBytes)
	raw, err := json.Marshal(p)
	if err != nil {
		return Event{}, fmt.Errorf("events: marshal connector payload: %w", err)
	}
	return Event{Source: SourceConnector, Kind: p.Kind, DedupKey: p.Provider + ":" + p.ConnectorID + ":" + p.ProviderEventID, Payload: raw}, nil
}

// DecodeConnectorEvent reads a connector event's payload.
func DecodeConnectorEvent(ev Event) (ConnectorEventPayload, error) {
	var p ConnectorEventPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return ConnectorEventPayload{}, fmt.Errorf("events: decode connector payload of event %d: %w", ev.ID, err)
	}
	if p.ConnectorID == "" || p.ProviderEventID == "" {
		return ConnectorEventPayload{}, fmt.Errorf("events: event %d is missing connector_id or provider_event_id", ev.ID)
	}
	return p, nil
}

// WebhookPayload is the payload of a webhook.received event. Body is
// the JSON body, or {"raw_truncated": true} when it was not JSON or
// exceeded MaxWebhookBodyBytes.
type WebhookPayload struct {
	TriggerID    string            `json:"trigger_id"`
	AutomationID string            `json:"automation_id"`
	Scheme       string            `json:"scheme"`
	Delivery     string            `json:"delivery"`
	ReceivedAt   time.Time         `json:"received_at"`
	Headers      map[string]string `json:"headers"`
	Body         json.RawMessage   `json:"body"`
}

// WebhookReceived builds the event for one verified delivery,
// deduplicated by trigger id and delivery id.
func WebhookReceived(p WebhookPayload) (Event, error) {
	if p.TriggerID == "" || p.AutomationID == "" || p.Delivery == "" {
		return Event{}, fmt.Errorf("events: webhook event needs a trigger id, an automation id and a delivery id")
	}
	p.Delivery = capUTF8(p.Delivery, maxDeliveryBytes)
	if len(p.Body) > MaxWebhookBodyBytes || !json.Valid(p.Body) {
		p.Body = json.RawMessage(`{"raw_truncated":true}`)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return Event{}, fmt.Errorf("events: marshal webhook payload: %w", err)
	}
	return Event{Source: SourceWebhook, Kind: KindWebhookReceived, DedupKey: "hook:" + p.TriggerID + ":" + p.Delivery, Payload: raw}, nil
}

// DecodeWebhook reads a webhook.received event's payload.
func DecodeWebhook(ev Event) (WebhookPayload, error) {
	var p WebhookPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return WebhookPayload{}, fmt.Errorf("events: decode webhook payload of event %d: %w", ev.ID, err)
	}
	if p.TriggerID == "" || p.AutomationID == "" || p.Delivery == "" {
		return WebhookPayload{}, fmt.Errorf("events: event %d is missing trigger_id, automation_id or delivery", ev.ID)
	}
	return p, nil
}

// ChannelMessagePayload is the payload of a channel.message event.
// TriggerID is the trigger the channel matched; the dispatcher fires
// only that one.
type ChannelMessagePayload struct {
	ChannelID      string `json:"channel_id"`
	ConversationID string `json:"conversation_id"`
	ChatID         string `json:"chat_id"`
	ThreadID       string `json:"thread_id,omitempty"`
	UserID         string `json:"user_id"`
	Sender         string `json:"sender"`
	MessageID      string `json:"message_id"`
	Text           string `json:"text"`
	TriggerID      string `json:"trigger_id"`
}

// ChannelMessage builds the event for one channel message,
// deduplicated by channel id and the channel's inbound dedup id. Text
// is capped at MaxChannelTextRunes.
func ChannelMessage(p ChannelMessagePayload, dedupID string) (Event, error) {
	if p.ChannelID == "" || p.TriggerID == "" || dedupID == "" {
		return Event{}, fmt.Errorf("events: channel event needs a channel id, a trigger id and a dedup id")
	}
	if utf8.RuneCountInString(p.Text) > MaxChannelTextRunes {
		p.Text = string([]rune(p.Text)[:MaxChannelTextRunes])
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return Event{}, fmt.Errorf("events: marshal channel payload: %w", err)
	}
	return Event{Source: SourceChannel, Kind: KindChannelMessage, DedupKey: "channel:" + p.ChannelID + ":" + dedupID, Payload: raw}, nil
}

// DecodeChannelMessage reads a channel.message event's payload.
func DecodeChannelMessage(ev Event) (ChannelMessagePayload, error) {
	var p ChannelMessagePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return ChannelMessagePayload{}, fmt.Errorf("events: decode channel payload of event %d: %w", ev.ID, err)
	}
	if p.ChannelID == "" || p.TriggerID == "" {
		return ChannelMessagePayload{}, fmt.Errorf("events: event %d is missing channel_id or trigger_id", ev.ID)
	}
	return p, nil
}

// capUTF8 cuts s to at most max bytes on a rune boundary.
func capUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// newRequestID returns a random RFC 4122 version 4 UUID.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
