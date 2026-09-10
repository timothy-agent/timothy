package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// TestRetryableFor pins the retry classification per code (D-104): a
// refusal the model cannot talk its way past is not retryable, a
// transient or external failure is, and an unrecognised code defaults
// to retryable.
func TestRetryableFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code string
		want bool
	}{
		{codePolicyDenied, false},
		{codeUnknownTool, false},
		{codeCallCap, false},
		{codeUserDenied, false},
		{codeTimeout, true},
		{codeNetwork, true},
		{codeGatewayUnavailable, true},
		{codeToolError, true},
		{"something_new", true},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			if got := retryableFor(tc.code); got != tc.want {
				t.Fatalf("retryableFor(%q) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

// TestWrapToolErrorShape proves the wrapped result is a valid object
// carrying the code, the untouched message and the flag.
func TestWrapToolErrorShape(t *testing.T) {
	t.Parallel()
	got := wrapToolError(codePolicyDenied, "denied: no shell redirects. This is a hard policy; do not retry the same call.", 0)
	var te toolError
	if err := json.Unmarshal([]byte(got), &te); err != nil {
		t.Fatalf("unmarshal %q: %v", got, err)
	}
	if te.Error != codePolicyDenied {
		t.Fatalf("error = %q, want %q", te.Error, codePolicyDenied)
	}
	if te.Retryable {
		t.Fatal("policy_denied must not be retryable")
	}
	if !strings.Contains(te.Message, "do not retry the same call") {
		t.Fatalf("message lost the remediation prose: %q", te.Message)
	}
}

// TestWrapToolErrorKeepsPartialOutput pins the shell-timeout path: the
// partial output the command produced before the deadline stays in
// message, so the model can still use it.
func TestWrapToolErrorKeepsPartialOutput(t *testing.T) {
	t.Parallel()
	raw := "command timed out after 30s\nline one\nline two"
	var te toolError
	if err := json.Unmarshal([]byte(wrapToolError(codeTimeout, raw, 0)), &te); err != nil {
		t.Fatal(err)
	}
	if te.Message != raw {
		t.Fatalf("message = %q, want %q", te.Message, raw)
	}
	if !te.Retryable {
		t.Fatal("timeout must be retryable")
	}
}

// TestWrapToolErrorCapKeepsValidJSON proves the message is trimmed
// before marshaling, so capToolResult never has to cut the object
// itself and the model always receives parseable JSON.
func TestWrapToolErrorCapKeepsValidJSON(t *testing.T) {
	t.Parallel()
	const capBytes = 512
	got := wrapToolError(codeToolError, strings.Repeat("x", 4000), capBytes)
	if len(got) > capBytes {
		t.Fatalf("wrapped length = %d, want <= %d", len(got), capBytes)
	}
	var te toolError
	if err := json.Unmarshal([]byte(got), &te); err != nil {
		t.Fatalf("capped result is not valid JSON: %v", err)
	}
	if !strings.HasSuffix(te.Message, "[truncated]") {
		t.Fatalf("capped message lost its marker: %q", te.Message)
	}
	if got := capToolResult(got, capBytes); !json.Valid([]byte(got)) {
		t.Fatal("capToolResult cut the wrapped object")
	}
}

// TestWrapToolErrorNoCap leaves a long message intact when the turn
// sets no cap.
func TestWrapToolErrorNoCap(t *testing.T) {
	t.Parallel()
	msg := strings.Repeat("y", 4000)
	var te toolError
	if err := json.Unmarshal([]byte(wrapToolError(codeToolError, msg, 0)), &te); err != nil {
		t.Fatal(err)
	}
	if te.Message != msg {
		t.Fatalf("message length = %d, want %d", len(te.Message), len(msg))
	}
}

// TestAlreadyStructured pins the passthrough guard: a tool that emits
// its own object with an "error" key is not wrapped again, everything
// else is.
func TestAlreadyStructured(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"own error object", `{"error":"quota","message":"over limit"}`, true},
		{"leading whitespace", "\n  {\"error\":\"quota\"}", true},
		{"object without error key", `{"result":"fine"}`, false},
		{"plain prose", "tool failed: dial tcp: refused", false},
		{"json array", `[{"error":"quota"}]`, false},
		{"broken json", `{"error":`, false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := alreadyStructured(tc.in); got != tc.want {
				t.Fatalf("alreadyStructured(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestClassifyToolError pins the mapping from a tool's own error to a
// code, all by errors.Is/As and never by message text.
func TestClassifyToolError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"shell timeout sentinel", fmt.Errorf("command timed out after 30s: %w", tools.ErrTimeout), codeTimeout},
		{"context deadline", fmt.Errorf("call: %w", context.DeadlineExceeded), codeTimeout},
		{"net timeout", &net.DNSError{Err: "i/o timeout", IsTimeout: true}, codeTimeout},
		{"dial failure", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, codeNetwork},
		{"plain failure", errors.New("bad request"), codeToolError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyToolError(tc.err); got != tc.want {
				t.Fatalf("classifyToolError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestAgentWrapsToolErrorForModel walks a failing call through the
// loop and proves the D-104 boundary: the model gets the structured
// object with the right code and flag, the stream digest and the
// audit line keep the raw prose, and IsError stays true so the turn
// continues at full effort.
func TestAgentWrapsToolErrorForModel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		err           error
		out           string
		wantCode      string
		wantRetryable bool
		wantInMessage string
		wantDigest    string
	}{
		{
			name: "shell timeout keeps partial output", err: fmt.Errorf("command timed out after 1s: %w", tools.ErrTimeout),
			out: "half a line", wantCode: codeTimeout, wantRetryable: true,
			wantInMessage: "half a line", wantDigest: "command timed out after 1s",
		},
		{
			name: "constraint violation is not retryable", err: &tools.Violation{Msg: "destructive command refused"},
			wantCode: codePolicyDenied, wantRetryable: false,
			wantInMessage: "destructive command refused", wantDigest: "destructive command refused",
		},
		{
			name: "generic failure is retryable", err: errors.New("upstream said no"),
			wantCode: codeToolError, wantRetryable: true,
			wantInMessage: "upstream said no", wantDigest: "tool failed: upstream said no",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failing := &tools.Tool{
				Name:        "flaky",
				Description: "fails on purpose",
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
				Execute: func(context.Context, json.RawMessage) (string, error) {
					return tc.out, tc.err
				},
			}
			gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
				toolCallStep([2]string{"flaky", `{}`}),
				finalStep("done"),
			}}
			a, audit, _, _ := testAgent(t, gw, failing)
			ch, err := a.Start(t.Context(), Request{
				SessionID: "s1", Route: "coding",
				Messages: []provider.Message{{Role: "user", Content: "go"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			evs := collect(t, ch)

			results := ofType(evs, stream.EventToolResult)
			if len(results) != 1 {
				t.Fatalf("tool results = %d, want 1", len(results))
			}
			if !strings.Contains(results[0].ToolResult.Digest, tc.wantDigest) {
				t.Fatalf("stream digest = %q, want the raw prose %q", results[0].ToolResult.Digest, tc.wantDigest)
			}
			if len(audit.entries) != 1 || !strings.Contains(audit.entries[0].Error, tc.wantDigest) {
				t.Fatalf("audit entries = %+v, want the raw prose", audit.entries)
			}

			var got *provider.ToolResult
			for _, m := range gw.requests[1].Messages {
				if m.ToolResult != nil {
					got = m.ToolResult
				}
			}
			if got == nil {
				t.Fatal("no tool result reached the model")
			}
			if !got.IsError {
				t.Fatal("wrapped failure must keep IsError true")
			}
			var te toolError
			if err := json.Unmarshal([]byte(got.Content), &te); err != nil {
				t.Fatalf("model content is not structured: %q", got.Content)
			}
			if te.Error != tc.wantCode || te.Retryable != tc.wantRetryable {
				t.Fatalf("structured error = %+v, want code %q retryable %v", te, tc.wantCode, tc.wantRetryable)
			}
			if !strings.Contains(te.Message, tc.wantInMessage) {
				t.Fatalf("message = %q, want it to contain %q", te.Message, tc.wantInMessage)
			}
			// D-020: an error result keeps full effort.
			if gw.requests[1].Effort != "" {
				t.Fatalf("effort after error = %q, want normal", gw.requests[1].Effort)
			}
		})
	}
}

// TestAgentUnknownToolIsNotRetryable proves a hallucinated tool name
// is tagged unknown_tool with retryable false.
func TestAgentUnknownToolIsNotRetryable(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"nothing_real", `{}`}),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw)
	ch, err := a.Start(t.Context(), Request{
		SessionID: "s1", Route: "coding",
		Messages: []provider.Message{{Role: "user", Content: "go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	var got *provider.ToolResult
	for _, m := range gw.requests[1].Messages {
		if m.ToolResult != nil {
			got = m.ToolResult
		}
	}
	if got == nil {
		t.Fatal("no tool result reached the model")
	}
	var te toolError
	if err := json.Unmarshal([]byte(got.Content), &te); err != nil {
		t.Fatalf("model content is not structured: %q", got.Content)
	}
	if te.Error != codeUnknownTool || te.Retryable {
		t.Fatalf("structured error = %+v, want unknown_tool and retryable false", te)
	}
	if !strings.Contains(te.Message, "unknown tool") {
		t.Fatalf("message = %q, want the original guidance", te.Message)
	}
}

// TestAgentPassesThroughToolOwnStructuredError proves a tool that
// already emits an object with an "error" key is relayed unwrapped.
func TestAgentPassesThroughToolOwnStructuredError(t *testing.T) {
	t.Parallel()
	const own = `{"error":"quota_exhausted","message":"daily limit","retryable":false}`
	structured := &tools.Tool{
		Name:        "quota",
		Description: "returns its own structured error",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New(own)
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"quota", `{}`}),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw, structured)
	ch, err := a.Start(t.Context(), Request{
		SessionID: "s1", Route: "coding",
		Messages: []provider.Message{{Role: "user", Content: "go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	var got *provider.ToolResult
	for _, m := range gw.requests[1].Messages {
		if m.ToolResult != nil {
			got = m.ToolResult
		}
	}
	if got == nil {
		t.Fatal("no tool result reached the model")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got.Content), &obj); err != nil {
		t.Fatalf("content is not an object: %q", got.Content)
	}
	if _, nested := obj["message"]; !nested {
		t.Fatalf("content lost its shape: %q", got.Content)
	}
	var code string
	if err := json.Unmarshal(obj["error"], &code); err != nil || code != "quota_exhausted" {
		t.Fatalf("content = %q, want the tool's own error code passed through", got.Content)
	}
}
