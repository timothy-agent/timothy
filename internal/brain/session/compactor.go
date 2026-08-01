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

// renderForSummary flattens one message to plain text for extraction/
// summarization prompts: image content never rides as bytes here (only
// runTurn resolves ImageRefs into base64, and only just before the
// gateway call for the real turn) — a ref becomes a short textual note
// instead (D-045).
func renderForSummary(m provider.Message) string {
	text := m.Content
	for range m.ImageRefs {
		text += " [image attached]"
	}
	return m.Role + ": " + text + "\n\n"
}

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
	compactTimeout = 60 * time.Second
	// Reasoning-forward models (GLM) spend output tokens thinking
	// before writing; a tight cap gets consumed entirely by hidden
	// reasoning and yields an empty summary. The summary itself stays
	// ~1200 tokens; the rest is thinking headroom.
	summaryMaxTokens = 3000
)

// summarizeSystem must preserve exactly what summaries usually lose
// (D-007/D-011 rationale).
const summarizeSystem = `Summarize this conversation excerpt for an AI assistant's context. You MUST explicitly preserve: every name, date, number, commitment, decision, and open question. Compress pleasantries and repetition aggressively. Write flowing prose, no headers, no preamble — output only the summary.`

// Compactor keeps projected contexts under a token budget by
// summarizing the oldest half of the live turns into a
// compaction_applied event. Automatic and incremental — never a
// destructive one-shot (D-006).
type Compactor struct {
	log     Log
	gw      Gateway
	windows Windows // nil-safe: static budget only
	// budget resolves the fallback cap when no model window is
	// resolvable — a func because it is a runtime setting, editable
	// without a restart.
	budget    func(context.Context) int
	extract   MemoryExtract
	sensitive *SensitiveTools // nil: no sensitive-tool route pin for summarize
	logger    *slog.Logger
	compacts  prometheus.Counter // nil-safe: may be unset in tests
}

// MemoryExtract sends turns about to be summarized to memoryd and
// returns the extracted memory ids. Runs BEFORE summarization so
// names, dates, and commitments survive the summary (D-011). Failures
// return nil — extraction must never block compaction. route carries
// the sensitive-tool route pin (see SetSensitiveTools) when the
// session being extracted from is sensitive, "" otherwise — matches
// chat.MemoryExtract's param order.
type MemoryExtract func(ctx context.Context, sessionID string, seq int64, text string, route string) []string

func NewCompactor(log Log, gw Gateway, windows Windows, budget func(context.Context) int, logger *slog.Logger, compacts prometheus.Counter) *Compactor {
	return &Compactor{log: log, gw: gw, windows: windows, budget: budget, logger: logger, compacts: compacts}
}

// SetMemoryExtract wires the memoryd hook. Optional — nil skips
// pre-compaction extraction.
func (c *Compactor) SetMemoryExtract(fn MemoryExtract) { c.extract = fn }

// SetSensitiveTools wires the sensitive-tool route pin for the
// summarize call: a session where any tool_execution event matches
// summarizes on t.Route instead of the compactor's own default route.
// Optional — nil leaves every session's summarize on today's behavior.
func (c *Compactor) SetSensitiveTools(t *SensitiveTools) { c.sensitive = t }

// budgetFor sizes the token budget to the model that served the
// session's last turn: 60% of its context window per the gateway's
// provider info. Falls back to the configured static budget when the
// session has no completed turn yet, the lookup fails, or the model
// declares no window.
func (c *Compactor) budgetFor(ctx context.Context, sessionID string, events []Event) int {
	model := lastModel(events)
	if c.windows == nil || model == "" {
		return c.budget(ctx)
	}
	ws, err := c.windows.ModelWindows(ctx)
	if err != nil {
		c.logger.Warn("model windows lookup, using static budget", "session_id", sessionID, "error", err)
		return c.budget(ctx)
	}
	if w := ws[model]; w > 0 {
		return w * 60 / 100
	}
	return c.budget(ctx)
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

	// Sensitive if ANY tool_execution event in the WHOLE session matches
	// — compaction can summarize any span of the session's history, not
	// just the newest turns, so a sensitive tool call anywhere in it
	// taints both the extract and summarize calls for the same reason
	// the loop pins the rest of a sensitive TURN's route (the content
	// downstream of it may quote raw sensitive output). Computed once so
	// extract and summarize agree on the same verdict.
	sensitive := c.sensitive.SessionSensitive(events)

	// Extract BEFORE summarizing: once the summary replaces these
	// turns, whatever it dropped is gone for good (D-011). The turns
	// being extracted are exactly the ones that may carry sensitive
	// content, so extraction rides the same route pin as summarize.
	facts := []string{}
	if c.extract != nil {
		var b strings.Builder
		for _, m := range toSummarize {
			b.WriteString(renderForSummary(m))
		}
		extractRoute := ""
		if sensitive && c.sensitive != nil && c.sensitive.Route != nil {
			extractRoute = c.sensitive.Route(ctx)
		}
		if ids := c.extract(ctx, sessionID, boundary, b.String(), extractRoute); ids != nil {
			facts = ids
		}
	}

	summary, err := c.summarize(ctx, sessionID, toSummarize, sensitive)
	if err != nil {
		return fmt.Errorf("session: compact %s: %w", sessionID, err)
	}
	if _, err := c.log.Append(ctx, sessionID, KindCompactionApplied, CompactionApplied{
		Summary:            summary,
		ReplacesThroughSeq: boundary,
		FactsExtracted:     facts,
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
		case KindTurnMemory:
			// Residue projects as one message when it carries anything.
			var tm TurnMemoryEvent
			if decode(ev, &tm) != nil || renderTurnMemory(&tm.TurnMemory) == "" {
				continue
			}
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

func (c *Compactor) summarize(ctx context.Context, sessionID string, msgs []provider.Message, sensitive bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, compactTimeout)
	defer cancel()

	route := "summarize"
	if sensitive && c.sensitive != nil && c.sensitive.Route != nil {
		if forced := c.sensitive.Route(ctx); forced != "" {
			route = forced
		}
	}
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(renderForSummary(m))
	}
	events, err := c.gw.Stream(ctx, gwclient.StreamRequest{
		Route: route,
		Purpose:      "compaction",
		System:       summarizeSystem,
		Messages:     []provider.Message{{Role: "user", Content: b.String()}},
		MaxTokens:    summaryMaxTokens,
		// Summaries are transcription, not reasoning. Low effort also
		// keeps reasoning-forward models (GLM) answering in the text
		// channel instead of burning the budget on hidden thinking.
		Effort:    "low",
		SessionID: sessionID,
	})
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for ev := range events {
		switch ev.Type {
		case stream.EventChunk:
			out.WriteString(ev.Text)
		case stream.EventIncomplete:
			// A truncated summary silently loses facts — refuse it and
			// try again next turn rather than store a lie.
			return "", fmt.Errorf("summarize: stream incomplete")
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
