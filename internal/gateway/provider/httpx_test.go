package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBackoffGrowsWithJitter(t *testing.T) {
	t.Parallel()
	for attempt := 1; attempt <= 3; attempt++ {
		base := baseBackoff << (attempt - 1)
		for range 20 {
			got := backoffFor(attempt)
			if got < base || got > base+base/2+time.Millisecond {
				t.Fatalf("attempt %d: backoff %v outside [%v, %v]", attempt, got, base, base+base/2)
			}
		}
	}
}

func TestRetriesFor(t *testing.T) {
	t.Parallel()
	if got := retriesFor(true); got != maxRetries {
		t.Fatalf("final attempt retries = %d, want the full budget %d", got, maxRetries)
	}
	if got := retriesFor(false); got != 1 {
		t.Fatalf("non-final attempt retries = %d, want 1 — the chain is the retry", got)
	}
}

// TestDoWithRetryHonorsBudget pins the failover-latency contract: a
// rate-limited provider mid-chain is retried once (2 requests total),
// not maxRetries times — re-hammering a limiter only delays failover.
func TestDoWithRetryHonorsBudget(t *testing.T) {
	t.Parallel()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	build := func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	}
	if _, err := doWithRetry(context.Background(), srv.Client(), 1, build, nil); err == nil {
		t.Fatal("persistent 429 reported no error")
	}
	if hits != 2 {
		t.Fatalf("provider hit %d times with budget 1, want 2 (initial + one retry)", hits)
	}
}

func TestRetryableStatus(t *testing.T) {
	t.Parallel()
	for code, want := range map[int]bool{
		429: true, 500: true, 502: true, 503: true,
		200: false, 400: false, 401: false, 404: false,
	} {
		if got := retryableStatus(code); got != want {
			t.Fatalf("retryableStatus(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestIsContextLengthMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		msg  string
		want bool
	}{
		{"Prompt exceeds max length of 128000 tokens", true},
		{"Your input exceeds the context window of this model", true},
		{"This model's maximum context length is 8192 tokens", true},
		{"error code: context_length_exceeded", true},
		{"too many tokens in the request", true},
		{"prompt is too long for this model", true},
		{"input is too long", true},
		{"request too large for this model", true},
		{"CONTEXT LENGTH exceeded", true},
		{"invalid api key", false},
		{"rate limit exceeded", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isContextLengthMessage(c.msg); got != c.want {
			t.Errorf("isContextLengthMessage(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func TestErrEventContextLengthCode(t *testing.T) {
	t.Parallel()
	err := &permanentError{status: 400, err: fmt.Errorf("http 400: Your input exceeds the context window of this model")}
	ev := errEvent(err)
	if ev.Err.Code != "context_length" {
		t.Fatalf("code = %q, want context_length", ev.Err.Code)
	}
	if ev.Err.Retryable {
		t.Fatal("context_length error must not be retryable")
	}
}

func TestErrEventHTTPStatusCode(t *testing.T) {
	t.Parallel()
	err := &permanentError{status: 401, err: fmt.Errorf("http 401: invalid api key")}
	ev := errEvent(err)
	if ev.Err.Code != "http_401" {
		t.Fatalf("code = %q, want http_401", ev.Err.Code)
	}
}
