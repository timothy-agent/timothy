package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

func ev(t *testing.T, seq int64, kind string, payload any) Event {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return Event{SessionID: "s", Seq: seq, Kind: kind, Payload: data}
}

func user(t *testing.T, seq int64, text string) Event {
	return ev(t, seq, KindUserMessage, UserMessage{Text: text})
}

func assistant(t *testing.T, seq int64, msg string, tm *TurnMemory) Event {
	var a AssistantTurn
	a.LLM.Message = msg
	a.LLM.TurnMemory = tm
	a.UI.Blocks = []UIBlock{{Type: "text", Text: msg}}
	a.Provider, a.Model = "prov", "mod"
	return ev(t, seq, KindAssistantTurn, a)
}

func TestLLMContextBasicConversation(t *testing.T) {
	t.Parallel()
	events := []Event{
		ev(t, 1, KindSessionStarted, SessionStarted{}),
		user(t, 2, "hello"),
		assistant(t, 3, "hi there", nil),
		user(t, 4, "how are you"),
	}

	msgs, err := LLMContext(events, 100_000)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	want := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "user", Content: "how are you"},
	}
	if len(msgs) != len(want) {
		t.Fatalf("messages = %+v, want %+v", msgs, want)
	}
	for i := range want {
		if msgs[i] != want[i] {
			t.Fatalf("msg[%d] = %+v, want %+v", i, msgs[i], want[i])
		}
	}
}

func TestLLMContextSerializesTurnMemory(t *testing.T) {
	t.Parallel()
	events := []Event{
		user(t, 1, "fix the bug"),
		assistant(t, 2, "done", &TurnMemory{
			FilesChanged: []string{"main.go"},
			Failures:     []Failure{{What: "go test", Why: "flaky network"}},
			KeyFindings:  []string{"root cause was a nil map"},
		}),
	}

	msgs, err := LLMContext(events, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	got := msgs[1].Content
	for _, want := range []string{"[turn memory]", "files changed: main.go", "go test — flaky network", "finding: root cause was a nil map"} {
		if !strings.Contains(got, want) {
			t.Fatalf("assistant content missing %q:\n%s", want, got)
		}
	}
}

func TestLLMContextCompactionReplacesPrefix(t *testing.T) {
	t.Parallel()
	events := []Event{
		user(t, 1, "old question one"),
		assistant(t, 2, "old answer one", nil),
		user(t, 3, "old question two"),
		assistant(t, 4, "old answer two", nil),
		ev(t, 5, KindCompactionApplied, CompactionApplied{
			Summary: "user asked two old questions; both answered", ReplacesThroughSeq: 4,
		}),
		user(t, 6, "new question"),
		assistant(t, 7, "new answer", nil),
	}

	msgs, err := LLMContext(events, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3 (summary + verbatim tail): %+v", len(msgs), msgs)
	}
	if !strings.HasPrefix(msgs[0].Content, summaryPrefix) || !strings.Contains(msgs[0].Content, "two old questions") {
		t.Fatalf("summary message = %+v", msgs[0])
	}
	if msgs[1].Content != "new question" || msgs[2].Content != "new answer" {
		t.Fatalf("post-compaction tail not verbatim: %+v", msgs[1:])
	}
	if strings.Contains(fmt.Sprint(msgs), "old answer one") {
		t.Fatal("replaced content leaked into projection")
	}
}

func TestLLMContextPendingStateSplice(t *testing.T) {
	t.Parallel()
	base := []Event{
		user(t, 1, "tell me a story"),
		ev(t, 2, KindPendingState, PendingState{Partial: "Once upon a"}),
	}

	msgs, err := LLMContext(base, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" || !strings.HasPrefix(last.Content, "Once upon a") || !strings.Contains(last.Content, "interrupted") {
		t.Fatalf("trailing pending state not spliced: %+v", last)
	}

	// A superseded pending state (later events exist) must vanish.
	superseded := append(base, assistant(t, 3, "Once upon a time, the end.", nil))
	msgs, err = LLMContext(superseded, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "interrupted") {
			t.Fatalf("superseded pending state leaked: %+v", msgs)
		}
	}
}

func TestLLMContextIgnoresToolExecutions(t *testing.T) {
	t.Parallel()
	events := []Event{
		user(t, 1, "what time is it"),
		ev(t, 2, KindToolExecution, ToolExecution{CallID: "c1", Name: "time", Status: "ok", ResultDigest: "12:00"}),
		assistant(t, 3, "it is noon", nil),
	}

	msgs, err := LLMContext(events, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("tool execution leaked into LLM context: %+v", msgs)
	}
}

func TestUITranscript(t *testing.T) {
	t.Parallel()
	events := []Event{
		ev(t, 1, KindSessionStarted, SessionStarted{Title: "t"}),
		user(t, 2, "hello"),
		ev(t, 3, KindToolExecution, ToolExecution{CallID: "c1", Name: "calc", Status: "ok"}),
		assistant(t, 4, "hi", nil),
		ev(t, 5, KindCompactionApplied, CompactionApplied{Summary: "s", ReplacesThroughSeq: 4}),
		user(t, 6, "again"),
		ev(t, 7, KindPendingState, PendingState{Partial: "par"}),
	}

	items, err := UITranscript(events)
	if err != nil {
		t.Fatalf("UITranscript: %v", err)
	}
	kinds := make([]string, len(items))
	for i, it := range items {
		kinds[i] = it.Kind
	}
	want := []string{"user", "tool", "assistant", "compaction", "user", "interrupted"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	// The UI replay hides NOTHING: compacted-away events still render.
	if items[0].Text != "hello" {
		t.Fatalf("compacted user message missing from transcript: %+v", items[0])
	}
	if items[2].Provider != "prov" || len(items[2].Blocks) != 1 {
		t.Fatalf("assistant item = %+v", items[2])
	}
}

// TestLLMContextPrefixStability pins the D-018 contract: growing a log
// without a compaction never rewrites earlier projected messages.
func TestLLMContextPrefixStability(t *testing.T) {
	t.Parallel()
	var events []Event
	seq := int64(0)
	next := func() int64 { seq++; return seq }

	events = append(events, ev(t, next(), KindSessionStarted, SessionStarted{}))
	for i := 0; i < 30; i++ {
		switch i % 4 {
		case 0:
			events = append(events, user(t, next(), fmt.Sprintf("question %d", i)))
		case 1:
			events = append(events, assistant(t, next(), fmt.Sprintf("answer %d", i), nil))
		case 2:
			events = append(events, ev(t, next(), KindToolExecution, ToolExecution{CallID: "c", Name: "n", Status: "ok"}))
		case 3:
			events = append(events, assistant(t, next(), fmt.Sprintf("more %d", i), &TurnMemory{KeyFindings: []string{"f"}}))
		}
	}

	var prev []provider.Message
	for n := 1; n <= len(events); n++ {
		cur, err := LLMContext(events[:n], 0)
		if err != nil {
			t.Fatalf("LLMContext(%d): %v", n, err)
		}
		// Ignore a trailing pending splice (none generated here) —
		// every earlier message must match the previous projection.
		if len(cur) < len(prev) {
			t.Fatalf("projection shrank at %d: %d -> %d", n, len(prev), len(cur))
		}
		for i := range prev {
			if cur[i] != prev[i] {
				t.Fatalf("prefix rewritten at event %d, message %d:\n prev %+v\n cur  %+v", n, i, prev[i], cur[i])
			}
		}
		prev = cur
	}
}
