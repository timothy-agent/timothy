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
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// SourceMission marks events produced by a mission transition.
	SourceMission = "mission"

	KindMissionDone   = "mission.done"
	KindMissionFailed = "mission.failed"

	// SourceManual marks events an operator requested through the API.
	SourceManual = "manual"
	// KindRunNow asks for one run of an automation.
	KindRunNow = "run.now"
)

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

// MissionPayload is the payload of a mission.done or mission.failed
// event. Empty strings are omitted.
type MissionPayload struct {
	MissionID     string `json:"mission_id"`
	Phase         string `json:"phase"`
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

// DecodeMission reads a mission terminal event's payload.
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

// newRequestID returns a random RFC 4122 version 4 UUID.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
