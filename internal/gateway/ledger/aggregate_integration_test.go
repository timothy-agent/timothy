//go:build integration

package ledger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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
	// Sweep at setup AND teardown. The teardown runs after t.Context()
	// is canceled and the pool may already be closed, so it uses an
	// independent connection — a pool-backed delete would silently fail
	// and leave rows that double the next test's fixture.
	_, _ = db.Exec(ctx, "DELETE FROM cost_ledger WHERE provider LIKE $1 || '%'", aggMarker)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		if _, err := conn.Exec(cctx, "DELETE FROM cost_ledger WHERE provider LIKE $1 || '%'", aggMarker); err != nil {
			t.Errorf("cleanup agg rows: %v", err)
		}
	})
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

func TestAggregateTotalsGroupsWholeRangeExcludingTest(t *testing.T) {
	agg, led := testAggregator(t)
	from, to := seedAgg(t, led)

	totals, err := agg.Totals(t.Context(), from, to, "provider")
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	byGroup := map[string]GroupTotal{}
	for _, g := range totals {
		byGroup[g.Group] = g
	}
	a, b := byGroup[aggMarker+"a"], byGroup[aggMarker+"b"]
	// Same fixture as the Series-based summary test: 2 real rows for
	// provider a ($0.30, 300 in / 150 out), 2 for b ($0.01) — the $99
	// test probe must be absent from both the row count and the sum.
	if a.Requests != 2 || a.CostUSD != 0.30 || a.InputTokens != 300 || a.OutputTokens != 150 {
		t.Fatalf("totals[a] = %+v, want 2 req / $0.30 / 300 in / 150 out", a)
	}
	if b.Requests != 2 || b.CostUSD != 0.01 {
		t.Fatalf("totals[b] = %+v, want 2 req / $0.01", b)
	}

	if _, err := agg.Totals(t.Context(), from, to, "purpose; DROP TABLE"); err == nil {
		t.Fatal("unknown group must error")
	}
}

func TestAggregateMissionUsage(t *testing.T) {
	agg, led := testAggregator(t)
	ctx := t.Context()
	mission := aggMarker + "mission-1"

	rows := []Entry{
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: mission,
			Usage:     &stream.Usage{InputTokens: 100, OutputTokens: 50},
			LatencyMS: 100, Status: "ok", CostUSD: usd(0.10)},
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: mission,
			Usage:     &stream.Usage{InputTokens: 200, OutputTokens: 100},
			LatencyMS: 100, Status: "ok", CostUSD: usd(0.20)},
		// A fallback to a second model — the whole point of Models: a
		// route is a chain, not one model, so both must show up.
		{Provider: aggMarker + "a", Model: "m-local", Route: "coding", MissionID: mission,
			Usage:     &stream.Usage{InputTokens: 40, OutputTokens: 20},
			LatencyMS: 100, Status: "ok"},
		// Another mission and a test probe: both excluded.
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: aggMarker + "other",
			Usage:     &stream.Usage{InputTokens: 1000, OutputTokens: 1000},
			LatencyMS: 100, Status: "ok", CostUSD: usd(9.0)},
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: mission, Purpose: "test",
			Usage:     &stream.Usage{InputTokens: 999999, OutputTokens: 999999},
			LatencyMS: 100, Status: "ok", CostUSD: usd(99.0)},
	}
	for _, e := range rows {
		led.Record(ctx, e)
	}

	got, err := agg.Mission(ctx, mission)
	if err != nil {
		t.Fatalf("Mission: %v", err)
	}
	if got.MissionID != mission || got.CostUSD != 0.30 || got.InputTokens != 340 ||
		got.OutputTokens != 170 || got.Requests != 3 || got.UnpricedRequests != 1 {
		t.Fatalf("Mission = %+v, want mission_id=%s cost=0.30 in=340 out=170 requests=3 unpriced=1",
			got, mission)
	}
	if len(got.Models) != 2 {
		t.Fatalf("Models = %+v, want 2 distinct provider/model pairs", got.Models)
	}
	byModel := map[string]ModelUsed{}
	for _, mu := range got.Models {
		byModel[mu.Model] = mu
	}
	if mu := byModel["m1"]; mu.Provider != aggMarker+"a" || mu.Requests != 2 {
		t.Fatalf("Models[m1] = %+v, want provider=%s requests=2", mu, aggMarker+"a")
	}
	if mu := byModel["m-local"]; mu.Provider != aggMarker+"a" || mu.Requests != 1 {
		t.Fatalf("Models[m-local] = %+v, want provider=%s requests=1", mu, aggMarker+"a")
	}

	// A mission with no ledger rows is all zeros, not an error.
	empty, err := agg.Mission(ctx, aggMarker+"never-ran")
	if err != nil {
		t.Fatalf("Mission(empty): %v", err)
	}
	if empty.Requests != 0 || empty.CostUSD != 0 || len(empty.Models) != 0 {
		t.Fatalf("empty mission = %+v, want zeros and no models", empty)
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
