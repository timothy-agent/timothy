package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

const (
	defaultTimeout = 5 * time.Minute
	maxRetries     = 3
	baseBackoff    = 500 * time.Millisecond
)

// retryableStatus reports whether an HTTP status is worth retrying.
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// backoffFor returns the jittered exponential backoff for an attempt
// (1-based): base*2^(n-1) plus up to 50% jitter.
func backoffFor(attempt int) time.Duration {
	d := baseBackoff << (attempt - 1)
	return d + time.Duration(rand.Int64N(int64(d)/2+1)) // #nosec G404 -- jitter, not a secret
}

// retriesFor sizes the in-provider retry budget: the full maxRetries
// when this is the chain's final attempt, one quick retry otherwise —
// failover to the next provider beats backing off against a limiter.
func retriesFor(finalAttempt bool) int {
	if finalAttempt {
		return maxRetries
	}
	return 1
}

// doWithRetry issues the request built by build, retrying 429/5xx
// responses and network errors up to retries times with jittered
// backoff. Each retry is reported through notify (return false to
// abort); pass nil to retry silently. On success the response body is
// open and the caller owns closing it.
func doWithRetry(ctx context.Context, client *http.Client, retries int, build func() (*http.Request, error), notify func(stream.RetryInfo) bool) (*http.Response, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			backoff := backoffFor(attempt)
			if notify != nil && !notify(stream.RetryInfo{
				Attempt:   attempt,
				BackoffMs: backoff.Milliseconds(),
				Reason:    lastErr.Error(),
			}) {
				return nil, ctx.Err()
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if attempt < retries && ctx.Err() == nil {
				continue
			}
			return nil, lastErr
		}
		if retryableStatus(resp.StatusCode) {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			if attempt < retries && ctx.Err() == nil {
				continue
			}
			return nil, lastErr
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			return nil, &permanentError{status: resp.StatusCode, err: fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))}
		}
		return resp, nil
	}
}

// permanentError marks a failure retries cannot fix (4xx other than
// 429): the stream error it produces is flagged not retryable and
// carries the upstream status in its code (http_401, http_422, …) so
// the failover layer can tell request-shape errors from
// provider-specific ones.
type permanentError struct {
	status int
	err    error
}

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// errEvent builds the terminal error event for a request failure.
func errEvent(err error) stream.StreamEvent {
	var perm *permanentError
	retryable := !errors.As(err, &perm)
	code := "provider_error"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case perm != nil && isContextLengthMessage(perm.Error()):
		code = "context_length"
	case perm != nil && perm.status != 0:
		code = fmt.Sprintf("http_%d", perm.status)
	}
	return stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
		Code:      code,
		Message:   err.Error(),
		Retryable: retryable,
	}}
}

// contextLengthSignatures are provider error phrasings that mean "the
// request itself is too big for this model's context window", a
// prompt-shape failure, not a transient one, so it gets its own code
// distinct from the generic http_<status>.
var contextLengthSignatures = []string{
	"exceeds max length",
	"exceeds the context window",
	"context length",
	"context_length_exceeded",
	"maximum context length",
	"too many tokens",
	"prompt is too long",
	"input is too long",
	"request too large",
}

// isContextLengthMessage reports whether s matches any known
// context-length rejection phrasing, case-insensitively.
func isContextLengthMessage(s string) bool {
	lower := strings.ToLower(s)
	for _, sig := range contextLengthSignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// emit sends an event unless ctx is done; it reports whether the send
// happened. Streams must never block forever on an abandoned consumer.
func emit(ctx context.Context, ch chan<- stream.StreamEvent, ev stream.StreamEvent) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// relayFunc translates one provider's wire stream into normalized
// events. It reports whether the stream reached its own terminal event
// (done or error already emitted) and, when it did not, the read error
// that cut it off (nil for a clean early EOF).
type relayFunc func(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (finished bool, err error)

// runStream is the streaming skeleton shared by every HTTP driver:
// goroutine launch, per-call timeout, request retries, response body
// lifecycle, terminal error emission, and the incomplete+done tail
// when a stream cuts off mid-flight. Drivers own only request building
// and delta parsing.
//
// timeout bounds IDLE gaps, not the call's total wall-clock: a slow
// CPU-only backend (e.g. remote Ollama) can take longer than any fixed
// ceiling to finish a response while still emitting chunks the whole
// time, so a flat deadline forces an impossible choice between cutting
// live generation short and leaving a genuinely stuck stream to hang
// forever. The watchdog resets on every relayed event; only a gap with
// no activity for a full `timeout` cancels the call.
func runStream(ctx context.Context, client *http.Client, timeout time.Duration, retries int, build func(ctx context.Context) (*http.Request, error), relay relayFunc) <-chan stream.StreamEvent {
	ch := make(chan stream.StreamEvent)
	go func() {
		defer close(ch)
		callCtx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		idle := time.AfterFunc(timeout, func() { cancel(context.DeadlineExceeded) })
		defer idle.Stop()
		resetIdle := func() { idle.Reset(timeout) }

		resp, err := doWithRetry(callCtx, client, retries, func() (*http.Request, error) {
			return build(callCtx)
		}, func(ri stream.RetryInfo) bool {
			resetIdle()
			return emit(callCtx, ch, stream.StreamEvent{Type: stream.EventRetry, Retry: &ri})
		})
		if err != nil {
			// Terminal events gate on the PARENT ctx: the per-call
			// timeout expiring is exactly when the consumer must still
			// receive the error.
			emit(ctx, ch, errEvent(err))
			return
		}
		defer func() { _ = resp.Body.Close() }()

		// relayCh proxies every event to ch and resets the idle watchdog
		// on each one, so any activity (chunk, tool call, usage, retry)
		// counts — not just raw bytes off the wire.
		relayCh := make(chan stream.StreamEvent)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range relayCh {
				resetIdle()
				if !emit(callCtx, ch, ev) {
					return
				}
			}
		}()
		finished, readErr := relay(callCtx, resp.Body, relayCh)
		close(relayCh)
		<-done

		if finished {
			return
		}
		reason := "stream ended before completion"
		if readErr != nil {
			reason = readErr.Error()
		}
		if emit(ctx, ch, stream.StreamEvent{Type: stream.EventIncomplete, Text: reason}) {
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventDone})
		}
	}()
	return ch
}

// toolAccumulator assembles streamed tool calls. Providers emit a
// start (id+name), argument fragments, and an end signal — sometimes
// interleaved across parallel calls, keyed by index. An empty argument
// set normalizes to "{}".
type toolAccumulator struct {
	byKey map[int]*pendingTool
	order []int
}

type pendingTool struct {
	id, name string
	args     bytes.Buffer
}

func newToolAccumulator() *toolAccumulator {
	return &toolAccumulator{byKey: map[int]*pendingTool{}}
}

// start registers a call under key and returns its tool_start event.
func (a *toolAccumulator) start(key int, id, name string) stream.StreamEvent {
	a.byKey[key] = &pendingTool{id: id, name: name}
	a.order = append(a.order, key)
	return stream.StreamEvent{Type: stream.EventToolStart, ToolCall: &stream.ToolCallEvent{ID: id, Name: name}}
}

// known reports whether key has a pending call.
func (a *toolAccumulator) known(key int) bool {
	_, ok := a.byKey[key]
	return ok
}

// append adds an argument fragment to key's pending call.
func (a *toolAccumulator) append(key int, fragment string) {
	if t, ok := a.byKey[key]; ok {
		t.args.WriteString(fragment)
	}
}

// finish completes key's call, returning its tool_end event.
func (a *toolAccumulator) finish(key int) (stream.StreamEvent, bool) {
	t, ok := a.byKey[key]
	if !ok {
		return stream.StreamEvent{}, false
	}
	delete(a.byKey, key)
	for i, k := range a.order {
		if k == key {
			a.order = append(a.order[:i], a.order[i+1:]...)
			break
		}
	}
	return toolEndEvent(t), true
}

// finishAll completes every pending call in the order opened.
func (a *toolAccumulator) finishAll() []stream.StreamEvent {
	events := make([]stream.StreamEvent, 0, len(a.order))
	for _, key := range a.order {
		events = append(events, toolEndEvent(a.byKey[key]))
	}
	a.byKey = map[int]*pendingTool{}
	a.order = nil
	return events
}

func toolEndEvent(t *pendingTool) stream.StreamEvent {
	args := t.args.Bytes()
	if len(args) == 0 {
		args = []byte("{}")
	}
	return stream.StreamEvent{Type: stream.EventToolEnd, ToolCall: &stream.ToolCallEvent{
		ID: t.id, Name: t.name, Input: json.RawMessage(args),
	}}
}
