//go:build integration

package ledger

import (
	"context"
	"fmt"
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
			LatencyMS: 100, Status: "ok", Cost: usd(0.10)},
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", SessionID: "s1",
			Usage:     &stream.Usage{InputTokens: 200, OutputTokens: 100},
			LatencyMS: 300, Status: "ok", Cost: usd(0.20)},
		{Provider: aggMarker + "b", Model: "m2", Route: "mini", SessionID: "s2",
			Usage:     &stream.Usage{InputTokens: 10, OutputTokens: 5},
			LatencyMS: 50, Status: "ok", Cost: usd(0.01)},
		{Provider: aggMarker + "b", Model: "m2", Route: "mini", SessionID: "s2",
			LatencyMS: 5, Status: "error", ErrorCode: "provider_error"},
		// Test-connection probe: excluded from every aggregate.
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", Purpose: "test",
			Usage:     &stream.Usage{InputTokens: 999999, OutputTokens: 999999},
			LatencyMS: 9999, Status: "ok", Cost: usd(99.0)},
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
			agg.Cost += p.Cost
			agg.InputTokens += p.InputTokens
			agg.OutputTokens += p.OutputTokens
			agg.Requests += p.Requests
			agg.Errors += p.Errors
			byProvider[p.Group] = agg
		}
	}
	a, b := byProvider[aggMarker+"a"], byProvider[aggMarker+"b"]
	// Provider a: 2 real rows (the 99-dollar test probe must be gone).
	if a.Requests != 2 || a.Cost != 0.30 || a.InputTokens != 300 || a.OutputTokens != 150 {
		t.Fatalf("provider a = %+v, want 2 req / $0.30 / 300 in / 150 out", a)
	}
	if b.Requests != 2 || b.Errors != 1 || b.Cost != 0.01 {
		t.Fatalf("provider b = %+v, want 2 req / 1 error / $0.01", b)
	}

	// Summary-level exclusion: the $99 probe would dominate any real
	// total in this window; its absence proves the purpose filter.
	// The window may span more than one currency in a shared DB, so sum
	// the USD row specifically rather than assuming SummaryByCurrency
	// returns exactly one entry.
	summaries, err := agg.SummaryByCurrency(t.Context(), from, to)
	if err != nil {
		t.Fatalf("SummaryByCurrency: %v", err)
	}
	var usdRequests int64
	var usdCost float64
	for _, s := range summaries {
		if s.Currency == "USD" {
			usdRequests, usdCost = s.Requests, s.Cost
		}
	}
	if usdRequests < 4 || usdCost >= 99 {
		t.Fatalf("USD summary requests=%d cost=%v, want >=4 requests and the $99 test probe excluded", usdRequests, usdCost)
	}
}

// TestAggregateSeriesAndTotalsSplitByCurrency confirms Series and
// Totals never sum a group's cost across billing currencies: two rows
// for the same provider, one in USD and one in EUR, come back as two
// separate rows (same group, different currency), each with its own
// cost — not one row with a blended (meaningless) total.
func TestAggregateSeriesAndTotalsSplitByCurrency(t *testing.T) {
	agg, led := testAggregator(t)
	ctx := t.Context()
	provider := aggMarker + "multi-currency"
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)

	led.Record(ctx, Entry{
		Provider: provider, Model: "m1", Route: "coding",
		Usage:     &stream.Usage{InputTokens: 100, OutputTokens: 50},
		LatencyMS: 100, Status: "ok", Cost: usd(1.00), Currency: "USD",
	})
	led.Record(ctx, Entry{
		Provider: provider, Model: "m1", Route: "coding",
		Usage:     &stream.Usage{InputTokens: 200, OutputTokens: 100},
		LatencyMS: 100, Status: "ok", Cost: usd(2.00), Currency: "EUR",
	})
	from, to := base, time.Now().UTC().Add(time.Hour)

	totals, err := agg.Totals(ctx, from, to, "provider")
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	var usdRow, eurRow *GroupTotal
	for i := range totals {
		if totals[i].Group != provider {
			continue
		}
		switch totals[i].Currency {
		case "USD":
			usdRow = &totals[i]
		case "EUR":
			eurRow = &totals[i]
		}
	}
	if usdRow == nil || eurRow == nil {
		t.Fatalf("Totals for %s = %+v, want separate USD and EUR rows", provider, totals)
	}
	if usdRow.Cost != 1.00 || eurRow.Cost != 2.00 {
		t.Fatalf("Totals cost = USD:%v EUR:%v, want USD:1.00 EUR:2.00 (never summed together)", usdRow.Cost, eurRow.Cost)
	}

	points, err := agg.Series(ctx, from, to, "day", "provider")
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	var usdCost, eurCost float64
	for _, p := range points {
		if p.Group != provider {
			continue
		}
		switch p.Currency {
		case "USD":
			usdCost += p.Cost
		case "EUR":
			eurCost += p.Cost
		}
	}
	if usdCost != 1.00 || eurCost != 2.00 {
		t.Fatalf("Series cost = USD:%v EUR:%v, want USD:1.00 EUR:2.00 (never summed together)", usdCost, eurCost)
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
	if a.Requests != 2 || a.Cost != 0.30 || a.InputTokens != 300 || a.OutputTokens != 150 {
		t.Fatalf("totals[a] = %+v, want 2 req / $0.30 / 300 in / 150 out", a)
	}
	if b.Requests != 2 || b.Cost != 0.01 {
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
			LatencyMS: 100, Status: "ok", Cost: usd(0.10)},
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: mission,
			Usage:     &stream.Usage{InputTokens: 200, OutputTokens: 100},
			LatencyMS: 100, Status: "ok", Cost: usd(0.20)},
		// A fallback to a second model — the whole point of Models: a
		// route is a chain, not one model, so both must show up.
		{Provider: aggMarker + "a", Model: "m-local", Route: "coding", MissionID: mission,
			Usage:     &stream.Usage{InputTokens: 40, OutputTokens: 20},
			LatencyMS: 100, Status: "ok"},
		// Same provider/model as the harness's delegated CLI executor
		// used directly by brain too — purpose='executor' must split
		// this into its own Harness=true row rather than merging with
		// brain's m1 rows above.
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: mission, Purpose: "executor",
			Usage:     &stream.Usage{InputTokens: 10, OutputTokens: 5},
			LatencyMS: 100, Status: "ok", Cost: usd(0.05)},
		// Another mission and a test probe: both excluded.
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: aggMarker + "other",
			Usage:     &stream.Usage{InputTokens: 1000, OutputTokens: 1000},
			LatencyMS: 100, Status: "ok", Cost: usd(9.0)},
		{Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: mission, Purpose: "test",
			Usage:     &stream.Usage{InputTokens: 999999, OutputTokens: 999999},
			LatencyMS: 100, Status: "ok", Cost: usd(99.0)},
	}
	for _, e := range rows {
		led.Record(ctx, e)
	}

	got, err := agg.Mission(ctx, mission)
	if err != nil {
		t.Fatalf("Mission: %v", err)
	}
	if got.MissionID != mission || got.CostByCurrency["USD"] != 0.35 || got.InputTokens != 350 ||
		got.OutputTokens != 175 || got.Requests != 4 || got.UnpricedRequests != 1 {
		t.Fatalf("Mission = %+v, want mission_id=%s cost=0.35 in=350 out=175 requests=4 unpriced=1",
			got, mission)
	}
	// The executor-purpose row's $0.05 is the harness's; the rest is
	// brain's.
	if got.BilledBrainByCurrency["USD"] != 0.30 || got.BilledHarnessByCurrency["USD"] != 0.05 {
		t.Fatalf("BilledBrainByCurrency/BilledHarnessByCurrency = %+v/%+v, want brain=0.30 harness=0.05",
			got.BilledBrainByCurrency, got.BilledHarnessByCurrency)
	}
	// m1 appears twice: once as brain's rows, once as the harness's
	// executor row — grouping by (provider, model, harness) keeps them
	// as distinct entries instead of merging brain and harness usage
	// of the same model.
	if len(got.Models) != 3 {
		t.Fatalf("Models = %+v, want 3 distinct provider/model/harness triples", got.Models)
	}
	byKey := map[string]ModelUsed{}
	for _, mu := range got.Models {
		byKey[fmt.Sprintf("%s:%v", mu.Model, mu.Harness)] = mu
	}
	if mu := byKey["m1:false"]; mu.Provider != aggMarker+"a" || mu.Requests != 2 {
		t.Fatalf("Models[m1,harness=false] = %+v, want provider=%s requests=2", mu, aggMarker+"a")
	}
	if mu := byKey["m1:true"]; mu.Provider != aggMarker+"a" || mu.Requests != 1 {
		t.Fatalf("Models[m1,harness=true] = %+v, want provider=%s requests=1", mu, aggMarker+"a")
	}
	if mu := byKey["m-local:false"]; mu.Provider != aggMarker+"a" || mu.Requests != 1 {
		t.Fatalf("Models[m-local,harness=false] = %+v, want provider=%s requests=1", mu, aggMarker+"a")
	}

	// A mission with no ledger rows is all zeros, not an error.
	empty, err := agg.Mission(ctx, aggMarker+"never-ran")
	if err != nil {
		t.Fatalf("Mission(empty): %v", err)
	}
	if empty.Requests != 0 || len(empty.CostByCurrency) != 0 || len(empty.Models) != 0 {
		t.Fatalf("empty mission = %+v, want zeros and no models", empty)
	}
}

// TestAggregateMissionUsageMixedCurrency confirms a mission that spent
// in two different billing currencies gets both totals back distinct
// — never summed together, since that would require a guessed FX
// rate (D-013's spend-side sibling invariant).
func TestAggregateMissionUsageMixedCurrency(t *testing.T) {
	agg, led := testAggregator(t)
	ctx := t.Context()
	mission := aggMarker + "mixed-mission"

	led.Record(ctx, Entry{
		Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: mission,
		Usage:     &stream.Usage{InputTokens: 100, OutputTokens: 50},
		LatencyMS: 100, Status: "ok", Cost: usd(0.10), Currency: "USD",
	})
	led.Record(ctx, Entry{
		Provider: aggMarker + "eu", Model: "m2", Route: "coding", MissionID: mission,
		Usage:     &stream.Usage{InputTokens: 10, OutputTokens: 5},
		LatencyMS: 100, Status: "ok", Cost: usd(0.05), Currency: "EUR",
	})

	got, err := agg.Mission(ctx, mission)
	if err != nil {
		t.Fatalf("Mission: %v", err)
	}
	if len(got.CostByCurrency) != 2 || got.CostByCurrency["USD"] != 0.10 || got.CostByCurrency["EUR"] != 0.05 {
		t.Fatalf("CostByCurrency = %+v, want {USD: 0.10, EUR: 0.05}", got.CostByCurrency)
	}
}

// TestAggregateMissionUsageBrainHarnessSplit confirms billed spend
// splits by who actually incurred it: the harness's own rows
// (Purpose="executor", D-051's delegated CLI) vs everything else the
// missions engine billed directly (explore/plan/worker/review) —
// BilledBrainByCurrency + BilledHarnessByCurrency must equal
// CostByCurrency.
func TestAggregateMissionUsageBrainHarnessSplit(t *testing.T) {
	agg, led := testAggregator(t)
	ctx := t.Context()
	mission := aggMarker + "brain-harness-mission"

	led.Record(ctx, Entry{
		Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: mission,
		Agent:     "mission-worker",
		Usage:     &stream.Usage{InputTokens: 100, OutputTokens: 50},
		LatencyMS: 100, Status: "ok", Cost: usd(0.21), Purpose: "executor",
	})
	led.Record(ctx, Entry{
		Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: mission,
		Usage:     &stream.Usage{InputTokens: 200, OutputTokens: 100},
		LatencyMS: 100, Status: "ok", Cost: usd(0.11),
	})

	got, err := agg.Mission(ctx, mission)
	if err != nil {
		t.Fatalf("Mission: %v", err)
	}
	if got.CostByCurrency["USD"] != 0.32 {
		t.Fatalf("CostByCurrency = %+v, want USD 0.32", got.CostByCurrency)
	}
	if got.BilledHarnessByCurrency["USD"] != 0.21 {
		t.Fatalf("BilledHarnessByCurrency = %+v, want USD 0.21", got.BilledHarnessByCurrency)
	}
	if got.BilledBrainByCurrency["USD"] != 0.11 {
		t.Fatalf("BilledBrainByCurrency = %+v, want USD 0.11", got.BilledBrainByCurrency)
	}
}

// TestAggregateMissionUsageNotionalSplit confirms a mission billed
// through a subscription/oauth_token executor (D-051's delegated CLI
// harness records the API-equivalent price as Notional) keeps that
// cost out of CostByCurrency — the mission's true bill — while still
// surfacing it separately in NotionalCostByCurrency.
func TestAggregateMissionUsageNotionalSplit(t *testing.T) {
	agg, led := testAggregator(t)
	ctx := t.Context()
	mission := aggMarker + "notional-mission"

	led.Record(ctx, Entry{
		Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: mission,
		Usage:     &stream.Usage{InputTokens: 100, OutputTokens: 50},
		LatencyMS: 100, Status: "ok", Cost: usd(0.10),
	})
	led.Record(ctx, Entry{
		Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: mission,
		Usage:     &stream.Usage{InputTokens: 200, OutputTokens: 100},
		LatencyMS: 100, Status: "ok", Cost: usd(0.25), Notional: true,
	})

	got, err := agg.Mission(ctx, mission)
	if err != nil {
		t.Fatalf("Mission: %v", err)
	}
	if got.CostByCurrency["USD"] != 0.10 {
		t.Fatalf("CostByCurrency = %+v, want USD 0.10 (billed only)", got.CostByCurrency)
	}
	if got.NotionalCostByCurrency["USD"] != 0.25 {
		t.Fatalf("NotionalCostByCurrency = %+v, want USD 0.25", got.NotionalCostByCurrency)
	}

	// A mission billed entirely through a subscription has zero billed
	// cost and must not appear in CostByCurrency at all — same
	// omit-on-zero rule the FX decorator already applies.
	subOnly := aggMarker + "sub-only-mission"
	led.Record(ctx, Entry{
		Provider: aggMarker + "a", Model: "m1", Route: "coding", MissionID: subOnly,
		Usage:     &stream.Usage{InputTokens: 100, OutputTokens: 50},
		LatencyMS: 100, Status: "ok", Cost: usd(0.15), Notional: true,
	})
	got2, err := agg.Mission(ctx, subOnly)
	if err != nil {
		t.Fatalf("Mission(subOnly): %v", err)
	}
	if len(got2.CostByCurrency) != 0 {
		t.Fatalf("CostByCurrency = %+v, want empty (all notional)", got2.CostByCurrency)
	}
	if got2.NotionalCostByCurrency["USD"] != 0.15 {
		t.Fatalf("NotionalCostByCurrency = %+v, want USD 0.15", got2.NotionalCostByCurrency)
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
