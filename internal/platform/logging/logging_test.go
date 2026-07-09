package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func newBufferLogger(buf *bytes.Buffer) *slog.Logger {
	h := slog.NewJSONHandler(buf, nil)
	return slog.New(traceHandler{h}).With(slog.String("service", "test"))
}

func logLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	return m
}

func TestServiceAttr(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	newBufferLogger(&buf).Info("hello")

	m := logLine(t, &buf)
	if m["service"] != "test" {
		t.Fatalf("service = %v, want test", m["service"])
	}
	if _, ok := m["trace_id"]; ok {
		t.Fatal("trace_id present without a traced context")
	}
}

func TestTraceIDFromContext(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	ctx := WithTraceID(context.Background(), "abc123")
	newBufferLogger(&buf).InfoContext(ctx, "hello")

	m := logLine(t, &buf)
	if m["trace_id"] != "abc123" {
		t.Fatalf("trace_id = %v, want abc123", m["trace_id"])
	}
}

func TestTraceIDRoundTrip(t *testing.T) {
	t.Parallel()
	if got := TraceID(context.Background()); got != "" {
		t.Fatalf("TraceID(empty ctx) = %q, want empty", got)
	}
	ctx := WithTraceID(context.Background(), "id-1")
	if got := TraceID(ctx); got != "id-1" {
		t.Fatalf("TraceID = %q, want id-1", got)
	}
}
