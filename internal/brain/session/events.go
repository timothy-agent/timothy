// Package session is the event-sourced session store: an append-only
// log per session and two pure projections over it — the UI transcript
// (rich replay) and the LLM context (what the model sees). They are
// deliberately separate documents (D-006).
package session

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// Event kinds. The log is append-only: kinds are added, never changed.
const (
	KindSessionStarted     = "session_started"
	KindUserMessage        = "user_message"
	KindAssistantTurn      = "assistant_turn"
	KindToolExecution      = "tool_execution"
	KindCompactionApplied  = "compaction_applied"
	KindPendingState       = "pending_state"
	KindTurnMemory         = "turn_memory"
	KindPermissionRequest  = "permission_request"
	KindPermissionResolved = "permission_resolved"
	// KindTurnFailed records a turn that ended without a usable answer —
	// a terminal error/incomplete with nothing worth keeping as partial
	// text, or a completed turn with no text, reasoning, or tool
	// executions (D-044) — so the event log carries evidence instead of
	// silence.
	KindTurnFailed = "turn_failed"
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
	Route     string `json:"route,omitempty"`
	Agent     string `json:"agent,omitempty"`
	ModelHint string `json:"model_hint,omitempty"`
	// Images are refs to attachments (internal/brain/attachments),
	// never bytes — base64 exists only transiently at request-build
	// time (D-045). Additive: a message with no images omits this.
	Images []ImageRef `json:"images,omitempty"`
	// Documents are PDF attachments converted to markdown once, at
	// message-send time (chat.Chat), with the markdown persisted here.
	// Re-converting per turn would re-call the markitdown sidecar every
	// turn, and any output drift would rewrite an earlier projected
	// message — breaking LLMContext's prefix stability that provider
	// prompt caches depend on. Additive: a message with no documents
	// omits this.
	Documents []DocumentRef `json:"documents,omitempty"`
}

// ImageRef points at a stored attachment; Mime rides alongside so
// projections and drivers can build a data: URL without a store
// round-trip. Name is the original filename, additive: events
// persisted before D-0xx filename threading omit it, and the UI falls
// back to its existing short-hash label in that case.
type ImageRef struct {
	ID   string `json:"id"`
	Mime string `json:"mime"`
	Name string `json:"name,omitempty"`
}

// DocumentRef points at a stored document attachment (PDF, text,
// audio, video); Markdown is its converted text or transcript,
// persisted so projection is deterministic and the markitdown/whisper
// sidecar is called exactly once per attach (see UserMessage.Documents).
// Video attachments and audio attachments with no transcript (whisper
// unconfigured) carry an empty Markdown — never sent to the model as
// content, see projection.go. Name is additive, same as ImageRef.
type DocumentRef struct {
	ID       string `json:"id"`
	Mime     string `json:"mime"`
	Markdown string `json:"markdown"`
	Name     string `json:"name,omitempty"`
}

// UIBlock is one renderable piece of an assistant turn.
type UIBlock struct {
	Type string         `json:"type"` // text | reasoning | tool_call | meta | media
	Text string         `json:"text,omitempty"`
	Meta map[string]any `json:"meta,omitempty"`
	// Media carries refs only (never bytes, D-045) for a "media" block
	// — content a tool call generated during the turn (share_file,
	// read_mail_attachment). Additive: absent on every block type that
	// predates this.
	Media []MediaRef `json:"media,omitempty"`
}

// MediaRef points at one attachment-store item a tool generated during
// a turn — id, mime, and an optional display name, never bytes
// (D-045). Mirrors stream.MediaRef; converted at the chat.go boundary.
type MediaRef struct {
	ID   string `json:"id"`
	Mime string `json:"mime"`
	Name string `json:"name,omitempty"`
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

// TurnMemoryEvent carries a turn's distilled residue as its own
// appended event. Distillation is an LLM call that finishes seconds
// after the turn is visible — persisting the assistant_turn first and
// the residue later keeps completed turns durable immediately and the
// projected prefix stable (the residue projects as a NEW message at
// its own position, never a rewrite of the turn it describes).
type TurnMemoryEvent struct {
	TurnSeq int64 `json:"turn_seq"` // the assistant_turn this distills
	TurnMemory
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
	Provider string        `json:"provider,omitempty"`
	Model    string        `json:"model,omitempty"`
	LedgerID string        `json:"ledger_id,omitempty"`
	Usage    *stream.Usage `json:"usage,omitempty"`
	// DurationMs is the turn's wall-clock time (runTurn entry to
	// persistence), not the sum of tool durations — set by chat.go so
	// live and replayed stats agree.
	DurationMs int64 `json:"duration_ms,omitempty"`
	// Cost and Currency mirror the gateway meta's cost attribution at
	// write time (D-013: unknown price is never guessed) — Cost is nil
	// and Currency blank when the serving model had no price, and both
	// stay absent through omitempty rather than persisting a 0.
	Cost     *float64 `json:"cost,omitempty"`
	Currency string   `json:"currency,omitempty"`
}

// ToolExecution stores a digest only; full results are transient.
type ToolExecution struct {
	CallID       string `json:"call_id"`
	Name         string `json:"name"`
	Args         string `json:"args,omitempty"`
	ResultDigest string `json:"result_digest,omitempty"`
	Status       string `json:"status"`
	DurationMs   int64  `json:"duration_ms,omitempty"`
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

// TurnFailed records a turn that produced no usable answer: Code is a
// short machine-readable reason (e.g. a terminal error/incomplete's
// code, or "empty_response" for a completed turn with nothing to
// show), Message is the human-readable detail.
type TurnFailed struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PermissionRequest records a parked tool call awaiting approval
// (mirrors stream.PermissionRequestEvent). A session opened while the
// turn is still parked replays this so the same approval prompt the
// live stream shows appears on reload too; a matching
// PermissionResolved (by ID) means it is no longer pending.
type PermissionRequest struct {
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Tool      string `json:"tool"`
	Args      string `json:"args"`
	Danger    string `json:"danger_level"`
	Rationale string `json:"rationale"`
}

// PermissionResolved records the decision a parked ask received
// (mirrors stream.PermissionResolvedEvent). ID ties it to the
// PermissionRequest it resolves.
type PermissionResolved struct {
	ID       string `json:"id"`
	Decision string `json:"decision"`
}

// decode unmarshals an event payload into out with kind context on
// error.
func decode(ev Event, out any) error {
	if err := json.Unmarshal(ev.Payload, out); err != nil {
		return fmt.Errorf("session: decode %s @%d: %w", ev.Kind, ev.Seq, err)
	}
	return nil
}
