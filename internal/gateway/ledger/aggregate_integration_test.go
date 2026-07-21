//go:build integration

package ledger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const aggMarker = "itest-agg-"

func testAggregator(t *testing.T) (*Aggregator, *Ledger) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Sweep at setup AND teardown (the admin tests' discipline): the
	// teardown runs after t.Context() is canceled, so its delete can
	// fail silently and leave rows that double the next test's fixture.
	sweep := func(ctx context.Context) {
		_, _ = db.Exec(ctx, "DELETE FROM cost_ledger WHERE provider LIKE $1 || '%'", aggMarker)
	}
	sweep(ctx)
	t.Cleanup(func() { sweep(context.Background()) })
	return NewAggregator(pool), New(pool, log)
}

func usd(v float64) *float64 { return &v }

// seedAgg writes a deterministic fixture: two providers, two sessions,
// an error row, and a purpose='test' row that must never be counted.
func seedAgg(t *testing.T, led *Ledger) (from, to time.Time) {
	t.Helper()
	ctx := t.Context()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)

	rows := []Entry{
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", SessionID: "s1",
			Usage:     &stream.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 20},
			LatencyMS: 100, Status: "ok", CostUSD: usd(0.10)},
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", SessionID: "s1",
			Usage:     &stream.Usage{InputTokens: 200, OutputTokens: 100},
			LatencyMS: 300, Status: "ok", CostUSD: usd(0.20)},
		{Provider: aggMarker + "b", Model: "m2", Route: "mini", SessionID: "s2",
			Usage:     &stream.Usage{InputTokens: 10, OutputTokens: 5},
			LatencyMS: 50, Status: "ok", CostUSD: usd(0.01)},
		{Provider: aggMarker + "b", Model: "m2", Route: "mini", SessionID: "s2",
			LatencyMS: 5, Status: "error", ErrorCode: "provider_error"},
		// Test-connection probe: excluded from every aggregate.
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", Purpose: "test",
			Usage:     &stream.Usage{InputTokens: 999999, OutputTokens: 999999},
			LatencyMS: 9999, Status: "ok", CostUSD: usd(99.0)},
	}
	for _, e := range rows {
		led.Record(ctx, e)
	}
	// Record stamps ts=now() server-side; the fixture window just needs
	// to bracket "now" generously.
	return base, time.Now().UTC().Add(time.Hour)
}

func TestAggregateSummaryExcludesTestTraffic(t *testing.T) {
	agg, led := testAggregator(t)
	from, to := seedAgg(t, led)

	// The shared DB may hold other rows; assert on a provider-scoped
	// series instead of global sums, and on summary deltas via series.
	points, err := agg.Series(t.Context(), from, to, "day", "provider")
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	byProvider := map[string]SeriesPoint{}
	for _, p := range points {
		if len(p.Group) >= len(aggMarker) && p.Group[:len(aggMarker)] == aggMarker {
			agg := byProvider[p.Group]
			agg.CostUSD += p.CostUSD
			agg.InputTokens += p.InputTokens
			agg.OutputTokens += p.OutputTokens
			agg.Requests += p.Requests
			agg.Errors += p.Errors
			byProvider[p.Group] = agg
		}
	}
	a, b := byProvider[aggMarker+"a"], byProvider[aggMarker+"b"]
	// Provider a: 2 real rows (the 99-dollar test probe must be gone).
	if a.Requests != 2 || a.CostUSD != 0.30 || a.InputTokens != 300 || a.OutputTokens != 150 {
		t.Fatalf("provider a = %+v, want 2 req / $0.30 / 300 in / 150 out", a)
	}
	if b.Requests != 2 || b.Errors != 1 || b.CostUSD != 0.01 {
		t.Fatalf("provider b = %+v, want 2 req / 1 error / $0.01", b)
	}

	// Summary-level exclusion: the $99 probe would dominate any real
	// total in this window; its absence proves the purpose filter.
	sum, err := agg.Summary(t.Context(), from, to)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.Requests < 4 || sum.CostUSD >= 99 {
		t.Fatalf("summary = %+v, want >=4 requests and the $99 test probe excluded", sum)
	}
}

func TestAggregateLatencyPercentiles(t *testing.T) {
	agg, led := testAggregator(t)
	from, to := seedAgg(t, led)

	rows, err := agg.Latency(t.Context(), from, to)
	if err != nil {
		t.Fatalf("Latency: %v", err)
	}
	var a *LatencyRow
	for i := range rows {
		if rows[i].Provider == aggMarker+"a" {
			a = &rows[i]
		}
		if rows[i].Provider == aggMarker+"b" && rows[i].Requests != 1 {
			// The 5ms error row must be excluded: only the 50ms ok row.
			t.Fatalf("provider b latency counted error rows: %+v", rows[i])
		}
	}
	if a == nil {
		t.Fatal("provider a missing from latency")
	}
	// Two ok rows at 100ms and 300ms: p50 interpolates to 200.
	if a.P50 != 200 || a.P99 < a.P50 {
		t.Fatalf("provider a percentiles = %+v", a)
	}
}

func TestAggregateTopSessionsAndCache(t *testing.T) {
	agg, led := testAggregator(t)
	from, to := seedAgg(t, led)

	sessions, err := agg.TopSessions(t.Context(), from, to, 50)
	if err != nil {
		t.Fatalf("TopSessions: %v", err)
	}
	pos := map[string]int{}
	for i, s := range sessions {
		pos[s.SessionID] = i
	}
	if _, ok := pos["s1"]; !ok {
		t.Fatal("s1 missing from top sessions")
	}
	if p1, p2 := pos["s1"], pos["s2"]; p2 < p1 {
		t.Fatalf("s2 ($0.01) ranked above s1 ($0.30): %v", pos)
	}

	cache, err := agg.Cache(t.Context(), from, to)
	if err != nil {
		t.Fatalf("Cache: %v", err)
	}
	for _, c := range cache {
		if c.Provider == aggMarker+"a" {
			// 20 cached vs 300 fresh input tokens.
			if c.CacheReadTokens != 20 || c.InputTokens != 300 {
				t.Fatalf("cache row = %+v", c)
			}
			return
		}
	}
	t.Fatal("provider a missing from cache rows")
}

func TestAggregateSeriesRejectsUnknownParams(t *testing.T) {
	agg, _ := testAggregator(t)
	if _, err := agg.Series(t.Context(), time.Now().Add(-time.Hour), time.Now(), "minute", "provider"); err == nil {
		t.Fatal("unknown bucket must error")
	}
	if _, err := agg.Series(t.Context(), time.Now().Add(-time.Hour), time.Now(), "day", "purpose; DROP TABLE"); err == nil {
		t.Fatal("unknown group must error")
	}
}
