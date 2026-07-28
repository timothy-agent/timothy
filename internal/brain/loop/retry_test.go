package loop

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// errThenScripts is a Gateway test double that returns a hard error
// from Stream a fixed number of times before falling back to
// scriptedGateway's normal script playback — for exercising the
// gw.Stream-returns-err retry path (as opposed to a terminal
// EventError arriving inside the stream).
type errThenScripts struct {
	scriptedGateway
	failsLeft int
	err       error
}

func (g *errThenScripts) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	if g.failsLeft > 0 {
		g.failsLeft--
		g.requests = append(g.requests, req)
		return nil, g.err
	}
	return g.scriptedGateway.Stream(ctx, req)
}

// retryableErrorStep is a one-event script: a terminal EventError
// with Retryable set, nothing emitted before it.
func retryableErrorStep() []stream.StreamEvent {
	return []stream.StreamEvent{
		{Type: stream.EventError, Err: &stream.StreamError{Code: "gateway_stream_cut", Message: "stream cut", Retryable: true}},
	}
}

func nonRetryableErrorStep() []stream.StreamEvent {
	return []stream.StreamEvent{
		{Type: stream.EventError, Err: &stream.StreamError{Code: "bad_request", Message: "bad request", Retryable: false}},
	}
}

// errorAfterChunkStep emits visible content before the terminal error
// — the case that must NOT retry, since the partial text already went
// to the client.
func errorAfterChunkStep() []stream.StreamEvent {
	return []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "partial"},
		{Type: stream.EventError, Err: &stream.StreamError{Code: "gateway_stream_cut", Message: "stream cut", Retryable: true}},
	}
}

func withShortRetryBackoff(t *testing.T) {
	t.Helper()
	orig := stepRetryBackoff
	stepRetryBackoff = time.Millisecond
	t.Cleanup(func() { stepRetryBackoff = orig })
}

func TestAgentRetriesStreamErrorBeforeContentThenSucceeds(t *testing.T) {
	withShortRetryBackoff(t)
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		retryableErrorStep(),
		finalStep("the answer"),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if got := ofType(evs, stream.EventRetry); len(got) != 1 {
		t.Fatalf("retry events = %d, want exactly 1: %+v", len(got), evs)
	}
	if got := ofType(evs, stream.EventError); len(got) != 0 {
		t.Fatalf("error events = %d, want 0: %+v", len(got), evs)
	}
	done := ofType(evs, stream.EventDone)
	if len(done) != 1 {
		t.Fatalf("done events = %d, want exactly 1", len(done))
	}
	chunks := ofType(evs, stream.EventChunk)
	if len(chunks) != 1 || chunks[0].Text != "the answer" {
		t.Fatalf("chunks = %+v, want exactly one 'the answer' (no duplication)", chunks)
	}
	if len(gw.requests) != 2 {
		t.Fatalf("gw.Stream calls = %d, want 2 (failed attempt + retry)", len(gw.requests))
	}
}

func TestAgentDoesNotRetryErrorAfterContentEmitted(t *testing.T) {
	withShortRetryBackoff(t)
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		errorAfterChunkStep(),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if got := ofType(evs, stream.EventRetry); len(got) != 0 {
		t.Fatalf("retry events = %d, want 0 once content emitted: %+v", len(got), evs)
	}
	if got := ofType(evs, stream.EventError); len(got) != 1 {
		t.Fatalf("error events = %d, want exactly 1", len(got))
	}
	if len(gw.requests) != 1 {
		t.Fatalf("gw.Stream calls = %d, want 1 (no retry)", len(gw.requests))
	}
}

func TestAgentDoesNotRetryNonRetryableError(t *testing.T) {
	withShortRetryBackoff(t)
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		nonRetryableErrorStep(),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if got := ofType(evs, stream.EventRetry); len(got) != 0 {
		t.Fatalf("retry events = %d, want 0 for a non-retryable error: %+v", len(got), evs)
	}
	if got := ofType(evs, stream.EventError); len(got) != 1 {
		t.Fatalf("error events = %d, want exactly 1", len(got))
	}
	if len(gw.requests) != 1 {
		t.Fatalf("gw.Stream calls = %d, want 1 (no retry)", len(gw.requests))
	}
}

func TestAgentGivesUpAfterMaxStepRetries(t *testing.T) {
	withShortRetryBackoff(t)
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		retryableErrorStep(),
		retryableErrorStep(),
		retryableErrorStep(),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if got := ofType(evs, stream.EventRetry); len(got) != maxStepRetries {
		t.Fatalf("retry events = %d, want %d (max retries, 3rd failure surfaces)", len(got), maxStepRetries)
	}
	if got := ofType(evs, stream.EventError); len(got) != 1 {
		t.Fatalf("error events = %d, want exactly 1 after budget exhausted", len(got))
	}
	if len(gw.requests) != maxStepRetries+1 {
		t.Fatalf("gw.Stream calls = %d, want %d (initial + %d retries)", len(gw.requests), maxStepRetries+1, maxStepRetries)
	}
}

func TestAgentRetriesGatewayStreamErrThenSucceeds(t *testing.T) {
	withShortRetryBackoff(t)
	gw := &errThenScripts{
		scriptedGateway: scriptedGateway{scripts: [][]stream.StreamEvent{
			finalStep("recovered"),
		}},
		failsLeft: 1,
		err:       errors.New("gateway_unavailable"),
	}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if got := ofType(evs, stream.EventRetry); len(got) != 1 {
		t.Fatalf("retry events = %d, want exactly 1: %+v", len(got), evs)
	}
	if got := ofType(evs, stream.EventError); len(got) != 0 {
		t.Fatalf("error events = %d, want 0: %+v", len(got), evs)
	}
	if got := ofType(evs, stream.EventDone); len(got) != 1 {
		t.Fatalf("done events = %d, want exactly 1", len(got))
	}
	chunks := ofType(evs, stream.EventChunk)
	if len(chunks) != 1 || chunks[0].Text != "recovered" {
		t.Fatalf("chunks = %+v, want exactly one 'recovered'", chunks)
	}
}
