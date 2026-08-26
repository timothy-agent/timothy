package ledger

import (
	"bytes"
	"log/slog"
	"math"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

func TestCost(t *testing.T) {
	t.Parallel()
	prices := &router.ModelPrices{
		InputPerMTok: 3, OutputPerMTok: 15,
		CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75,
	}

	tests := []struct {
		name   string
		prices *router.ModelPrices
		usage  *stream.Usage
		want   *float64
	}{
		{
			name: "nil prices means unknown", usage: &stream.Usage{InputTokens: 100},
			want: nil,
		},
		{
			name: "nil usage means unknown", prices: prices,
			want: nil,
		},
		{
			name: "plain tokens", prices: prices,
			usage: &stream.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
			want:  f(18),
		},
		{
			name: "cache tokens priced", prices: prices,
			usage: &stream.Usage{InputTokens: 100_000, OutputTokens: 50_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 200_000},
			want:  f(0.3 + 0.75 + 0.3 + 0.75),
		},
		{
			name: "zero usage costs zero", prices: prices,
			usage: &stream.Usage{},
			want:  f(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Cost(tt.prices, tt.usage)
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("Cost() = %v, want nil", *got)
			case tt.want != nil && got == nil:
				t.Fatalf("Cost() = nil, want %v", *tt.want)
			case tt.want != nil && math.Abs(*got-*tt.want) > 1e-9:
				t.Fatalf("Cost() = %v, want %v", *got, *tt.want)
			}
		})
	}
}

func f(v float64) *float64 { return &v }

func TestRecordUnpricedUsageWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entry   Entry
		wantLog bool
	}{
		{
			name:    "billable usage with nil cost warns",
			entry:   Entry{Provider: "openai", Model: "gpt-5.2", Status: "ok", Usage: &stream.Usage{InputTokens: 10, OutputTokens: 5}},
			wantLog: true,
		},
		{
			name:    "zero tokens does not warn",
			entry:   Entry{Provider: "openai", Model: "gpt-5.2", Status: "ok", Usage: &stream.Usage{}},
			wantLog: false,
		},
		{
			name:    "nil usage does not warn",
			entry:   Entry{Provider: "openai", Model: "gpt-5.2", Status: "ok"},
			wantLog: false,
		},
		{
			name:    "errored status does not warn",
			entry:   Entry{Provider: "openai", Model: "gpt-5.2", Status: "error", Usage: &stream.Usage{InputTokens: 10}},
			wantLog: false,
		},
		{
			name:    "incomplete status does not warn",
			entry:   Entry{Provider: "openai", Model: "gpt-5.2", Status: "incomplete", Usage: &stream.Usage{InputTokens: 10}},
			wantLog: false,
		},
		{
			name:    "non-nil cost does not warn",
			entry:   Entry{Provider: "openai", Model: "gpt-5.2", Status: "ok", Usage: &stream.Usage{InputTokens: 10}, Cost: f(0.01)},
			wantLog: false,
		},
		{
			name:    "unbilled entry does not warn",
			entry:   Entry{Provider: "claude-cli", Model: "opus", Status: "ok", Usage: &stream.Usage{InputTokens: 10}, Unbilled: true},
			wantLog: false,
		},
		{
			name:    "local provider does not warn",
			entry:   Entry{Provider: "ollama", Model: "qwen3:4b", Status: "ok", Usage: &stream.Usage{InputTokens: 10}, Local: true},
			wantLog: false,
		},
		{
			name:    "remote provider still warns",
			entry:   Entry{Provider: "openai", Model: "gpt-5.2", Status: "ok", Usage: &stream.Usage{InputTokens: 10}, Local: false},
			wantLog: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_unpriced_usage_total"})
			l := New(degradedPool(t), log, counter)

			l.Record(t.Context(), tt.entry)

			gotLog := strings.Contains(buf.String(), "unpriced usage recorded")
			if gotLog != tt.wantLog {
				t.Fatalf("log contains warning = %v, want %v (log: %s)", gotLog, tt.wantLog, buf.String())
			}
			wantCount := 0.0
			if tt.wantLog {
				wantCount = 1
			}
			if got := testutil.ToFloat64(counter); got != wantCount {
				t.Fatalf("counter = %v, want %v", got, wantCount)
			}
		})
	}
}

func TestRecordUnpricedUsageNilCounterDoesNotPanic(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	l := New(degradedPool(t), log, nil)

	l.Record(t.Context(), Entry{Provider: "openai", Model: "gpt-5.2", Status: "ok", Usage: &stream.Usage{InputTokens: 10}})

	if !strings.Contains(buf.String(), "unpriced usage recorded") {
		t.Fatalf("expected warning log, got: %s", buf.String())
	}
}

// degradedPool returns a pool with no DSN — permanently degraded, so
// Record's DB write fails fast without touching the network.
func degradedPool(t *testing.T) *pgpool.Pool {
	t.Helper()
	return pgpool.New(t.Context(), "", slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
}
