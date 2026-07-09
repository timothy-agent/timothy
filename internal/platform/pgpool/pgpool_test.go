package pgpool

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEmptyDSNStaysDegraded(t *testing.T) {
	t.Parallel()
	p := New(t.Context(), "", discard())

	if p.Healthy() {
		t.Fatal("Healthy() = true with empty DSN")
	}
	if _, err := p.Get(); !errors.Is(err, ErrDegraded) {
		t.Fatalf("Get() error = %v, want ErrDegraded", err)
	}
	status, detail := p.Status()
	if status != "degraded" || detail == "" {
		t.Fatalf("Status() = %q, %q; want degraded with detail", status, detail)
	}
}

func TestUnreachableDSNDegradedNoPanic(t *testing.T) {
	t.Parallel()
	// Port 1 refuses immediately; the manager must retry, not panic.
	p := New(t.Context(), "postgres://u:p@127.0.0.1:1/db?connect_timeout=1", discard())

	time.Sleep(200 * time.Millisecond)
	if p.Healthy() {
		t.Fatal("Healthy() = true for unreachable database")
	}
	if _, err := p.Get(); !errors.Is(err, ErrDegraded) {
		t.Fatalf("Get() error = %v, want ErrDegraded", err)
	}
}

func TestWaitHealthyHonorsContext(t *testing.T) {
	t.Parallel()
	p := New(t.Context(), "", discard())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.WaitHealthy(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitHealthy() = %v, want DeadlineExceeded", err)
	}
}
