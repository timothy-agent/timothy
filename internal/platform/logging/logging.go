// Package logging provides structured JSON logging with trace-id
// correlation. Every line carries the service name; lines logged with
// a request context also carry its trace_id.
package logging

import (
	"context"
	"log/slog"
	"os"
)

type ctxKey struct{}

// WithTraceID returns a context carrying a trace id that the logger
// attaches to every line logged with that context.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// TraceID returns the trace id carried by ctx, or "".
func TraceID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := TraceID(ctx); id != "" {
		r.AddAttrs(slog.String("trace_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// New returns a JSON logger writing to stdout, tagged with the service
// name and trace-aware via WithTraceID.
func New(service, level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(traceHandler{h}).With(slog.String("service", service))
}
