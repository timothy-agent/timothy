package chat

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type scriptedGateway struct {
	requests []gwclient.StreamRequest
	reply    string
}

func (g *scriptedGateway) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	g.requests = append(g.requests, req)
	ch := make(chan stream.StreamEvent, 3)
	ch <- stream.StreamEvent{Type: stream.EventChunk, Text: g.reply}
	ch <- stream.StreamEvent{Type: stream.EventDone}
	close(ch)
	return ch, nil
}

func drain(t *testing.T, ch <-chan stream.StreamEvent) {
	t.Helper()
	for range ch {
	}
}

func newTestService(t *testing.T, gw Gateway) *Service {
	t.Helper()
	return New(gw, pgpool.New(t.Context(), "", discard()), discard())
}

func TestChatBuffersHistoryAcrossTurns(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{reply: "first answer"}
	s := newTestService(t, gw)

	id, ch, err := s.Chat(t.Context(), Request{SessionID: "sess", Message: "question one"})
	if err != nil {
		t.Fatalf("Chat 1: %v", err)
	}
	if id != "sess" {
		t.Fatalf("session id = %q", id)
	}
	drain(t, ch)

	gw.reply = "second answer"
	_, ch, err = s.Chat(t.Context(), Request{SessionID: "sess", Message: "question two"})
	if err != nil {
		t.Fatalf("Chat 2: %v", err)
	}
	drain(t, ch)

	second := gw.requests[1]
	if len(second.Messages) != 3 {
		t.Fatalf("second request messages = %d, want 3 (q1, a1, q2): %+v", len(second.Messages), second.Messages)
	}
	if second.Messages[0].Content != "question one" ||
		second.Messages[1].Role != "assistant" || second.Messages[1].Content != "first answer" ||
		second.Messages[2].Content != "question two" {
		t.Fatalf("history wrong: %+v", second.Messages)
	}
}

func TestChatSessionsAreIsolated(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{reply: "a"}
	s := newTestService(t, gw)

	_, ch, _ := s.Chat(t.Context(), Request{SessionID: "one", Message: "in session one"})
	drain(t, ch)
	_, ch, _ = s.Chat(t.Context(), Request{SessionID: "two", Message: "in session two"})
	drain(t, ch)

	if len(gw.requests[1].Messages) != 1 {
		t.Fatalf("session two saw session one's history: %+v", gw.requests[1].Messages)
	}
}

func TestChatBufferIsBounded(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{reply: "r"}
	s := newTestService(t, gw)

	for range maxBufferedMessages { // each turn adds 2 buffered messages
		_, ch, err := s.Chat(t.Context(), Request{SessionID: "sess", Message: "m"})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		drain(t, ch)
	}

	last := gw.requests[len(gw.requests)-1]
	if got := len(last.Messages); got > maxBufferedMessages+1 {
		t.Fatalf("request messages = %d, exceeds bound %d", got, maxBufferedMessages+1)
	}
}

func TestChatDefaultsCategory(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{reply: "r"}
	s := newTestService(t, gw)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	if gw.requests[0].TaskCategory != defaultCategory {
		t.Fatalf("category = %q, want %q", gw.requests[0].TaskCategory, defaultCategory)
	}
}

func TestChatNewSessionRequiresDatabase(t *testing.T) {
	t.Parallel()
	s := newTestService(t, &scriptedGateway{reply: "r"}) // degraded pool

	if _, _, err := s.Chat(t.Context(), Request{Message: "hi"}); err == nil {
		t.Fatal("Chat without session id and without database: want error")
	}
}
