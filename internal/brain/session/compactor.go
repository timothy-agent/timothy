package session

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// Gateway is the slice of the gateway client the compactor needs.
type Gateway interface {
	Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error)
}

// Log is the slice of the event store the compactor needs; *Store
// satisfies it, tests fake it.
type Log interface {
	Events(ctx context.Context, sessionID string) ([]Event, error)
	Append(ctx context.Context, sessionID, kind string, payload any) (int64, error)
}

// Windows resolves model context windows from the gateway;
// *gwclient.Client satisfies it. May be nil (static budget only).
type Windows interface {
	ModelWindows(ctx context.Context) (map[string]int, error)
}

const (
	compactTimeout   = 60 * time.Second
	summaryMaxTokens = 1200
)

// summarizeSystem must preserve exactly what summaries usually lose
// (D-007/D-011 rationale).
const summarizeSystem = `Summarize this conversation excerpt for an AI assistant's context. You MUST explicitly preserve: every name, date, number, commitment, decision, and open question. Compress pleasantries and repetition aggressively. Write flowing prose, no headers, no preamble — output only the summary.`

// Compactor keeps projected contexts under a token budget by
// summarizing the oldest half of the live turns into a
// compaction_applied event. Automatic and incremental — never a
// destructive one-shot (D-006).
type Compactor struct {
	log      Log
	gw       Gateway
	windows  Windows // nil-safe: static budget only
	budget   int     // fallback when no model window is resolvable
	logger   *slog.Logger
	compacts prometheus.Counter // nil-safe: may be unset in tests
}

func NewCompactor(log Log, gw Gateway, windows Windows, budget int, logger *slog.Logger, compacts prometheus.Counter) *Compactor {
	return &Compactor{log: log, gw: gw, windows: windows, budget: budget, logger: logger, compacts: compacts}
}

// budgetFor sizes the token budget to the model that served the
// session's last turn: 60% of its context window per the gateway's
// provider info. Falls back to the configured static budget when the
// session has no completed turn yet, the lookup fails, or the model
// declares no window.
func (c *Compactor) budgetFor(ctx context.Context, sessionID string, events []Event) int {
	model := lastModel(events)
	if c.windows == nil || model == "" {
		return c.budget
	}
	ws, err := c.windows.ModelWindows(ctx)
	if err != nil {
		c.logger.Warn("model windows lookup, using static budget", "session_id", sessionID, "error", err)
		return c.budget
	}
	if w := ws[model]; w > 0 {
		return w * 60 / 100
	}
	return c.budget
}

// lastModel returns the model of the newest assistant_turn, or "".
func lastModel(events []Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != KindAssistantTurn {
			continue
		}
		var t AssistantTurn
		if decode(events[i], &t) == nil {
			return t.Model
		}
		return ""
	}
	return ""
}

// MaybeCompact measures the session's projected context and compacts
// once when it exceeds the budget. Chat runs it after each completed
// turn (proactive) and again before each send (the wire-level
// guarantee on the crossing turn); repeated passes converge because
// each one halves the live prefix.
func (c *Compactor) MaybeCompact(ctx context.Context, sessionID string) error {
	events, err := c.log.Events(ctx, sessionID)
	if err != nil {
		return err
	}
	budget := c.budgetFor(ctx, sessionID, events)
	msgs, err := LLMContext(events, budget)
	if err != nil {
		return err
	}
	tokens, err := EstimateTokens(msgs)
	if err != nil {
		return err
	}
	if tokens <= budget {
		return nil
	}

	boundary, toSummarize, ok := planCompaction(events)
	if !ok {
		return nil // nothing safely summarizable (e.g. one giant turn)
	}

	summary, err := c.summarize(ctx, sessionID, toSummarize)
	if err != nil {
		return fmt.Errorf("session: compact %s: %w", sessionID, err)
	}
	if _, err := c.log.Append(ctx, sessionID, KindCompactionApplied, CompactionApplied{
		Summary:            summary,
		ReplacesThroughSeq: boundary,
		FactsExtracted:     []string{}, // memory extraction wires in with memoryd
	}); err != nil {
		return err
	}
	if c.compacts != nil {
		c.compacts.Inc()
	}
	c.logger.Info("session compacted", "session_id", sessionID, "through_seq", boundary, "tokens_before", tokens)
	return nil
}

// planCompaction picks the summarization boundary: the oldest half of
// the conversation messages that survive the latest compaction,
// always keeping at least one full turn live. Pure and heavily
// testable.
func planCompaction(events []Event) (boundary int64, toSummarize []provider.Message, ok bool) {
	msgs, err := LLMContext(events, 0)
	if err != nil || len(msgs) < 4 {
		return 0, nil, false // too small to split into summary + live tail
	}

	cut := len(msgs) / 2
	// Never end the summarized half on a user message: a summary that
	// swallows a question the assistant hasn't answered yet reads
	// wrong. Extend to include the assistant reply when possible.
	for cut < len(msgs)-1 && msgs[cut-1].Role == "user" {
		cut++
	}
	if cut >= len(msgs) {
		return 0, nil, false
	}
	toSummarize = msgs[:cut]

	// Map the cut back to an event boundary. Past the latest
	// compaction, projected messages correspond 1:1, in order, to
	// user_message/assistant_turn events plus the one live pending
	// (a summary head projects from the compaction event itself;
	// superseded checkpoints and empty pendings project nothing). The
	// boundary is the seq of the last message event the summary
	// consumes; invisible events (tool digests) between boundary and
	// the next message simply stay outside the replaced range.
	start := 0
	if strings.HasPrefix(msgs[0].Content, summaryPrefix) {
		start = 1
	}
	need := cut - start
	if need <= 0 {
		return 0, nil, false
	}
	// Live events are those past the latest compaction's REPLACED
	// range — not past the compaction event itself, which sits at the
	// end of the log with the highest seq.
	var replacedThrough int64 = -1
	for _, ev := range events {
		if ev.Kind == KindCompactionApplied {
			var c CompactionApplied
			if decode(ev, &c) == nil {
				replacedThrough = c.ReplacesThroughSeq
			}
		}
	}
	livePending := livePendingSeq(events)
	count := 0
	for _, ev := range events {
		if ev.Seq <= replacedThrough {
			continue
		}
		switch ev.Kind {
		case KindUserMessage, KindAssistantTurn:
			count++
		case KindPendingState:
			// The live pending projects as one interrupted assistant
			// message; superseded checkpoints and empty partials don't.
			if ev.Seq != livePending {
				continue
			}
			var p PendingState
			if decode(ev, &p) != nil || p.Partial == "" {
				continue
			}
			count++
		default:
			continue
		}
		if count == need {
			return ev.Seq, toSummarize, true
		}
	}
	return 0, nil, false // projection/event mismatch: refuse to guess
}

func (c *Compactor) summarize(ctx context.Context, sessionID string, msgs []provider.Message) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, compactTimeout)
	defer cancel()

	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role + ": " + m.Content + "\n\n")
	}
	events, err := c.gw.Stream(ctx, gwclient.StreamRequest{
		TaskCategory: "summarize",
		System:       summarizeSystem,
		Messages:     []provider.Message{{Role: "user", Content: b.String()}},
		MaxTokens:    summaryMaxTokens,
		SessionID:    sessionID,
	})
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for ev := range events {
		switch ev.Type {
		case stream.EventChunk:
			out.WriteString(ev.Text)
		case stream.EventError:
			return "", fmt.Errorf("summarize: %s", ev.Err.Message)
		}
	}
	summary := strings.TrimSpace(out.String())
	if summary == "" {
		return "", fmt.Errorf("summarize: empty summary")
	}
	return summary, nil
}
