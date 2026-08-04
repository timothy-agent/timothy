package session

import (
	"reflect"
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
		if !reflect.DeepEqual(msgs[i], want[i]) {
			t.Fatalf("msg[%d] = %+v, want %+v", i, msgs[i], want[i])
		}
	}
}

// TestLLMContextCarriesImageRefsNotBytes confirms a user_message event
// with attached images projects into ImageRefs (id+mime only) on the
// provider.Message — never into Content, and never into Images (which
// only chat.runTurn fills, from the store, at request-build time).
// Projection has no store dependency; this pins that it stays that way.
func TestLLMContextCarriesImageRefsNotBytes(t *testing.T) {
	t.Parallel()
	events := []Event{
		ev(t, 1, KindSessionStarted, SessionStarted{}),
		ev(t, 2, KindUserMessage, UserMessage{
			Text:   "what is this?",
			Images: []ImageRef{{ID: "abc123", Mime: "image/png"}},
		}),
	}

	msgs, err := LLMContext(events, 100_000)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v, want 1", msgs)
	}
	m := msgs[0]
	if m.Content != "what is this?" {
		t.Fatalf("Content = %q, want the text only (no image data)", m.Content)
	}
	if len(m.Images) != 0 {
		t.Fatalf("Images = %+v, want empty (projection never resolves bytes)", m.Images)
	}
	if len(m.ImageRefs) != 1 || m.ImageRefs[0].ID != "abc123" || m.ImageRefs[0].Mime != "image/png" {
		t.Fatalf("ImageRefs = %+v, want one ref to abc123/image/png", m.ImageRefs)
	}
}

// TestLLMContextRendersDocumentMarkdown confirms a user_message's
// DocumentRef renders its already-converted markdown straight into the
// LLM content (no store round-trip, no sidecar call at projection
// time) — the persisted markdown is the sole source, per D-045/D-046's
// send-time-conversion design.
func TestLLMContextRendersDocumentMarkdown(t *testing.T) {
	t.Parallel()
	events := []Event{
		ev(t, 1, KindSessionStarted, SessionStarted{}),
		ev(t, 2, KindUserMessage, UserMessage{
			Text:      "summarize this",
			Documents: []DocumentRef{{ID: "doc1", Mime: "application/pdf", Markdown: "# Title\n\nbody text"}},
		}),
	}

	msgs, err := LLMContext(events, 100_000)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v, want 1", msgs)
	}
	m := msgs[0]
	if !strings.Contains(m.Content, "summarize this") || !strings.Contains(m.Content, "# Title") ||
		!strings.Contains(m.Content, "body text") || !strings.Contains(m.Content, "doc1") {
		t.Fatalf("Content = %q, want text + rendered document markdown", m.Content)
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

	// A user message after the pending does NOT supersede it: the
	// follow-up question must still see the partial, in order.
	withQuestion := append(append([]Event(nil), base...), user(t, 3, "you were cut off — continue"))
	msgs, err = LLMContext(withQuestion, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	if len(msgs) != 3 || !strings.Contains(msgs[1].Content, "Once upon a") || msgs[2].Content != "you were cut off — continue" {
		t.Fatalf("pending not spliced before follow-up question: %+v", msgs)
	}

	// Newer checkpoints supersede older ones: only the last partial
	// appears.
	checkpoints := append(append([]Event(nil), base...),
		ev(t, 3, KindPendingState, PendingState{Partial: "Once upon a time, in a"}))
	msgs, err = LLMContext(checkpoints, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	count := 0
	for _, m := range msgs {
		if strings.Contains(m.Content, "Once upon a") {
			count++
			if !strings.Contains(m.Content, "in a") {
				t.Fatalf("older checkpoint won: %+v", m)
			}
		}
	}
	if count != 1 {
		t.Fatalf("checkpoint messages = %d, want 1: %+v", count, msgs)
	}

	// An assistant turn supersedes every pending.
	superseded := append(append([]Event(nil), base...), assistant(t, 3, "Once upon a time, the end.", nil))
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

// TestLLMContextRendersTurnFailed pins D-044: a persisted turn_failed
// event rides the LLM context as a bracketed user-role note (same
// register as compaction/turn-memory asides), never as an assistant
// message the model might imitate.
func TestLLMContextRendersTurnFailed(t *testing.T) {
	t.Parallel()
	events := []Event{
		user(t, 1, "do the thing"),
		ev(t, 2, KindTurnFailed, TurnFailed{Code: "chain_exhausted", Message: "every provider failed"}),
		user(t, 3, "try again"),
	}

	msgs, err := LLMContext(events, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("messages = %+v, want 3 (user, failed note, user)", msgs)
	}
	if msgs[1].Role != "user" || !strings.Contains(msgs[1].Content, "every provider failed") {
		t.Fatalf("turn_failed note = %+v", msgs[1])
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

// TestUITranscriptCarriesImages confirms a user_message's image refs
// surface on TranscriptItem.Images for UI replay (additive field; the
// web PR renders thumbnails from it later).
func TestUITranscriptCarriesImages(t *testing.T) {
	t.Parallel()
	events := []Event{
		ev(t, 1, KindSessionStarted, SessionStarted{}),
		ev(t, 2, KindUserMessage, UserMessage{
			Text:   "look",
			Images: []ImageRef{{ID: "img1", Mime: "image/jpeg"}},
		}),
	}

	items, err := UITranscript(events)
	if err != nil {
		t.Fatalf("UITranscript: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want 1", items)
	}
	if len(items[0].Images) != 1 || items[0].Images[0].ID != "img1" || items[0].Images[0].Mime != "image/jpeg" {
		t.Fatalf("Images = %+v, want one ref to img1/image/jpeg", items[0].Images)
	}
}

// TestUITranscriptExposesDocumentsWithoutMarkdown confirms a
// user_message's document refs surface on TranscriptItem.Documents as
// id+mime only — the converted markdown never rides the UI payload,
// which can be arbitrarily large.
func TestUITranscriptExposesDocumentsWithoutMarkdown(t *testing.T) {
	t.Parallel()
	events := []Event{
		ev(t, 1, KindSessionStarted, SessionStarted{}),
		ev(t, 2, KindUserMessage, UserMessage{
			Text:      "look",
			Documents: []DocumentRef{{ID: "doc1", Mime: "application/pdf", Markdown: strings.Repeat("x", 10_000)}},
		}),
	}

	items, err := UITranscript(events)
	if err != nil {
		t.Fatalf("UITranscript: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want 1", items)
	}
	if len(items[0].Documents) != 1 || items[0].Documents[0].ID != "doc1" || items[0].Documents[0].Mime != "application/pdf" {
		t.Fatalf("Documents = %+v, want one ref to doc1/application/pdf", items[0].Documents)
	}
	data, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal item: %v", err)
	}
	if strings.Contains(string(data), "xxxx") {
		t.Fatal("markdown leaked into the UI transcript payload")
	}
}

// TestUITranscriptRendersTurnFailed pins D-044's UI side: a persisted
// turn_failed event surfaces as its own "error" replay item carrying
// the human-readable message — the whole point is that a failed turn
// is visible on reload, not silently dropped like session_started.
func TestUITranscriptRendersTurnFailed(t *testing.T) {
	t.Parallel()
	events := []Event{
		user(t, 1, "do the thing"),
		ev(t, 2, KindTurnFailed, TurnFailed{Code: "empty_response", Message: "the turn completed with no text, reasoning, or tool calls"}),
	}

	items, err := UITranscript(events)
	if err != nil {
		t.Fatalf("UITranscript: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want 2", items)
	}
	if items[1].Kind != "error" || items[1].Text != "the turn completed with no text, reasoning, or tool calls" {
		t.Fatalf("turn_failed item = %+v", items[1])
	}
}

// TestUITranscriptShortensChainExhausted confirms chain_exhausted
// always renders as a short, code-based message — even for rows
// persisted before the codes-only summary shipped, whose stored
// message carries raw per-provider wire error text with no use to a
// user reading history.
func TestUITranscriptShortensChainExhausted(t *testing.T) {
	t.Parallel()
	events := []Event{
		user(t, 1, "do the thing"),
		ev(t, 2, KindTurnFailed, TurnFailed{
			Code:    "chain_exhausted",
			Message: `every provider attempt failed: AWS Bedrock/nova-pro: operation error Bedrock Runtime: ConverseStream, context canceled; GLM (Z.ai)/glm-5.2: terminated signal received`,
		}),
	}

	items, err := UITranscript(events)
	if err != nil {
		t.Fatalf("UITranscript: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want 2", items)
	}
	if items[1].Kind != "error" || items[1].Text != "all providers failed (chain_exhausted)" {
		t.Fatalf("chain_exhausted item = %+v, want short form", items[1])
	}
}

// TestUITranscriptExposesDuration confirms UITranscript carries an
// assistant_turn's DurationMs through to the replay item — replayed
// sessions must show the same turn-stats duration the live SSE meta
// event carried.
func TestUITranscriptExposesDuration(t *testing.T) {
	t.Parallel()
	var a AssistantTurn
	a.LLM.Message = "hi"
	a.UI.Blocks = []UIBlock{{Type: "text", Text: "hi"}}
	a.DurationMs = 81234
	events := []Event{ev(t, 1, KindAssistantTurn, a)}

	items, err := UITranscript(events)
	if err != nil {
		t.Fatalf("UITranscript: %v", err)
	}
	if len(items) != 1 || items[0].DurationMs != 81234 {
		t.Fatalf("items = %+v, want duration_ms 81234", items)
	}
}

// TestLLMContextPrefixStability pins the D-018 contract: growing a log
// without a compaction never rewrites earlier projected messages.
// Pending checkpoints are deliberately absent from the generator: an
// interrupted turn's spliced partial is replaced when the turn
// completes — the one sanctioned prefix rewrite (D-023).
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
			if !reflect.DeepEqual(cur[i], prev[i]) {
				t.Fatalf("prefix rewritten at event %d, message %d:\n prev %+v\n cur  %+v", n, i, prev[i], cur[i])
			}
		}
		prev = cur
	}
}

// TestPendingSurvivesNonCoveringCompaction pins the kill-test artifact
// against pre-send compaction: a compaction that did NOT consume the
// live pending (replaces_through_seq below it) must leave the partial
// in both projections; one that covered it supersedes it.
func TestPendingSurvivesNonCoveringCompaction(t *testing.T) {
	t.Parallel()
	base := []Event{
		user(t, 1, "old question"),
		assistant(t, 2, "old answer", nil),
		user(t, 3, "tell me a story"),
		ev(t, 4, KindPendingState, PendingState{Partial: "Once upon a"}),
		// Summarized only the first exchange — the partial is NOT covered.
		ev(t, 5, KindCompactionApplied, CompactionApplied{Summary: "old exchange summarized", ReplacesThroughSeq: 2}),
	}

	msgs, err := LLMContext(base, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	if !strings.Contains(fmt.Sprint(msgs), "Once upon a") {
		t.Fatalf("uncovered partial vanished from LLM context: %+v", msgs)
	}
	items, err := UITranscript(base)
	if err != nil {
		t.Fatalf("UITranscript: %v", err)
	}
	found := false
	for _, it := range items {
		if it.Kind == "interrupted" && strings.Contains(it.Text, "Once upon a") {
			found = true
		}
	}
	if !found {
		t.Fatalf("uncovered partial vanished from UI transcript: %+v", items)
	}

	// A compaction that consumed the pending DOES supersede it.
	covered := append(append([]Event(nil), base[:4]...),
		ev(t, 5, KindCompactionApplied, CompactionApplied{Summary: "everything incl. the partial", ReplacesThroughSeq: 4}))
	msgs, err = LLMContext(covered, 0)
	if err != nil {
		t.Fatalf("LLMContext covered: %v", err)
	}
	if strings.Contains(fmt.Sprint(msgs), "Once upon a") {
		t.Fatalf("consumed partial leaked past its compaction: %+v", msgs)
	}
}

// TestTurnMemoryEventProjectsAsOwnMessage pins the late-residue
// contract: a turn_memory event appends a NEW message at its own
// position — the assistant turn it describes is never rewritten, so
// the projected prefix stays byte-stable when residue lands late.
func TestTurnMemoryEventProjectsAsOwnMessage(t *testing.T) {
	t.Parallel()
	events := []Event{
		user(t, 1, "fix the bug"),
		assistant(t, 2, "done", nil),
	}
	before, err := LLMContext(events, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}

	withMemory := append(append([]Event(nil), events...),
		ev(t, 3, KindTurnMemory, TurnMemoryEvent{TurnSeq: 2, TurnMemory: TurnMemory{
			FilesChanged: []string{"main.go"}, KeyFindings: []string{"nil map"},
		}}))
	after, err := LLMContext(withMemory, 0)
	if err != nil {
		t.Fatalf("LLMContext with memory: %v", err)
	}

	if len(after) != len(before)+1 {
		t.Fatalf("messages = %d, want %d (residue as its own message)", len(after), len(before)+1)
	}
	for i := range before {
		if !reflect.DeepEqual(after[i], before[i]) {
			t.Fatalf("late residue rewrote the prefix at %d:\n before %+v\n after  %+v", i, before[i], after[i])
		}
	}
	tail := after[len(after)-1]
	if tail.Role != "user" || !strings.Contains(tail.Content, "[turn memory]") ||
		!strings.Contains(tail.Content, "files changed: main.go") {
		t.Fatalf("residue message = %+v", tail)
	}

	// The UI replay does not render residue.
	items, err := UITranscript(withMemory)
	if err != nil {
		t.Fatalf("UITranscript: %v", err)
	}
	for _, it := range items {
		if strings.Contains(it.Text, "turn memory") {
			t.Fatalf("residue leaked into UI transcript: %+v", it)
		}
	}
}
