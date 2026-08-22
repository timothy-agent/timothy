package session

import (
	"reflect"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// memLog is an in-memory Log for compactor tests.
type memLog struct {
	events map[string][]Event
}

func newMemLog() *memLog { return &memLog{events: map[string][]Event{}} }

func (m *memLog) Events(_ context.Context, id string) ([]Event, error) {
	return append([]Event(nil), m.events[id]...), nil
}

func (m *memLog) Append(_ context.Context, id, kind string, payload any) (int64, error) {
	ev := Event{SessionID: id, Seq: int64(len(m.events[id]) + 1), Kind: kind}
	data, err := jsonMarshal(payload)
	if err != nil {
		return 0, err
	}
	ev.Payload = data
	m.events[id] = append(m.events[id], ev)
	return ev.Seq, nil
}

// summarizerGW returns a fixed short summary and records its input.
type summarizerGW struct {
	summary   string
	sawText   string
	sawSystem string
	sawRoute  string
	calls     int
}

func (g *summarizerGW) RouteForRole(_ context.Context, role string) (string, bool, error) {
	return role, true, nil
}

func (g *summarizerGW) Stream(_ context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	g.calls++
	g.sawText = req.Messages[0].Content
	g.sawSystem = req.System
	g.sawRoute = req.Route
	ch := make(chan stream.StreamEvent, 2)
	ch <- stream.StreamEvent{Type: stream.EventChunk, Text: g.summary}
	ch <- stream.StreamEvent{Type: stream.EventDone}
	close(ch)
	return ch, nil
}

func staticBudget(n int) func(context.Context) int {
	return func(context.Context) int { return n }
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// seedConversation appends n user/assistant turn pairs with padded
// content so token counts are meaningful.
func seedConversation(t *testing.T, log *memLog, id string, turns int) {
	t.Helper()
	pad := strings.Repeat("lorem ipsum dolor sit amet ", 30)
	for i := 0; i < turns; i++ {
		if _, err := log.Append(context.Background(), id, KindUserMessage,
			UserMessage{Text: fmt.Sprintf("question %d about topic-%d %s", i, i, pad)}); err != nil {
			t.Fatal(err)
		}
		var turn AssistantTurn
		turn.LLM.Message = fmt.Sprintf("answer %d %s", i, pad)
		if _, err := log.Append(context.Background(), id, KindAssistantTurn, turn); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMaybeCompactUnderBudgetIsNoop(t *testing.T) {
	t.Parallel()
	log, gw := newMemLog(), &summarizerGW{summary: "s"}
	seedConversation(t, log, "s1", 3)
	c := NewCompactor(log, gw, nil, staticBudget(1_000_000), discardLogger(), nil)

	if err := c.MaybeCompact(t.Context(), "s1"); err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if gw.calls != 0 {
		t.Fatal("summarizer called while under budget")
	}
}

func TestMaybeCompactSummarizesOldestHalf(t *testing.T) {
	t.Parallel()
	log := newMemLog()
	gw := &summarizerGW{summary: "compact summary: topics 0 through N discussed"}
	seedConversation(t, log, "s1", 10)
	c := NewCompactor(log, gw, nil, staticBudget(500), discardLogger(), nil) // force compaction

	if err := c.MaybeCompact(t.Context(), "s1"); err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if gw.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", gw.calls)
	}
	// Oldest content reached the summarizer; newest stayed out.
	if !strings.Contains(gw.sawText, "question 0") || strings.Contains(gw.sawText, "question 9") {
		t.Fatalf("summarizer saw wrong slice (has q0=%v, has q9=%v)",
			strings.Contains(gw.sawText, "question 0"), strings.Contains(gw.sawText, "question 9"))
	}

	events, _ := log.Events(t.Context(), "s1")
	msgs, err := LLMContext(events, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	if !strings.HasPrefix(msgs[0].Content, summaryPrefix) {
		t.Fatalf("first message is not the summary: %+v", msgs[0])
	}
	// The newest turns survive verbatim.
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "answer 9") {
		t.Fatalf("tail not verbatim: %+v", last)
	}
}

// TestCompactionConverges is the property test: for various session
// shapes, repeated compaction brings the projection under budget while
// post-compaction turns stay verbatim.
func TestCompactionConverges(t *testing.T) {
	t.Parallel()
	for _, turns := range []int{4, 7, 12, 25} {
		t.Run(fmt.Sprintf("turns=%d", turns), func(t *testing.T) {
			t.Parallel()
			log := newMemLog()
			gw := &summarizerGW{summary: "short summary"}
			id := fmt.Sprintf("s-%d", turns)
			seedConversation(t, log, id, turns)
			budget := 800
			c := NewCompactor(log, gw, nil, staticBudget(budget), discardLogger(), nil)

			for i := 0; i < 12; i++ { // bounded iterations must suffice
				events, _ := log.Events(t.Context(), id)
				msgs, err := LLMContext(events, 0)
				if err != nil {
					t.Fatalf("LLMContext: %v", err)
				}
				tokens, err := EstimateTokens(msgs)
				if err != nil {
					t.Fatalf("EstimateTokens: %v", err)
				}
				if tokens <= budget {
					return // converged
				}
				before := len(msgs)
				if err := c.MaybeCompact(t.Context(), id); err != nil {
					t.Fatalf("MaybeCompact: %v", err)
				}
				events, _ = log.Events(t.Context(), id)
				after, err := LLMContext(events, 0)
				if err != nil {
					t.Fatalf("LLMContext after: %v", err)
				}
				if len(after) >= before {
					t.Fatalf("compaction did not shrink projection: %d -> %d", before, len(after))
				}
				// Newest turn stays verbatim through every pass.
				want := fmt.Sprintf("answer %d", turns-1)
				if !strings.Contains(after[len(after)-1].Content, want) {
					t.Fatalf("newest turn lost after compaction: %+v", after[len(after)-1])
				}
			}
			t.Fatal("did not converge under budget within bounded compactions")
		})
	}
}

func TestPlanCompactionRefusesTinySessions(t *testing.T) {
	t.Parallel()
	log := newMemLog()
	seedConversation(t, log, "s1", 1) // 2 messages: below the split floor
	events, _ := log.Events(t.Context(), "s1")

	if _, _, ok := planCompaction(events); ok {
		t.Fatal("planCompaction split a session too small to summarize")
	}
}

func TestPlanCompactionNeverEndsOnUserMessage(t *testing.T) {
	t.Parallel()
	log := newMemLog()
	seedConversation(t, log, "s1", 3)
	events, _ := log.Events(t.Context(), "s1")

	boundary, toSummarize, ok := planCompaction(events)
	if !ok {
		t.Fatal("planCompaction refused a splittable session")
	}
	if toSummarize[len(toSummarize)-1].Role == "user" {
		t.Fatalf("summarized half ends on a user message (boundary %d)", boundary)
	}
}

// fakeWindows is an in-memory Windows lookup.
type fakeWindows struct {
	windows map[string]int
	err     error
}

func (f *fakeWindows) ModelWindows(context.Context) (map[string]int, error) {
	return f.windows, f.err
}

// TestBudgetFollowsModelWindow pins the budget contract: 60% of the
// context window of the model that served the last turn, static
// fallback when the lookup fails or the model is unknown.
func TestBudgetFollowsModelWindow(t *testing.T) {
	t.Parallel()
	seed := func(t *testing.T, log *memLog) {
		seedConversation(t, log, "s1", 10) // thousands of tokens
		var turn AssistantTurn
		turn.LLM.Message = "closing answer"
		turn.Model = "narrow-model"
		if _, err := log.Append(context.Background(), "s1", KindAssistantTurn, turn); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("window shrinks budget below static default", func(t *testing.T) {
		t.Parallel()
		log, gw := newMemLog(), &summarizerGW{summary: "s"}
		seed(t, log)
		windows := &fakeWindows{windows: map[string]int{"narrow-model": 1000}} // budget 600
		c := NewCompactor(log, gw, windows, staticBudget(1_000_000), discardLogger(), nil)
		if err := c.MaybeCompact(t.Context(), "s1"); err != nil {
			t.Fatalf("MaybeCompact: %v", err)
		}
		if gw.calls != 1 {
			t.Fatalf("summarizer calls = %d, want 1 (model window must override static budget)", gw.calls)
		}
	})

	t.Run("lookup failure falls back to static budget", func(t *testing.T) {
		t.Parallel()
		log, gw := newMemLog(), &summarizerGW{summary: "s"}
		seed(t, log)
		windows := &fakeWindows{err: fmt.Errorf("gateway down")}
		c := NewCompactor(log, gw, windows, staticBudget(1_000_000), discardLogger(), nil)
		if err := c.MaybeCompact(t.Context(), "s1"); err != nil {
			t.Fatalf("MaybeCompact: %v", err)
		}
		if gw.calls != 0 {
			t.Fatal("compacted against a failed window lookup instead of the static budget")
		}
	})

	t.Run("unknown model falls back to static budget", func(t *testing.T) {
		t.Parallel()
		log, gw := newMemLog(), &summarizerGW{summary: "s"}
		seed(t, log)
		windows := &fakeWindows{windows: map[string]int{"other-model": 1000}}
		c := NewCompactor(log, gw, windows, staticBudget(1_000_000), discardLogger(), nil)
		if err := c.MaybeCompact(t.Context(), "s1"); err != nil {
			t.Fatalf("MaybeCompact: %v", err)
		}
		if gw.calls != 0 {
			t.Fatal("compacted with an unknown model instead of the static budget")
		}
	})
}

// TestPlanCompactionCountsLivePending pins the cut→seq mapping when a
// live pending_state projects inside the summarized half (possible
// when user messages pile up after an interruption): the boundary must
// account for the pending's projected message, not skip past it.
func TestPlanCompactionCountsLivePending(t *testing.T) {
	t.Parallel()
	log := newMemLog()
	ctx := context.Background()
	pad := strings.Repeat("pad ", 20)
	if _, err := log.Append(ctx, "s1", KindUserMessage, UserMessage{Text: "start " + pad}); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(ctx, "s1", KindPendingState, PendingState{Partial: "interrupted answer " + pad}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := log.Append(ctx, "s1", KindUserMessage, UserMessage{Text: fmt.Sprintf("nudge %d %s", i, pad)}); err != nil {
			t.Fatal(err)
		}
	}

	events, _ := log.Events(ctx, "s1")
	before, err := LLMContext(events, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	boundary, toSummarize, ok := planCompaction(events)
	if !ok {
		t.Fatal("planCompaction refused a session with a live pending in the summarized half")
	}

	// Apply the plan and check the live tail is exactly the messages
	// the summary did not consume — a mis-mapped boundary would swallow
	// or duplicate a message.
	if _, err := log.Append(ctx, "s1", KindCompactionApplied, CompactionApplied{
		Summary: "sum", ReplacesThroughSeq: boundary,
	}); err != nil {
		t.Fatal(err)
	}
	events, _ = log.Events(ctx, "s1")
	after, err := LLMContext(events, 0)
	if err != nil {
		t.Fatalf("LLMContext after: %v", err)
	}
	tail, want := after[1:], before[len(toSummarize):]
	if len(tail) != len(want) {
		t.Fatalf("live tail = %d messages, want %d\n after: %+v\n want tail: %+v", len(tail), len(want), after, want)
	}
	for i := range want {
		if !reflect.DeepEqual(tail[i], want[i]) {
			t.Fatalf("tail[%d] = %+v, want %+v", i, tail[i], want[i])
		}
	}
}

// TestSummarizeInputPreservesFacts pins the fidelity pipeline: the
// facts a summary must keep (names, dates, numbers, commitments) reach
// the summarizer verbatim, under a system prompt that demands their
// preservation.
func TestSummarizeInputPreservesFacts(t *testing.T) {
	t.Parallel()
	log, gw := newMemLog(), &summarizerGW{summary: "s"}
	ctx := context.Background()
	pad := strings.Repeat("lorem ipsum dolor sit amet ", 30)
	facts := []string{"Dr. Ada Marlowe", "2026-08-14", "invoice #4471", "$3,250"}
	if _, err := log.Append(ctx, "s1", KindUserMessage, UserMessage{
		Text: "Meet " + facts[0] + " on " + facts[1] + " about " + facts[2] + " for " + facts[3] + " " + pad,
	}); err != nil {
		t.Fatal(err)
	}
	var turn AssistantTurn
	turn.LLM.Message = "noted, meeting confirmed " + pad
	if _, err := log.Append(ctx, "s1", KindAssistantTurn, turn); err != nil {
		t.Fatal(err)
	}
	seedConversation(t, log, "s1", 6) // push the facts into the oldest half

	c := NewCompactor(log, gw, nil, staticBudget(500), discardLogger(), nil)
	if err := c.MaybeCompact(ctx, "s1"); err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if gw.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", gw.calls)
	}
	for _, f := range facts {
		if !strings.Contains(gw.sawText, f) {
			t.Fatalf("fact %q never reached the summarizer:\n%s", f, gw.sawText)
		}
	}
	for _, demand := range []string{"preserve", "name", "date", "number", "commitment"} {
		if !strings.Contains(gw.sawSystem, demand) {
			t.Fatalf("summarize system prompt missing %q: %s", demand, gw.sawSystem)
		}
	}
}

// TestSummarizeInputRendersImageNoteNotBytes confirms a user_message
// carrying image refs reaches the summarizer as a short textual note
// ("[image attached]"), never as base64 or any image identifier — the
// summarizer must never see attachment bytes (D-045), and refs alone
// (ids, mime types) are not useful summary content either.
func TestSummarizeInputRendersImageNoteNotBytes(t *testing.T) {
	t.Parallel()
	log, gw := newMemLog(), &summarizerGW{summary: "s"}
	ctx := context.Background()
	pad := strings.Repeat("lorem ipsum dolor sit amet ", 30)
	if _, err := log.Append(ctx, "s1", KindUserMessage, UserMessage{
		Text:   "what is in this photo? " + pad,
		Images: []ImageRef{{ID: "abc123", Mime: "image/png"}},
	}); err != nil {
		t.Fatal(err)
	}
	var turn AssistantTurn
	turn.LLM.Message = "a cat " + pad
	if _, err := log.Append(ctx, "s1", KindAssistantTurn, turn); err != nil {
		t.Fatal(err)
	}
	seedConversation(t, log, "s1", 6) // push the image message into the oldest half

	c := NewCompactor(log, gw, nil, staticBudget(500), discardLogger(), nil)
	if err := c.MaybeCompact(ctx, "s1"); err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if gw.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", gw.calls)
	}
	if !strings.Contains(gw.sawText, "[image attached]") {
		t.Fatalf("summarizer input missing the image note:\n%s", gw.sawText)
	}
	if strings.Contains(gw.sawText, "abc123") {
		t.Fatalf("summarizer input leaked the attachment id:\n%s", gw.sawText)
	}
}

// TestHundredTurnSessionPreservesReference is the acceptance harness
// in miniature: a 100-turn session compacts (possibly repeatedly)
// under a tight budget, and afterwards a commitment stated in turn 3
// is still present in the projected LLM context — via the summary
// head — so a follow-up question can reference it.
func TestHundredTurnSessionPreservesReference(t *testing.T) {
	t.Parallel()
	const fact = "the launch review is committed for 2026-08-14"
	log := newMemLog()
	// The fake summarizer behaves like the real prompt demands: it
	// carries the commitment through every pass.
	gw := &summarizerGW{summary: "Earlier discussion; " + fact + "."}
	ctx := context.Background()
	pad := strings.Repeat("lorem ipsum dolor sit amet ", 10)

	budget := 2000
	c := NewCompactor(log, gw, nil, staticBudget(budget), discardLogger(), nil)
	for i := 0; i < 100; i++ {
		text := fmt.Sprintf("question %d %s", i, pad)
		if i == 2 {
			text = "Remember: " + fact + ". " + pad
		}
		if _, err := log.Append(ctx, "s1", KindUserMessage, UserMessage{Text: text}); err != nil {
			t.Fatal(err)
		}
		var turn AssistantTurn
		turn.LLM.Message = fmt.Sprintf("answer %d %s", i, pad)
		if _, err := log.Append(ctx, "s1", KindAssistantTurn, turn); err != nil {
			t.Fatal(err)
		}
		// As in production: compaction runs after every completed turn.
		if err := c.MaybeCompact(ctx, "s1"); err != nil {
			t.Fatalf("MaybeCompact turn %d: %v", i, err)
		}
	}

	events, _ := log.Events(ctx, "s1")
	msgs, err := LLMContext(events, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	tokens, err := EstimateTokens(msgs)
	if err != nil {
		t.Fatalf("EstimateTokens: %v", err)
	}
	if tokens > budget {
		t.Fatalf("projection = %d tokens after 100 turns, budget %d", tokens, budget)
	}
	if gw.calls == 0 {
		t.Fatal("no compaction ever fired — the test proved nothing")
	}
	// The commitment from turn 3 must be visible to the model NOW.
	var all strings.Builder
	for _, m := range msgs {
		all.WriteString(m.Content + "\n")
	}
	if !strings.Contains(all.String(), fact) {
		t.Fatalf("turn-3 commitment lost from projected context:\n%s", all.String())
	}
	// And the newest turn survives verbatim next to it.
	if !strings.Contains(all.String(), "answer 99") {
		t.Fatal("newest turn missing from projected context")
	}
}

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func TestCompactionExtractsBeforeSummarizing(t *testing.T) {
	t.Parallel()
	log := newMemLog()
	gw := &summarizerGW{summary: "short summary"}
	seedConversation(t, log, "s1", 10)
	c := NewCompactor(log, gw, nil, staticBudget(500), discardLogger(), nil)

	var sawText string
	var extractedAtCall int
	c.SetMemoryExtract(func(_ context.Context, sessionID string, seq int64, text, _ string) []string {
		sawText = text
		extractedAtCall = gw.calls // 0 = before the summarizer ran
		if sessionID != "s1" || seq == 0 {
			t.Errorf("extract got sessionID=%s seq=%d", sessionID, seq)
		}
		return []string{"mem-1", "mem-2"}
	})

	if err := c.MaybeCompact(t.Context(), "s1"); err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if extractedAtCall != 0 {
		t.Fatal("extraction ran after summarization; facts may already be lost")
	}
	if !strings.Contains(sawText, "question 0") {
		t.Fatalf("extractor did not see the summarized turns: %q", sawText[:80])
	}

	events, _ := log.Events(t.Context(), "s1")
	var applied CompactionApplied
	for _, ev := range events {
		if ev.Kind == KindCompactionApplied {
			if err := decode(ev, &applied); err != nil {
				t.Fatalf("decode: %v", err)
			}
		}
	}
	if len(applied.FactsExtracted) != 2 || applied.FactsExtracted[0] != "mem-1" {
		t.Fatalf("facts_extracted = %v, want [mem-1 mem-2]", applied.FactsExtracted)
	}
}

// TestCompactionUsesSensitiveRouteWhenSessionRanSensitiveTool pins the
// summarize route pin: a session carrying ANY tool_execution event that
// matches the wired SensitiveTools summarizes on the sensitive route
// instead of the compactor's own default, mirroring the in-turn
// SetForceRoute pin the loop already applies within a turn.
func TestCompactionUsesSensitiveRouteWhenSessionRanSensitiveTool(t *testing.T) {
	t.Parallel()
	log := newMemLog()
	gw := &summarizerGW{summary: "short summary"}
	seedConversation(t, log, "s1", 10)
	if _, err := log.Append(context.Background(), "s1", KindToolExecution, ToolExecution{
		CallID: "c1", Name: "personal_gmail_read", Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	c := NewCompactor(log, gw, nil, staticBudget(500), discardLogger(), nil)
	c.SetSensitiveTools(&SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	if err := c.MaybeCompact(t.Context(), "s1"); err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if gw.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", gw.calls)
	}
	if gw.sawRoute != "local" {
		t.Fatalf("summarize route = %q, want local (session ran a sensitive tool)", gw.sawRoute)
	}
}

// TestCompactionUsesDefaultRouteWithoutSensitiveTool proves the pin is
// per-session, not global: a session with no matching tool_execution
// event summarizes on the compactor's own default route even with
// SensitiveTools wired.
func TestCompactionUsesDefaultRouteWithoutSensitiveTool(t *testing.T) {
	t.Parallel()
	log := newMemLog()
	gw := &summarizerGW{summary: "short summary"}
	seedConversation(t, log, "s1", 10)
	c := NewCompactor(log, gw, nil, staticBudget(500), discardLogger(), nil)
	c.SetSensitiveTools(&SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	if err := c.MaybeCompact(t.Context(), "s1"); err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if gw.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", gw.calls)
	}
	if gw.sawRoute != "summarize" {
		t.Fatalf("summarize route = %q, want summarize (no sensitive tool ran)", gw.sawRoute)
	}
}

// TestCompactionExtractUsesSensitiveRouteWhenSessionRanSensitiveTool
// proves the pre-compaction extract hook honors the same route pin as
// summarize: the turns handed to extract are exactly the ones that may
// carry sensitive content, so a sensitive tool call anywhere in the
// session must pin the extract call's route too.
func TestCompactionExtractUsesSensitiveRouteWhenSessionRanSensitiveTool(t *testing.T) {
	t.Parallel()
	log := newMemLog()
	gw := &summarizerGW{summary: "short summary"}
	seedConversation(t, log, "s1", 10)
	if _, err := log.Append(context.Background(), "s1", KindToolExecution, ToolExecution{
		CallID: "c1", Name: "personal_gmail_read", Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	c := NewCompactor(log, gw, nil, staticBudget(500), discardLogger(), nil)
	c.SetSensitiveTools(&SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	var sawRoute string
	c.SetMemoryExtract(func(_ context.Context, _ string, _ int64, _ string, route string) []string {
		sawRoute = route
		return nil
	})

	if err := c.MaybeCompact(t.Context(), "s1"); err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if sawRoute != "local" {
		t.Fatalf("extract route = %q, want local (session ran a sensitive tool)", sawRoute)
	}
}

// TestCompactionExtractUsesEmptyRouteWithoutSensitiveTool proves the
// extract route pin is per-session, not global: a session with no
// matching tool_execution event extracts on "" even with
// SensitiveTools wired.
func TestCompactionExtractUsesEmptyRouteWithoutSensitiveTool(t *testing.T) {
	t.Parallel()
	log := newMemLog()
	gw := &summarizerGW{summary: "short summary"}
	seedConversation(t, log, "s1", 10)
	c := NewCompactor(log, gw, nil, staticBudget(500), discardLogger(), nil)
	c.SetSensitiveTools(&SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	sawRoute := "unset"
	c.SetMemoryExtract(func(_ context.Context, _ string, _ int64, _ string, route string) []string {
		sawRoute = route
		return nil
	})

	if err := c.MaybeCompact(t.Context(), "s1"); err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if sawRoute != "" {
		t.Fatalf("extract route = %q, want empty (no sensitive tool ran)", sawRoute)
	}
}

func TestCompactionSurvivesExtractionFailure(t *testing.T) {
	t.Parallel()
	log := newMemLog()
	gw := &summarizerGW{summary: "short summary"}
	seedConversation(t, log, "s1", 10)
	c := NewCompactor(log, gw, nil, staticBudget(500), discardLogger(), nil)
	c.SetMemoryExtract(func(context.Context, string, int64, string, string) []string {
		return nil // extraction failed upstream; hook contract returns nil
	})

	if err := c.MaybeCompact(t.Context(), "s1"); err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	events, _ := log.Events(t.Context(), "s1")
	found := false
	for _, ev := range events {
		if ev.Kind == KindCompactionApplied {
			found = true
			var applied CompactionApplied
			if err := decode(ev, &applied); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if applied.FactsExtracted == nil || len(applied.FactsExtracted) != 0 {
				t.Fatalf("facts_extracted = %#v, want empty non-nil", applied.FactsExtracted)
			}
		}
	}
	if !found {
		t.Fatal("compaction did not happen despite extraction failure")
	}
}
