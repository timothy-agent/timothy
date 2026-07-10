// Package session is the event-sourced session store: an append-only
// log per session and two pure projections over it — the UI transcript
// (rich replay) and the LLM context (what the model sees). They are
// deliberately separate documents (D-006).
package session

import (
	"encoding/json"
	"fmt"
	"time"
)

// Event kinds. The log is append-only: kinds are added, never changed.
const (
	KindSessionStarted    = "session_started"
	KindUserMessage       = "user_message"
	KindAssistantTurn     = "assistant_turn"
	KindToolExecution     = "tool_execution"
	KindCompactionApplied = "compaction_applied"
	KindPendingState      = "pending_state"
)

// Event is one row of a session's log.
type Event struct {
	SessionID string
	Seq       int64
	Kind      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// SessionStarted opens a session.
type SessionStarted struct {
	Title string `json:"title,omitempty"`
}

// UserMessage is one user turn.
type UserMessage struct {
	Text      string `json:"text"`
	Category  string `json:"category,omitempty"`
	ModelHint string `json:"model_hint,omitempty"`
}

// UIBlock is one renderable piece of an assistant turn.
type UIBlock struct {
	Type string         `json:"type"` // text | reasoning | tool_call | meta
	Text string         `json:"text,omitempty"`
	Meta map[string]any `json:"meta,omitempty"`
}

// TurnMemory is the structured residue distilled from a turn's raw
// traffic (D-007): what the model needs across turns, nothing else.
type TurnMemory struct {
	FilesChanged []string  `json:"files_changed,omitempty"`
	Failures     []Failure `json:"failures,omitempty"`
	KeyFindings  []string  `json:"key_findings,omitempty"`
}

// Failure records one failed command or tool call and why.
type Failure struct {
	What string `json:"what"`
	Why  string `json:"why"`
}

// AssistantTurn carries both projections' source material: the UI
// blocks for replay and the LLM-facing message + residue.
type AssistantTurn struct {
	UI struct {
		Blocks []UIBlock `json:"blocks"`
	} `json:"ui"`
	LLM struct {
		Message    string      `json:"message"`
		TurnMemory *TurnMemory `json:"turn_memory,omitempty"`
	} `json:"llm"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	LedgerID string `json:"ledger_id,omitempty"`
}

// ToolExecution stores a digest only; full results are transient.
type ToolExecution struct {
	CallID       string `json:"call_id"`
	Name         string `json:"name"`
	Args         string `json:"args,omitempty"`
	ResultDigest string `json:"result_digest,omitempty"`
	Status       string `json:"status"`
}

// CompactionApplied replaces every event up to and including
// ReplacesThroughSeq with its summary in the LLM projection.
type CompactionApplied struct {
	Summary            string   `json:"summary"`
	ReplacesThroughSeq int64    `json:"replaces_through_seq"`
	FactsExtracted     []string `json:"facts_extracted"`
}

// PendingState holds a turn's accumulated deltas when it ended
// abnormally; the next request splices it in, then it is superseded.
type PendingState struct {
	Partial string `json:"partial"`
}

// decode unmarshals an event payload into out with kind context on
// error.
func decode(ev Event, out any) error {
	if err := json.Unmarshal(ev.Payload, out); err != nil {
		return fmt.Errorf("session: decode %s @%d: %w", ev.Kind, ev.Seq, err)
	}
	return nil
}
