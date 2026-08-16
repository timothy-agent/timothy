package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func oaiWrite(w http.ResponseWriter, data string) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()
}

func oaiServer(t *testing.T, handler http.HandlerFunc) *OpenAICompat {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewOpenAICompat(OpenAICompatConfig{
		Name: "oai-test", BaseURL: srv.URL, APIKey: "test-key",
		Timeout: 10 * time.Second,
	})
}

func TestOpenAICompatHappyPath(t *testing.T) {
	t.Parallel()
	var gotAuth string
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		oaiWrite(w, `{"choices":[{"delta":{"content":"Hel"}}]}`)
		oaiWrite(w, `{"choices":[{"delta":{"content":"lo"}}]}`)
		oaiWrite(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		oaiWrite(w, `{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":6}}}`)
		oaiWrite(w, "[DONE]")
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "m", System: "sys", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if got := textOf(events, stream.EventChunk); got != "Hello" {
		t.Fatalf("chunks = %q, want Hello", got)
	}
	usages := eventsOfType(events, stream.EventUsage)
	if len(usages) != 1 {
		t.Fatalf("usage events = %d, want 1", len(usages))
	}
	u := usages[0].Usage
	// prompt_tokens=12 includes cached_tokens=6: normalized input is 6.
	if u.InputTokens != 6 || u.OutputTokens != 4 || u.CacheReadTokens != 6 {
		t.Fatalf("usage = %+v", u)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last = %v, want done", lastType(t, events))
	}
}

func TestOpenAICompatReasoning(t *testing.T) {
	t.Parallel()
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		oaiWrite(w, `{"choices":[{"delta":{"reasoning_content":"think"}}]}`)
		oaiWrite(w, `{"choices":[{"delta":{"content":"answer"}}]}`)
		oaiWrite(w, "[DONE]")
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	if got := textOf(events, stream.EventReasoningChunk); got != "think" {
		t.Fatalf("reasoning = %q", got)
	}
	if got := textOf(events, stream.EventChunk); got != "answer" {
		t.Fatalf("chunks = %q", got)
	}
}

func TestOpenAICompatToolCalls(t *testing.T) {
	t.Parallel()
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		oaiWrite(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":"{\"city"}}]}}]}`)
		oaiWrite(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\":\"SF\"}"}}]}}]}`)
		oaiWrite(w, `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
		oaiWrite(w, "[DONE]")
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	starts := eventsOfType(events, stream.EventToolStart)
	ends := eventsOfType(events, stream.EventToolEnd)
	if len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("tool events %d/%d, want 1/1: %+v", len(starts), len(ends), events)
	}
	if starts[0].ToolCall.ID != "call_1" || starts[0].ToolCall.Name != "get_weather" {
		t.Fatalf("tool_start = %+v", starts[0].ToolCall)
	}
	if got := string(ends[0].ToolCall.Input); got != `{"city":"SF"}` {
		t.Fatalf("tool input = %q", got)
	}
}

func TestOpenAICompatTruncationIncomplete(t *testing.T) {
	t.Parallel()
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		oaiWrite(w, `{"choices":[{"delta":{"content":"cut"}}]}`)
		oaiWrite(w, `{"choices":[{"delta":{},"finish_reason":"length"}]}`)
		oaiWrite(w, "[DONE]")
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	if len(eventsOfType(events, stream.EventIncomplete)) != 1 {
		t.Fatalf("want incomplete on finish_reason=length: %+v", events)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last = %v, want done", lastType(t, events))
	}
}

func TestOpenAICompat500ThenSuccess(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		oaiWrite(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
		oaiWrite(w, "[DONE]")
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	if len(eventsOfType(events, stream.EventRetry)) != 1 {
		t.Fatalf("want one retry event: %+v", events)
	}
	if got := textOf(events, stream.EventChunk); got != "ok" {
		t.Fatalf("chunks = %q", got)
	}
}

func TestOpenAICompatMidStreamDisconnect(t *testing.T) {
	t.Parallel()
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		oaiWrite(w, `{"choices":[{"delta":{"content":"par"}}]}`)
		// no [DONE]: connection closes mid-stream
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	if len(eventsOfType(events, stream.EventIncomplete)) != 1 {
		t.Fatalf("want one incomplete event: %+v", events)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last = %v, want done", lastType(t, events))
	}
}

func TestOpenAICompatAPIError(t *testing.T) {
	t.Parallel()
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		oaiWrite(w, `{"error":{"message":"model overloaded","type":"server_error"}}`)
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	errs := eventsOfType(events, stream.EventError)
	if len(errs) != 1 || errs[0].Err.Message != "model overloaded" {
		t.Fatalf("want api error event: %+v", events)
	}
}

func TestOpenAICompatEmbedRetriesTransientFailure(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[{"index":0,"embedding":[0.5]},{"index":1,"embedding":[0.6]}],"usage":{"prompt_tokens":3}}`)
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAICompat(OpenAICompatConfig{Name: "e", BaseURL: srv.URL, APIKey: "k", Timeout: 10 * time.Second})

	vecs, usage, err := p.Embed(t.Context(), "m", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (retry after 503)", calls.Load())
	}
	if len(vecs) != 2 || vecs[0][0] != 0.5 || usage.InputTokens != 3 {
		t.Fatalf("vecs = %v usage = %+v", vecs, usage)
	}
}

func TestOpenAICompatEmbedRejectsBadIndices(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, payload, wantErr string
	}{
		{
			name:    "duplicate index",
			payload: `{"data":[{"index":0,"embedding":[0.1]},{"index":0,"embedding":[0.2]}],"usage":{}}`,
			wantErr: "duplicate embedding index",
		},
		{
			name:    "out of range index",
			payload: `{"data":[{"index":0,"embedding":[0.1]},{"index":5,"embedding":[0.2]}],"usage":{}}`,
			wantErr: "out of range",
		},
		{
			name:    "count mismatch",
			payload: `{"data":[{"index":0,"embedding":[0.1]}],"usage":{}}`,
			wantErr: "got 1 embeddings for 2 texts",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, tt.payload)
			}))
			t.Cleanup(srv.Close)
			p := NewOpenAICompat(OpenAICompatConfig{Name: "e", BaseURL: srv.URL, APIKey: "k", Timeout: time.Second})

			_, _, err := p.Embed(t.Context(), "m", []string{"a", "b"})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Embed err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestOpenAICompatMalformed(t *testing.T) {
	t.Parallel()
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		oaiWrite(w, `{broken`)
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	events := collect(t, ch)

	errs := eventsOfType(events, stream.EventError)
	if len(errs) != 1 || errs[0].Err.Code != "malformed_stream" {
		t.Fatalf("want malformed_stream error: %+v", events)
	}
}

func readReasoningEffort(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var wire oaiRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	return wire.ReasoningEffort
}

func TestOpenAICompat400ReasoningEffortStripAndRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var efforts []string
	var mu sync.Mutex
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		efforts = append(efforts, readReasoningEffort(t, r))
		mu.Unlock()
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		oaiWrite(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
		oaiWrite(w, "[DONE]")
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "m", Effort: "low"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (retry after 400)", calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(efforts) != 2 || efforts[0] != "low" || efforts[1] != "" {
		t.Fatalf("efforts across attempts = %v, want [low, \"\"]", efforts)
	}
	if got := textOf(events, stream.EventChunk); got != "ok" {
		t.Fatalf("chunks = %q, want ok", got)
	}
	if len(eventsOfType(events, stream.EventError)) != 0 {
		t.Fatalf("want no error event surfaced after successful retry: %+v", events)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last = %v, want done", lastType(t, events))
	}
}

func TestOpenAICompat400ConfigReasoningEffortStripAndRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var efforts []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		efforts = append(efforts, readReasoningEffort(t, r))
		mu.Unlock()
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		oaiWrite(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
		oaiWrite(w, "[DONE]")
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAICompat(OpenAICompatConfig{
		Name: "oai-test", BaseURL: srv.URL, APIKey: "test-key",
		Timeout: 10 * time.Second, ReasoningEffort: "none",
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (retry after 400)", calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(efforts) != 2 || efforts[0] != "none" || efforts[1] != "" {
		t.Fatalf("efforts across attempts = %v, want [none, \"\"]", efforts)
	}
	if got := textOf(events, stream.EventChunk); got != "ok" {
		t.Fatalf("chunks = %q, want ok", got)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last = %v, want done", lastType(t, events))
	}
}

func TestOpenAICompat400TwiceSurfacesError(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "m", Effort: "low"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (one retry, then surface)", calls.Load())
	}
	errs := eventsOfType(events, stream.EventError)
	if len(errs) != 1 || errs[0].Err.Code != "http_400" {
		t.Fatalf("want one http_400 error event: %+v", events)
	}
}

func TestOpenAICompat400MaxTokensSwapAndRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	type caps struct{ maxTokens, maxCompletion int }
	var got []caps
	var efforts []string
	var mu sync.Mutex
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MaxTokens           int    `json:"max_tokens"`
			MaxCompletionTokens int    `json:"max_completion_tokens"`
			ReasoningEffort     string `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		got = append(got, caps{req.MaxTokens, req.MaxCompletionTokens})
		efforts = append(efforts, req.ReasoningEffort)
		mu.Unlock()
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":"max_tokens","code":"unsupported_parameter"}}`))
			return
		}
		oaiWrite(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
		oaiWrite(w, "[DONE]")
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "m", MaxTokens: 512, Effort: "low"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (retry after max_tokens 400)", calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	want := []caps{{512, 0}, {0, 512}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("token caps across attempts = %v, want %v", got, want)
	}
	if len(efforts) != 2 || efforts[0] != "low" || efforts[1] != "low" {
		t.Fatalf("efforts across attempts = %v, want reasoning_effort preserved on swap retry", efforts)
	}
	if got := textOf(events, stream.EventChunk); got != "ok" {
		t.Fatalf("chunks = %q, want ok", got)
	}
	if len(eventsOfType(events, stream.EventError)) != 0 {
		t.Fatalf("want no error event surfaced after successful retry: %+v", events)
	}
}

func TestOpenAICompat400WithoutReasoningEffortNoRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (no retry without reasoning_effort)", calls.Load())
	}
	errs := eventsOfType(events, stream.EventError)
	if len(errs) != 1 || errs[0].Err.Code != "http_400" {
		t.Fatalf("want one http_400 error event: %+v", events)
	}
}

func TestOpenAICompatNoEffortNeverSendsReasoningEffort(t *testing.T) {
	t.Parallel()
	var gotEffort string
	p := oaiServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotEffort = readReasoningEffort(t, r)
		oaiWrite(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
		oaiWrite(w, "[DONE]")
	})

	ch, _ := p.Stream(t.Context(), CompletionRequest{Model: "m"})
	collect(t, ch)

	if gotEffort != "" {
		t.Fatalf("reasoning_effort = %q, want empty when Effort unset", gotEffort)
	}
}
