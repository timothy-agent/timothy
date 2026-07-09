//go:build live

package provider

import (
	"os"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// Live tests stream one real completion per configured provider:
//
//	make test-live   (skips any provider whose env vars are absent)
//
// They assert the normalized contract — at least one chunk, one usage
// event, terminal done — not response content.

func assertLiveStream(t *testing.T, p Provider, model string) {
	t.Helper()
	ch, err := p.Stream(t.Context(), CompletionRequest{
		Model:     model,
		Messages:  []Message{{Role: "user", Content: "Reply with the single word: ping"}},
		MaxTokens: 32,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if got := textOf(events, stream.EventChunk); got == "" {
		t.Fatalf("no chunk text; events: %+v", events)
	}
	if len(eventsOfType(events, stream.EventUsage)) != 1 {
		t.Fatalf("want one usage event; events: %+v", events)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last event = %v, want done", lastType(t, events))
	}
}

func TestLiveAnthropic(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	model := os.Getenv("ANTHROPIC_TEST_MODEL")
	if key == "" || model == "" {
		t.Skip("ANTHROPIC_API_KEY / ANTHROPIC_TEST_MODEL not set")
	}
	p := NewAnthropic(AnthropicConfig{Name: "anthropic-live", APIKey: key, Timeout: time.Minute})
	assertLiveStream(t, p, model)
}

func TestLiveOpenAICompat(t *testing.T) {
	base := os.Getenv("OPENAICOMPAT_TEST_BASE_URL")
	key := os.Getenv("OPENAICOMPAT_TEST_API_KEY")
	model := os.Getenv("OPENAICOMPAT_TEST_MODEL")
	if base == "" || key == "" || model == "" {
		t.Skip("OPENAICOMPAT_TEST_BASE_URL / _API_KEY / _MODEL not set")
	}
	p := NewOpenAICompat(OpenAICompatConfig{Name: "oai-live", BaseURL: base, APIKey: key, Timeout: time.Minute})
	assertLiveStream(t, p, model)
}
