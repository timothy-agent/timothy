package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// sseWrite writes one SSE event and flushes.
func sseWrite(w http.ResponseWriter, event, data string) {
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	w.(http.Flusher).Flush()
}

func anthropicServer(t *testing.T, handler http.HandlerFunc) *Anthropic {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewAnthropic(AnthropicConfig{
		Name: "anthropic-test", BaseURL: srv.URL, APIKey: "test-key",
		Timeout: 10 * time.Second,
	})
}

func TestAnthropicHappyPath(t *testing.T) {
	t.Parallel()
	var gotKey, gotVersion string
	p := anthropicServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		gotVersion = r.Header.Get("Anthropic-Version")
		w.Header().Set("Content-Type", "text/event-stream")
		sseWrite(w, "message_start", `{"type":"message_start","message":{"usage":{"input_tokens":25,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`)
		sseWrite(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`)
		sseWrite(w, "ping", `{"type":"ping"}`)
		sseWrite(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
		sseWrite(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}`)
		sseWrite(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		sseWrite(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}`)
		sseWrite(w, "message_stop", `{"type":"message_stop"}`)
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if gotKey != "test-key" || gotVersion != anthropicVersion {
		t.Fatalf("headers: key=%q version=%q", gotKey, gotVersion)
	}
	if got := textOf(events, stream.EventChunk); got != "Hello!" {
		t.Fatalf("chunks = %q, want Hello!", got)
	}
	usages := eventsOfType(events, stream.EventUsage)
	if len(usages) != 1 {
		t.Fatalf("usage events = %d, want 1", len(usages))
	}
	u := usages[0].Usage
	if u.InputTokens != 25 || u.OutputTokens != 15 || u.CacheReadTokens != 10 || u.CacheWriteTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last event = %v, want done", lastType(t, events))
	}
}

func TestAnthropicToolUse(t *testing.T) {
	t.Parallel()
	p := anthropicServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w, "message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		sseWrite(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"get_weather"}}`)
		sseWrite(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"location\": \"San"}}`)
		sseWrite(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" Francisco\"}"}}`)
		sseWrite(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		sseWrite(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`)
		sseWrite(w, "message_stop", `{"type":"message_stop"}`)
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "m", Messages: []Message{{Role: "user", Content: "weather?"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	starts := eventsOfType(events, stream.EventToolStart)
	ends := eventsOfType(events, stream.EventToolEnd)
	if len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("tool events = %d starts, %d ends; want 1/1", len(starts), len(ends))
	}
	if starts[0].ToolCall.ID != "tu_1" || starts[0].ToolCall.Name != "get_weather" {
		t.Fatalf("tool_start = %+v", starts[0].ToolCall)
	}
	if got := string(ends[0].ToolCall.Input); got != `{"location": "San Francisco"}` {
		t.Fatalf("tool input = %q", got)
	}
}

func TestAnthropicThinkingDelta(t *testing.T) {
	t.Parallel()
	p := anthropicServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w, "message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		sseWrite(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`)
		sseWrite(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`)
		sseWrite(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`)
		sseWrite(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		sseWrite(w, "message_stop", `{"type":"message_stop"}`)
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	if got := textOf(events, stream.EventReasoningChunk); got != "hmm" {
		t.Fatalf("reasoning = %q, want hmm", got)
	}
}

func TestAnthropicMidStreamDisconnect(t *testing.T) {
	t.Parallel()
	p := anthropicServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w, "message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		sseWrite(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"par"}}`)
		// handler returns without message_stop: connection closes
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	if len(eventsOfType(events, stream.EventIncomplete)) != 1 {
		t.Fatalf("want one incomplete event, got %+v", events)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last = %v, want done after incomplete", lastType(t, events))
	}
}

func TestAnthropic429ThenSuccess(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	p := anthropicServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		sseWrite(w, "message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		sseWrite(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		sseWrite(w, "message_stop", `{"type":"message_stop"}`)
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	retries := eventsOfType(events, stream.EventRetry)
	if len(retries) != 1 || retries[0].Retry.Attempt != 1 {
		t.Fatalf("retry events = %+v, want one attempt=1", retries)
	}
	if got := textOf(events, stream.EventChunk); got != "ok" {
		t.Fatalf("chunks = %q, want ok", got)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last = %v, want done", lastType(t, events))
	}
}

func TestAnthropicPermanent401NoRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	p := anthropicServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want 1 (no retry on 401)", calls.Load())
	}
	errs := eventsOfType(events, stream.EventError)
	if len(errs) != 1 || errs[0].Err.Retryable {
		t.Fatalf("want one non-retryable error, got %+v", errs)
	}
}

func TestAnthropicTimeout(t *testing.T) {
	t.Parallel()
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	t.Cleanup(func() { close(blocked); srv.Close() })

	p := NewAnthropic(AnthropicConfig{Name: "t", BaseURL: srv.URL, APIKey: "k", Timeout: 200 * time.Millisecond})
	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	errs := eventsOfType(events, stream.EventError)
	if len(errs) != 1 {
		t.Fatalf("want one error event, got %+v", events)
	}
	if errs[0].Err.Code != "timeout" {
		t.Fatalf("error code = %q, want timeout", errs[0].Err.Code)
	}
}

func TestAnthropicCancelMidStream(t *testing.T) {
	t.Parallel()
	p := anthropicServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w, "message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			sseWrite(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`)
			time.Sleep(10 * time.Millisecond)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	ch, _ := p.Stream(ctx, CompletionRequest{Model: "m"})

	<-ch // first event arrived; stream is live
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // closed promptly after cancel
			}
		case <-deadline:
			t.Fatal("channel not closed after context cancel")
		}
	}
}

func TestAnthropicMalformedSSE(t *testing.T) {
	t.Parallel()
	p := anthropicServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w, "message_start", `{not json`)
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	errs := eventsOfType(events, stream.EventError)
	if len(errs) != 1 || errs[0].Err.Code != "malformed_stream" {
		t.Fatalf("want malformed_stream error, got %+v", events)
	}
}

func TestAnthropicMissingModel(t *testing.T) {
	t.Parallel()
	p := NewAnthropic(AnthropicConfig{Name: "t", APIKey: "k"})
	if _, err := p.Stream(t.Context(), CompletionRequest{}); err == nil {
		t.Fatal("Stream() with empty model: want error")
	}
}
