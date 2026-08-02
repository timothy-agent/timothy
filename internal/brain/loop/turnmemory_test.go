package loop

import (
	"context"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// scriptedGW returns one canned reply per call, in order.
type scriptedGW struct {
	replies []string
	errs    []bool
	calls   int
	lastReq gwclient.StreamRequest
}

func (g *scriptedGW) RouteForRole(_ context.Context, role string) (string, bool, error) {
	return role, true, nil
}

func (g *scriptedGW) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	g.lastReq = req
	i := g.calls
	g.calls++
	ch := make(chan stream.StreamEvent, 3)
	if i < len(g.errs) && g.errs[i] {
		ch <- stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{Code: "x", Message: "provider down"}}
	} else {
		reply := g.replies[min(i, len(g.replies)-1)]
		// split across two chunks: parsing must handle assembly
		ch <- stream.StreamEvent{Type: stream.EventChunk, Text: reply[:len(reply)/2]}
		ch <- stream.StreamEvent{Type: stream.EventChunk, Text: reply[len(reply)/2:]}
		ch <- stream.StreamEvent{Type: stream.EventDone}
	}
	close(ch)
	return ch, nil
}

const validJSON = `{"files_changed":["main.go"],"failures":[{"what":"go test","why":"timeout"}],"key_findings":["user prefers tabs"]}`

func TestDistillTurnParsesValidReply(t *testing.T) {
	t.Parallel()
	gw := &scriptedGW{replies: []string{validJSON}}

	tm := DistillTurn(t.Context(), gw, "s1", "user: hi / assistant: hello", "")
	if tm == nil {
		t.Fatal("DistillTurn returned nil for valid reply")
	}
	if tm.FilesChanged[0] != "main.go" || tm.Failures[0].Why != "timeout" || tm.KeyFindings[0] != "user prefers tabs" {
		t.Fatalf("turn memory = %+v", tm)
	}
	if gw.calls != 1 {
		t.Fatalf("calls = %d, want 1", gw.calls)
	}
}

func TestDistillTurnStripsFences(t *testing.T) {
	t.Parallel()
	gw := &scriptedGW{replies: []string{"```json\n" + validJSON + "\n```"}}

	if tm := DistillTurn(t.Context(), gw, "s1", "turn", ""); tm == nil || tm.FilesChanged[0] != "main.go" {
		t.Fatalf("fenced reply not parsed: %+v", tm)
	}
}

func TestDistillTurnRetriesOnceThenGivesUp(t *testing.T) {
	t.Parallel()
	// invalid then valid → parsed on the retry
	gw := &scriptedGW{replies: []string{"sorry, here is the JSON you asked for", validJSON}}
	if tm := DistillTurn(t.Context(), gw, "s1", "turn", ""); tm == nil {
		t.Fatal("retry after invalid output did not recover")
	}
	if gw.calls != 2 {
		t.Fatalf("calls = %d, want 2", gw.calls)
	}

	// invalid twice → nil, never an error that blocks the turn
	gw = &scriptedGW{replies: []string{"not json", "still not json"}}
	if tm := DistillTurn(t.Context(), gw, "s1", "turn", ""); tm != nil {
		t.Fatalf("expected nil after two invalid replies, got %+v", tm)
	}
	if gw.calls != 2 {
		t.Fatalf("calls = %d, want exactly 2 attempts", gw.calls)
	}
}

func TestDistillTurnProviderErrorRetries(t *testing.T) {
	t.Parallel()
	gw := &scriptedGW{errs: []bool{true, false}, replies: []string{validJSON, validJSON}}
	if tm := DistillTurn(t.Context(), gw, "s1", "turn", ""); tm == nil {
		t.Fatal("provider error on first attempt should retry and recover")
	}
}

// TestDistillTurnDefaultsToSummarizeRoute pins the fix: non-sensitive
// turns distill on the cheap cloud side-call route, not always-local —
// the local route now serves a reasoning model that burns its whole
// completion budget thinking before the JSON answer.
func TestDistillTurnDefaultsToSummarizeRoute(t *testing.T) {
	t.Parallel()
	gw := &scriptedGW{replies: []string{validJSON}}

	DistillTurn(t.Context(), gw, "s1", "turn", "")

	if gw.lastReq.Route != "summarize" {
		t.Fatalf("route = %q, want summarize", gw.lastReq.Route)
	}
}

// TestDistillTurnHonorsSensitiveRoutePin proves a turn that ran a
// sensitive tool keeps its distillation on the pinned route instead of
// falling back to the cheap default — same privacy floor as memory
// extraction (extract.Request.Route).
func TestDistillTurnHonorsSensitiveRoutePin(t *testing.T) {
	t.Parallel()
	gw := &scriptedGW{replies: []string{validJSON}}

	DistillTurn(t.Context(), gw, "s1", "turn", "local")

	if gw.lastReq.Route != "local" {
		t.Fatalf("route = %q, want local (sensitive pin)", gw.lastReq.Route)
	}
}

func TestParseTurnMemoryStrictness(t *testing.T) {
	t.Parallel()
	if _, err := parseTurnMemory(`{"files_changed":[],"unknown_field":1}`); err == nil {
		t.Fatal("unknown field accepted; schema must be strict")
	}

	tm, err := parseTurnMemory(`{"key_findings":["1","2","3","4","5","6","7"]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tm.KeyFindings) != maxKeyFindings {
		t.Fatalf("findings = %d, want clamped to %d", len(tm.KeyFindings), maxKeyFindings)
	}
}
