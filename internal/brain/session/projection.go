package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// summaryPrefix marks a compaction summary inside the LLM context.
const summaryPrefix = "[Summary of the conversation so far]\n"

// interruptedNote marks a spliced partial turn inside the LLM context.
const interruptedNote = "\n[this response was interrupted mid-stream; continue from it]"

// LLMContext projects the log into the messages the model sees.
//
// Rules (D-006/D-007/D-018):
//   - user_message → user message; assistant_turn → assistant message
//     (llm.message plus serialized turn memory).
//   - compaction_applied replaces everything ≤ replaces_through_seq
//     with one summary message.
//   - the newest pending_state splices in as an interrupted assistant
//     message at its position, unless a later assistant_turn or
//     compaction superseded it. User messages do NOT supersede a
//     pending — the question after an interruption must still see the
//     partial. Older pendings (periodic checkpoints) are superseded by
//     newer ones.
//   - tool_execution events never enter the LLM context (turn memory
//     carries their residue).
//
// The projection is prefix-stable: appending events never rewrites
// earlier messages — the prefix changes only when a compaction event
// lands. Provider prompt caches depend on this.
//
// budget is the caller's token ceiling; projection never trims to it
// (a sliding window would break prefix stability) — the compactor
// reacts to it instead.
func LLMContext(events []Event, budget int) ([]provider.Message, error) {
	_ = budget // enforced by the compactor, not the projection

	// Find the latest compaction: its summary replaces the prefix.
	var summary string
	var replacedThrough int64 = -1
	for _, ev := range events {
		if ev.Kind == KindCompactionApplied {
			var c CompactionApplied
			if err := decode(ev, &c); err != nil {
				return nil, err
			}
			summary = c.Summary
			replacedThrough = c.ReplacesThroughSeq
		}
	}

	livePending := livePendingSeq(events)

	var msgs []provider.Message
	if summary != "" {
		msgs = append(msgs, provider.Message{Role: "user", Content: summaryPrefix + summary})
	}

	for _, ev := range events {
		if ev.Seq <= replacedThrough {
			continue
		}
		switch ev.Kind {
		case KindUserMessage:
			var m UserMessage
			if err := decode(ev, &m); err != nil {
				return nil, err
			}
			msgs = append(msgs, provider.Message{Role: "user", Content: m.Text})
		case KindAssistantTurn:
			var t AssistantTurn
			if err := decode(ev, &t); err != nil {
				return nil, err
			}
			msgs = append(msgs, provider.Message{Role: "assistant", Content: renderAssistant(t)})
		case KindPendingState:
			if ev.Seq != livePending {
				continue // superseded checkpoint
			}
			var p PendingState
			if err := decode(ev, &p); err != nil {
				return nil, err
			}
			if p.Partial != "" {
				msgs = append(msgs, provider.Message{Role: "assistant", Content: p.Partial + interruptedNote})
			}
		case KindTurnMemory:
			var tm TurnMemoryEvent
			if err := decode(ev, &tm); err != nil {
				return nil, err
			}
			if block := renderTurnMemory(&tm.TurnMemory); block != "" {
				msgs = append(msgs, provider.Message{Role: "assistant", Content: block})
			}
		}
	}
	return msgs, nil
}

// livePendingSeq returns the seq of the pending_state still in play:
// the newest one, unless an assistant_turn landed after it or a
// compaction actually CONSUMED it (replaces_through_seq covers its
// seq). A compaction that summarized older turns but left the partial
// outside its boundary must not erase it — the partial is the one
// artifact the kill-test contract protects. Returns -1 when none is
// live.
func livePendingSeq(events []Event) int64 {
	var lastPending, lastSuperseder int64 = -1, -1
	for _, ev := range events {
		switch ev.Kind {
		case KindPendingState:
			lastPending = ev.Seq
		case KindAssistantTurn:
			lastSuperseder = ev.Seq
		case KindCompactionApplied:
			var c CompactionApplied
			if decode(ev, &c) == nil && c.ReplacesThroughSeq >= lastPending {
				lastSuperseder = ev.Seq
			}
		}
	}
	if lastPending > lastSuperseder {
		return lastPending
	}
	return -1
}

// renderAssistant serializes a turn for the LLM: the message plus,
// for events written before turn_memory became its own kind, the
// embedded residue block.
func renderAssistant(t AssistantTurn) string {
	block := renderTurnMemory(t.LLM.TurnMemory)
	if block == "" {
		return t.LLM.Message
	}
	return t.LLM.Message + "\n\n" + block
}

// renderTurnMemory serializes residue into its compact deterministic
// block; empty when there is nothing worth carrying.
func renderTurnMemory(tm *TurnMemory) string {
	if tm == nil || (len(tm.FilesChanged) == 0 && len(tm.Failures) == 0 && len(tm.KeyFindings) == 0) {
		return ""
	}
	var b strings.Builder
	b.WriteString("[turn memory]")
	if len(tm.FilesChanged) > 0 {
		b.WriteString("\nfiles changed: " + strings.Join(tm.FilesChanged, ", "))
	}
	for _, f := range tm.Failures {
		b.WriteString("\nfailure: " + f.What + " — " + f.Why)
	}
	for _, k := range tm.KeyFindings {
		b.WriteString("\nfinding: " + k)
	}
	return b.String()
}

// TranscriptItem is one renderable unit of the UI replay.
type TranscriptItem struct {
	Seq       int64          `json:"seq"`
	Kind      string         `json:"kind"` // user | assistant | tool | compaction | interrupted
	Text      string         `json:"text,omitempty"`
	Blocks    []UIBlock      `json:"blocks,omitempty"`
	Provider  string         `json:"provider,omitempty"`
	Model     string         `json:"model,omitempty"`
	Usage     *stream.Usage  `json:"usage,omitempty"`
	Tool      *ToolExecution `json:"tool,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// UITranscript projects the log into the full rich replay. Unlike the
// LLM context it hides nothing: compactions render as dividers, tool
// executions as digest blocks, and a trailing pending state as an
// interrupted turn.
func UITranscript(events []Event) ([]TranscriptItem, error) {
	livePending := livePendingSeq(events)
	var items []TranscriptItem
	for _, ev := range events {
		item := TranscriptItem{Seq: ev.Seq, CreatedAt: ev.CreatedAt}
		switch ev.Kind {
		case KindUserMessage:
			var m UserMessage
			if err := decode(ev, &m); err != nil {
				return nil, err
			}
			item.Kind, item.Text = "user", m.Text
		case KindAssistantTurn:
			var t AssistantTurn
			if err := decode(ev, &t); err != nil {
				return nil, err
			}
			item.Kind = "assistant"
			item.Blocks = t.UI.Blocks
			item.Provider, item.Model = t.Provider, t.Model
			item.Usage = t.Usage
		case KindToolExecution:
			var te ToolExecution
			if err := decode(ev, &te); err != nil {
				return nil, err
			}
			item.Kind, item.Tool = "tool", &te
		case KindCompactionApplied:
			var c CompactionApplied
			if err := decode(ev, &c); err != nil {
				return nil, err
			}
			item.Kind = "compaction"
			item.Text = fmt.Sprintf("older messages summarized (through #%d)", c.ReplacesThroughSeq)
		case KindPendingState:
			if ev.Seq != livePending {
				continue
			}
			var p PendingState
			if err := decode(ev, &p); err != nil {
				return nil, err
			}
			item.Kind, item.Text = "interrupted", p.Partial
		default:
			continue // session_started renders nothing
		}
		items = append(items, item)
	}
	return items, nil
}
