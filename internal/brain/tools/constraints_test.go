package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

func testToolCalls() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_tool_calls_total"}, []string{"tool", "outcome"})
}

func constrainedEcho(t *testing.T) *Constrained {
	t.Helper()
	r := NewRegistry()
	err := r.Register(&Tool{
		Name:        "echo",
		Description: "echoes text",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"text": {"type": "string"},
				"count": {"type": "integer"}
			},
			"required": ["text"],
			"additionalProperties": false
		}`),
		Execute: func(_ context.Context, args json.RawMessage) (string, error) {
			return string(args), nil
		},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	c, err := NewConstrained(r, testToolCalls())
	if err != nil {
		t.Fatalf("NewConstrained: %v", err)
	}
	return c
}

func TestConstrainedSchemaValidation(t *testing.T) {
	t.Parallel()
	c := constrainedEcho(t)

	tests := []struct {
		name    string
		tool    string
		args    string
		wantErr string
	}{
		{name: "valid passes", tool: "echo", args: `{"text":"hi"}`},
		{name: "missing required", tool: "echo", args: `{}`, wantErr: "failed validation"},
		{name: "wrong type", tool: "echo", args: `{"text":42}`, wantErr: "failed validation"},
		{name: "extra property", tool: "echo", args: `{"text":"hi","bogus":1}`, wantErr: "failed validation"},
		{name: "malformed json", tool: "echo", args: `{"text":`, wantErr: "not valid JSON"},
		{name: "unknown tool", tool: "nope", args: `{}`, wantErr: "unknown tool"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := c.Execute(context.Background(), tc.tool, json.RawMessage(tc.args))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Execute: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if !IsViolation(err) {
				t.Fatalf("err %v is not a Violation — the loop would treat it as a fault", err)
			}
		})
	}
}

func TestConstrainedRequiresSchemas(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	_ = r.Register(&Tool{Name: "bare", Description: "no schema"})
	if _, err := NewConstrained(r, testToolCalls()); err == nil {
		t.Fatal("tool without schema accepted")
	}
}

func TestConstrainedRecordsToolCallOutcomes(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if err := r.Register(&Tool{
		Name:        "echo",
		Description: "echoes text",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		Execute: func(_ context.Context, args json.RawMessage) (string, error) {
			return string(args), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	calls := testToolCalls()
	c, err := NewConstrained(r, calls)
	if err != nil {
		t.Fatalf("NewConstrained: %v", err)
	}

	if _, err := c.Execute(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := c.Execute(context.Background(), "echo", json.RawMessage(`{}`)); err == nil {
		t.Fatal("want validation violation")
	}
	if _, err := c.Execute(context.Background(), "nope", json.RawMessage(`{}`)); err == nil {
		t.Fatal("want unknown-tool violation")
	}

	if got := testutil.ToFloat64(calls.WithLabelValues("echo", "ok")); got != 1 {
		t.Fatalf("echo/ok = %v, want 1", got)
	}
	if got := testutil.ToFloat64(calls.WithLabelValues("echo", "violation")); got != 1 {
		t.Fatalf("echo/violation = %v, want 1", got)
	}
	if got := testutil.ToFloat64(calls.WithLabelValues("nope", "violation")); got != 1 {
		t.Fatalf("nope/violation = %v, want 1", got)
	}
}

func TestConstrainedAppliesClamp(t *testing.T) {
	t.Parallel()
	c := constrainedEcho(t)
	c.SetClamp("echo", func(raw json.RawMessage) (json.RawMessage, error) {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		if n, ok := m["count"].(float64); ok && n > 5 {
			m["count"] = 5
		}
		return json.Marshal(m)
	})

	got, err := c.Execute(context.Background(), "echo", json.RawMessage(`{"text":"x","count":99}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, `"count":5`) {
		t.Fatalf("clamp not applied: %s", got)
	}
}

func TestWithinRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	// A symlink inside the workspace pointing out of it.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "inside ok", path: filepath.Join(root, "sub")},
		{name: "new file under root ok", path: filepath.Join(root, "sub", "new", "file.txt")},
		{name: "root itself ok", path: root},
		{name: "relative rejected", path: "sub/file.txt", wantErr: "must be absolute"},
		{name: "dotdot escape", path: filepath.Join(root, "..", "elsewhere"), wantErr: "outside the workspace"},
		{name: "absolute outside", path: "/etc/passwd", wantErr: "outside the workspace"},
		{name: "symlink escape", path: filepath.Join(link, "f.txt"), wantErr: "outside the workspace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := WithinRoot(root, tc.path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("WithinRoot: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if !IsViolation(err) {
				t.Fatalf("err %v is not a Violation", err)
			}
		})
	}
}

func TestCheckCommandPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "target"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the workspace pointing out of it — what
	// guardSubject's lexical check on its own cannot see through.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		command string
		wantErr string
	}{
		{name: "no root configured", command: "rm -rf /etc/passwd"},
		{name: "relative path ok", command: "rm -rf reports/"},
		{name: "plain absolute path within root ok", command: "rm -rf " + filepath.Join(root, "sub")},
		{name: "dev null exempt", command: "echo hi > /dev/null"},
		{
			name:    "symlink escape rejected",
			command: "rm -rf " + filepath.Join(link, "target"),
			wantErr: "outside the workspace",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := root
			if tc.name == "no root configured" {
				r = ""
			}
			err := CheckCommandPaths(r, tc.command)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckCommandPaths: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if !IsViolation(err) {
				t.Fatalf("err %v is not a Violation", err)
			}
		})
	}
}

func TestCeilingFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		step, max int
		want      StepDirective
	}{
		{step: 1, max: 16, want: StepProceed},
		{step: 14, max: 16, want: StepProceed},
		{step: 15, max: 16, want: StepWarnFinalize},
		{step: 16, max: 16, want: StepForceSynthesis},
		{step: 20, max: 16, want: StepForceSynthesis},
		{step: 15, max: 0, want: StepWarnFinalize}, // default ceiling
		{step: 1, max: 2, want: StepWarnFinalize},
		{step: 2, max: 2, want: StepForceSynthesis},
	}
	for _, tc := range tests {
		if got := CeilingFor(tc.step, tc.max); got != tc.want {
			t.Errorf("CeilingFor(%d, %d) = %v, want %v", tc.step, tc.max, got, tc.want)
		}
	}
}

func TestRepeatGuard(t *testing.T) {
	t.Parallel()
	call := func(name, input string) provider.ToolCall {
		return provider.ToolCall{Name: name, Input: json.RawMessage(input)}
	}

	t.Run("same call three times in a row trips", func(t *testing.T) {
		t.Parallel()
		var g RepeatGuard
		if g.Record([]provider.ToolCall{call("search_web", `{"query":"book hotel in Nairobi"}`)}) {
			t.Fatal("tripped on first occurrence")
		}
		if g.Record([]provider.ToolCall{call("search_web", `{"query":"book hotel in Nairobi"}`)}) {
			t.Fatal("tripped on second occurrence")
		}
		if !g.Record([]provider.ToolCall{call("search_web", `{"query":"book hotel in Nairobi"}`)}) {
			t.Fatal("did not trip on third identical occurrence")
		}
	})

	t.Run("different args resets the run", func(t *testing.T) {
		t.Parallel()
		var g RepeatGuard
		g.Record([]provider.ToolCall{call("search_web", `{"query":"a"}`)})
		g.Record([]provider.ToolCall{call("search_web", `{"query":"a"}`)})
		if g.Record([]provider.ToolCall{call("search_web", `{"query":"b"}`)}) {
			t.Fatal("tripped after a genuinely different call broke the run")
		}
	})

	t.Run("exploring different tools never trips", func(t *testing.T) {
		t.Parallel()
		var g RepeatGuard
		for range 5 {
			if g.Record([]provider.ToolCall{call("search_web", `{"query":"a"}`)}) {
				t.Fatal("tripped despite alternating calls")
			}
			if g.Record([]provider.ToolCall{call("fetch_url", `{"url":"https://example.com"}`)}) {
				t.Fatal("tripped despite alternating calls")
			}
		}
	})
}

func TestNeedsRetrievalCoercion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		category  string
		toolCalls int
		coerced   bool
		want      bool
	}{
		{name: "research with no calls", category: "research", toolCalls: 0, want: true},
		{name: "research after one call", category: "research", toolCalls: 1, want: false},
		{name: "research already coerced", category: "research", toolCalls: 0, coerced: true, want: false},
		{name: "unlisted category", category: "chat", toolCalls: 0, want: false},
		{name: "empty category", category: "", toolCalls: 0, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NeedsRetrievalCoercion(tc.category, tc.toolCalls, tc.coerced); got != tc.want {
				t.Fatalf("= %v, want %v", got, tc.want)
			}
		})
	}
}
