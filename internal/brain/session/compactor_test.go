package session

import (
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
	summary string
	sawText string
	calls   int
}

func (g *summarizerGW) Stream(_ context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	g.calls++
	g.sawText = req.Messages[0].Content
	ch := make(chan stream.StreamEvent, 2)
	ch <- stream.StreamEvent{Type: stream.EventChunk, Text: g.summary}
	ch <- stream.StreamEvent{Type: stream.EventDone}
	close(ch)
	return ch, nil
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
	c := NewCompactor(log, gw, 1_000_000, discardLogger(), nil)

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
	c := NewCompactor(log, gw, 500, discardLogger(), nil) // force compaction

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
			c := NewCompactor(log, gw, budget, discardLogger(), nil)

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

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
