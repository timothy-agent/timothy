package ledger

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Aggregator answers the dashboard's questions with SQL aggregation —
// raw ledger rows never leave the service (D-004 control plane reads
// chart-ready series). Test-connection traffic (purpose='test') is
// excluded everywhere: probing a provider must not pollute usage.
type Aggregator struct {
	db *pgpool.Pool
}

func NewAggregator(db *pgpool.Pool) *Aggregator {
	return &Aggregator{db: db}
}

// buckets whitelists the date_trunc argument — it is interpolated into
// SQL and must never come from the caller unchecked.
var buckets = map[string]string{"hour": "hour", "day": "day", "week": "week"}

// groups whitelists the series group column for the same reason.
var groups = map[string]string{
	"provider": "provider",
	"model":    "model",
	"route":    "route",
}

const notTest = `purpose IS DISTINCT FROM 'test'`

// Summary is the header-tile answer: totals for a range, in one
// currency. Cost excludes unbilled rows (subscription/oauth_token
// executor runs, D-051) via FILTER (WHERE NOT unbilled) — same as the
// mission aggregator — so it stays the range's true bill; UnbilledCost
// carries what that excluded spend would have cost at metered API
// prices, same currency as Cost, never folded into it. Unpriced fields
// count rows where cost is NULL (D-013: unknown price is recorded as
// NULL, never guessed) — without them, unpriced usage is invisible
// because SUM(cost) silently treats NULL as free. A range spanning more
// than one billing currency comes back as multiple Summary rows (one
// per currency) from SummaryByCurrency — summing across them would mix
// currencies, which this package never does.
type Summary struct {
	Currency             string  `json:"currency"`
	Cost                 float64 `json:"cost"`
	UnbilledCost         float64 `json:"unbilled_cost"`
	InputTokens          int64   `json:"input_tokens"`
	OutputTokens         int64   `json:"output_tokens"`
	CacheReadTokens      int64   `json:"cache_read_tokens"`
	CacheWriteTokens     int64   `json:"cache_write_tokens"`
	Requests             int64   `json:"requests"`
	Errors               int64   `json:"errors"`
	UnpricedRequests     int64   `json:"unpriced_requests"`
	UnpricedInputTokens  int64   `json:"unpriced_input_tokens"`
	UnpricedOutputTokens int64   `json:"unpriced_output_tokens"`
}

// SummaryByCurrency is Summary grouped by billing currency — one row
// per currency present in the range, never summed together.
func (a *Aggregator) SummaryByCurrency(ctx context.Context, from, to time.Time) ([]Summary, error) {
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("usage summary: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT currency,
			COALESCE(SUM(cost) FILTER (WHERE NOT unbilled), 0),
			COALESCE(SUM(cost) FILTER (WHERE unbilled), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0),
			COUNT(*), COUNT(*) FILTER (WHERE status = 'error'),
			COUNT(*) FILTER (WHERE cost IS NULL),
			COALESCE(SUM(input_tokens) FILTER (WHERE cost IS NULL), 0),
			COALESCE(SUM(output_tokens) FILTER (WHERE cost IS NULL), 0)
		FROM cost_ledger
		WHERE ts >= $1 AND ts < $2 AND `+notTest+`
		GROUP BY currency ORDER BY currency`, from, to)
	if err != nil {
		return nil, fmt.Errorf("usage summary: %w", err)
	}
	defer rows.Close()

	out := []Summary{}
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.Currency, &s.Cost, &s.UnbilledCost, &s.InputTokens, &s.OutputTokens,
			&s.CacheReadTokens, &s.CacheWriteTokens, &s.Requests, &s.Errors,
			&s.UnpricedRequests, &s.UnpricedInputTokens, &s.UnpricedOutputTokens); err != nil {
			return nil, fmt.Errorf("usage summary: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SeriesPoint is one (time bucket, group, currency) cell of a stacked
// chart. Cost excludes unbilled rows (FILTER (WHERE NOT unbilled), same
// as Summary); UnbilledCost carries that excluded spend's metered-price
// equivalent, same currency, never folded into Cost. Unpriced token sums
// (rows where cost is NULL) let the dashboard estimate what unpriced
// usage would cost from its advisory catalog — the estimate stays
// client-side, the server never guesses (D-013).
type SeriesPoint struct {
	Bucket               time.Time `json:"bucket"`
	Group                string    `json:"group"`
	Currency             string    `json:"currency"`
	Cost                 float64   `json:"cost"`
	UnbilledCost         float64   `json:"unbilled_cost"`
	InputTokens          int64     `json:"input_tokens"`
	OutputTokens         int64     `json:"output_tokens"`
	Requests             int64     `json:"requests"`
	Errors               int64     `json:"errors"`
	UnpricedInputTokens  int64     `json:"unpriced_input_tokens"`
	UnpricedOutputTokens int64     `json:"unpriced_output_tokens"`
}

// Series returns bucketed usage grouped by provider, model, or
// category — the shape every dashboard time chart consumes. Also
// grouped by currency so cost never sums across billing currencies.
func (a *Aggregator) Series(ctx context.Context, from, to time.Time, bucket, groupBy string) ([]SeriesPoint, error) {
	b, ok := buckets[bucket]
	if !ok {
		return nil, fmt.Errorf("usage series: unknown bucket %q", bucket)
	}
	col, ok := groups[groupBy]
	if !ok {
		return nil, fmt.Errorf("usage series: unknown group %q", groupBy)
	}
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("usage series: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT
			date_trunc('`+b+`', ts) AS bucket, `+col+`, currency,
			COALESCE(SUM(cost) FILTER (WHERE NOT unbilled), 0),
			COALESCE(SUM(cost) FILTER (WHERE unbilled), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COUNT(*), COUNT(*) FILTER (WHERE status = 'error'),
			COALESCE(SUM(input_tokens) FILTER (WHERE cost IS NULL), 0),
			COALESCE(SUM(output_tokens) FILTER (WHERE cost IS NULL), 0)
		FROM cost_ledger
		WHERE ts >= $1 AND ts < $2 AND `+notTest+`
		GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`, from, to)
	if err != nil {
		return nil, fmt.Errorf("usage series: %w", err)
	}
	defer rows.Close()

	out := []SeriesPoint{}
	for rows.Next() {
		var p SeriesPoint
		if err := rows.Scan(&p.Bucket, &p.Group, &p.Currency, &p.Cost, &p.UnbilledCost,
			&p.InputTokens, &p.OutputTokens, &p.Requests, &p.Errors,
			&p.UnpricedInputTokens, &p.UnpricedOutputTokens); err != nil {
			return nil, fmt.Errorf("usage series: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GroupTotal is one group's totals over a whole range — the
// non-time-bucketed sibling of SeriesPoint, for tables/charts that
// rank groups rather than plot them over time. Also split by currency
// so a group's cost is never summed across billing currencies.
type GroupTotal struct {
	Group                string  `json:"group"`
	Currency             string  `json:"currency"`
	Cost                 float64 `json:"cost"`
	InputTokens          int64   `json:"input_tokens"`
	OutputTokens         int64   `json:"output_tokens"`
	Requests             int64   `json:"requests"`
	UnpricedInputTokens  int64   `json:"unpriced_input_tokens"`
	UnpricedOutputTokens int64   `json:"unpriced_output_tokens"`
}

// Totals returns one row per group (and currency) summed over the
// whole range — Series without the time bucket, for tables/rankings
// rather than time-series charts. Cost excludes unbilled rows (FILTER
// (WHERE NOT unbilled), same as Summary/Series) so a group's ranking
// reflects real spend only.
func (a *Aggregator) Totals(ctx context.Context, from, to time.Time, groupBy string) ([]GroupTotal, error) {
	col, ok := groups[groupBy]
	if !ok {
		return nil, fmt.Errorf("usage totals: unknown group %q", groupBy)
	}
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("usage totals: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+col+`, currency,
			COALESCE(SUM(cost) FILTER (WHERE NOT unbilled), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COUNT(*),
			COALESCE(SUM(input_tokens) FILTER (WHERE cost IS NULL), 0),
			COALESCE(SUM(output_tokens) FILTER (WHERE cost IS NULL), 0)
		FROM cost_ledger
		WHERE ts >= $1 AND ts < $2 AND `+notTest+`
		GROUP BY 1, 2 ORDER BY 3 DESC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("usage totals: %w", err)
	}
	defer rows.Close()

	out := []GroupTotal{}
	for rows.Next() {
		var g GroupTotal
		if err := rows.Scan(&g.Group, &g.Currency, &g.Cost, &g.InputTokens, &g.OutputTokens,
			&g.Requests, &g.UnpricedInputTokens, &g.UnpricedOutputTokens); err != nil {
			return nil, fmt.Errorf("usage totals: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UnpricedGroup is one (provider, model) pair's unpriced token totals
// over a range — rows where cost is NULL (D-013: unknown price is
// recorded as NULL, never guessed), the pairs the dashboard's advisory
// catalog estimate needs to price. Grouping by provider alongside model
// matters: the catalog match must stay scoped to the provider that
// actually served the tokens (CatalogPrices), never matched against the
// whole catalog where another vendor's model of the same name could
// carry a different price.
type UnpricedGroup struct {
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	UnpricedInputTokens  int64  `json:"unpriced_input_tokens"`
	UnpricedOutputTokens int64  `json:"unpriced_output_tokens"`
}

// UnpricedByProviderModel returns one row per (provider, model) pair
// that had unpriced usage (cost IS NULL) in range.
func (a *Aggregator) UnpricedByProviderModel(ctx context.Context, from, to time.Time) ([]UnpricedGroup, error) {
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("usage unpriced: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT provider, model,
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
		FROM cost_ledger
		WHERE ts >= $1 AND ts < $2 AND cost IS NULL AND `+notTest+`
		GROUP BY provider, model ORDER BY provider, model`, from, to)
	if err != nil {
		return nil, fmt.Errorf("usage unpriced: %w", err)
	}
	defer rows.Close()

	out := []UnpricedGroup{}
	for rows.Next() {
		var g UnpricedGroup
		if err := rows.Scan(&g.Provider, &g.Model, &g.UnpricedInputTokens, &g.UnpricedOutputTokens); err != nil {
			return nil, fmt.Errorf("usage unpriced: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// MissionUsage totals one mission's ledger footprint — every turn the
// missions engine ran for it, across all its sessions. Cost is broken
// out per currency (CostByCurrency) rather than summed into one
// number: a mission that switched providers mid-run can carry spend in
// more than one billing currency, and this package never sums across
// currencies. CostByCurrency is billed spend only (unbilled excluded)
// and equals BilledBrainByCurrency + BilledHarnessByCurrency;
// UnbilledCostByCurrency holds the API-equivalent price of rows billed
// through a subscription/oauth_token instead (D-051's delegated CLI
// executor) — a mission's true bill is CostByCurrency alone.
// BilledHarnessByCurrency is the delegated CLI executor's own billed
// rows (purpose='executor'); BilledBrainByCurrency is every other
// billed turn the missions engine ran (explore/plan/worker/review).
type MissionUsage struct {
	MissionID               string             `json:"mission_id"`
	CostByCurrency          map[string]float64 `json:"cost_by_currency"`
	BilledBrainByCurrency   map[string]float64 `json:"billed_brain_by_currency"`
	BilledHarnessByCurrency map[string]float64 `json:"billed_harness_by_currency"`
	UnbilledCostByCurrency  map[string]float64 `json:"unbilled_cost_by_currency"`
	InputTokens             int64              `json:"input_tokens"`
	OutputTokens            int64              `json:"output_tokens"`
	Requests                int64              `json:"requests"`
	UnpricedRequests        int64              `json:"unpriced_requests"`
	Models                  []ModelUsed        `json:"models"`
}

// ModelUsed is one provider/model/harness-ness triple actually invoked
// for a mission — a route is a named fallback chain, not a single
// model, so this is the only honest answer to "which model ran this,"
// and it can be more than one entry if the chain fell back mid-mission.
// Harness is true when the group's rows are the delegated CLI
// executor's own (purpose='executor', D-051) rather than the missions
// engine's direct calls — a model used by both sides yields two rows,
// so callers can tell the harness's model apart from brain's.
type ModelUsed struct {
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Harness  bool      `json:"harness"`
	Requests int64     `json:"requests"`
	LastUsed time.Time `json:"last_used"`
}

func (a *Aggregator) Mission(ctx context.Context, missionID string) (MissionUsage, error) {
	db, err := a.db.Get()
	if err != nil {
		return MissionUsage{}, fmt.Errorf("usage mission: %w", err)
	}
	m := MissionUsage{
		MissionID:               missionID,
		CostByCurrency:          map[string]float64{},
		BilledBrainByCurrency:   map[string]float64{},
		BilledHarnessByCurrency: map[string]float64{},
		UnbilledCostByCurrency:  map[string]float64{},
	}
	err = db.QueryRow(ctx, `SELECT
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COUNT(*), COUNT(*) FILTER (WHERE cost IS NULL)
		FROM cost_ledger
		WHERE mission_id = $1 AND `+notTest, missionID).
		Scan(&m.InputTokens, &m.OutputTokens, &m.Requests, &m.UnpricedRequests)
	if err != nil {
		return MissionUsage{}, fmt.Errorf("usage mission: %w", err)
	}
	// brain = every billed turn the missions engine ran directly
	// (explore/plan/worker/review); harness = the delegated CLI
	// executor's own billed rows (purpose='executor', D-051).
	costRows, err := db.Query(ctx, `SELECT currency,
			COALESCE(SUM(cost) FILTER (WHERE NOT unbilled), 0),
			COALESCE(SUM(cost) FILTER (WHERE NOT unbilled AND purpose = 'executor'), 0),
			COALESCE(SUM(cost) FILTER (WHERE NOT unbilled AND purpose IS DISTINCT FROM 'executor'), 0),
			COALESCE(SUM(cost) FILTER (WHERE unbilled), 0)
		FROM cost_ledger
		WHERE mission_id = $1 AND `+notTest+`
		GROUP BY currency`, missionID)
	if err != nil {
		return MissionUsage{}, fmt.Errorf("usage mission: cost by currency: %w", err)
	}
	for costRows.Next() {
		var currency string
		var billed, harness, brain, unbilled float64
		if err := costRows.Scan(&currency, &billed, &harness, &brain, &unbilled); err != nil {
			costRows.Close()
			return MissionUsage{}, fmt.Errorf("usage mission: cost by currency: %w", err)
		}
		if billed != 0 {
			m.CostByCurrency[currency] = billed
		}
		if brain != 0 {
			m.BilledBrainByCurrency[currency] = brain
		}
		if harness != 0 {
			m.BilledHarnessByCurrency[currency] = harness
		}
		if unbilled != 0 {
			m.UnbilledCostByCurrency[currency] = unbilled
		}
	}
	if err := costRows.Err(); err != nil {
		costRows.Close()
		return MissionUsage{}, fmt.Errorf("usage mission: cost by currency: %w", err)
	}
	costRows.Close()
	models, err := a.missionModels(ctx, db, missionID)
	if err != nil {
		return MissionUsage{}, err
	}
	m.Models = models
	return m, nil
}

func (a *Aggregator) missionModels(ctx context.Context, db *pgxpool.Pool, missionID string) ([]ModelUsed, error) {
	rows, err := db.Query(ctx, `SELECT provider, model, purpose IS NOT DISTINCT FROM 'executor' AS harness, COUNT(*), MAX(ts)
		FROM cost_ledger
		WHERE mission_id = $1 AND `+notTest+`
		GROUP BY provider, model, harness ORDER BY MAX(ts) DESC`, missionID)
	if err != nil {
		return nil, fmt.Errorf("usage mission: models: %w", err)
	}
	defer rows.Close()

	out := []ModelUsed{}
	for rows.Next() {
		var mu ModelUsed
		if err := rows.Scan(&mu.Provider, &mu.Model, &mu.Harness, &mu.Requests, &mu.LastUsed); err != nil {
			return nil, fmt.Errorf("usage mission: models: %w", err)
		}
		out = append(out, mu)
	}
	return out, rows.Err()
}

// TopModelByMission answers the mission list's "which model served
// this" column for a page of missions in one query: per mission_id,
// the provider/model with the most requests (ties broken by most
// recent). Unlike missionModels above (one mission's full model mix,
// ranked by recency), this ranks by request count and returns only the
// winner, batched across many missions — the list view's cheaper
// question. A mission with no ledger rows is simply absent from the
// result map.
//
// A delegated mission has many native brain rows (explore/plan/verify/
// review/title) but only one executor row (purpose='executor', the
// harness CLI run) per attempt, so count alone always favors the
// native model. Any successful executor row (status='ok') outranks
// count entirely — that's the model that actually did the work — with
// ties among executor rows broken by recency. A failed executor row
// (fallback to native) never gets this priority, so a mission whose
// harness spawn failed still shows the native model.
func (a *Aggregator) TopModelByMission(ctx context.Context, missionIDs []string) (map[string]ModelUsed, error) {
	out := map[string]ModelUsed{}
	if len(missionIDs) == 0 {
		return out, nil
	}
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("top model by mission: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT DISTINCT ON (mission_id) mission_id, provider, model, requests, last_used FROM (
			SELECT mission_id, provider, model, COUNT(*) AS requests, MAX(ts) AS last_used,
				BOOL_OR(purpose IS NOT DISTINCT FROM 'executor' AND status = 'ok') AS ok_executor
			FROM cost_ledger
			WHERE mission_id = ANY($1) AND `+notTest+`
			GROUP BY mission_id, provider, model
		) ranked
		ORDER BY mission_id, ok_executor DESC, requests DESC, last_used DESC`, missionIDs)
	if err != nil {
		return nil, fmt.Errorf("top model by mission: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var missionID string
		var mu ModelUsed
		if err := rows.Scan(&missionID, &mu.Provider, &mu.Model, &mu.Requests, &mu.LastUsed); err != nil {
			return nil, fmt.Errorf("top model by mission: %w", err)
		}
		out[missionID] = mu
	}
	return out, rows.Err()
}

// SessionUsage ranks sessions by spend for the top-N table. Grouped by
// currency as well as session: a session's rows are ranked by cost
// within their own currency, never summed against a different one.
type SessionUsage struct {
	SessionID    string  `json:"session_id"`
	Currency     string  `json:"currency"`
	Cost         float64 `json:"cost"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Requests     int64   `json:"requests"`
}

// TopSessions ranks sessions by real spend only — Cost excludes
// unbilled rows (FILTER (WHERE NOT unbilled), same as
// Summary/Series/Totals), so a subscription-billed executor run never
// inflates a session into the top-spend table.
func (a *Aggregator) TopSessions(ctx context.Context, from, to time.Time, limit int) ([]SessionUsage, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("usage sessions: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT session_id, currency,
			COALESCE(SUM(cost) FILTER (WHERE NOT unbilled), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COUNT(*)
		FROM cost_ledger
		WHERE ts >= $1 AND ts < $2 AND session_id IS NOT NULL AND `+notTest+`
		GROUP BY session_id, currency ORDER BY 3 DESC LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("usage sessions: %w", err)
	}
	defer rows.Close()

	out := []SessionUsage{}
	for rows.Next() {
		var s SessionUsage
		if err := rows.Scan(&s.SessionID, &s.Currency, &s.Cost, &s.InputTokens, &s.OutputTokens, &s.Requests); err != nil {
			return nil, fmt.Errorf("usage sessions: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// LatencyRow is a provider's latency profile. Only status='ok' rows
// count: errors would flatter percentiles with fast failures, and
// incomplete (truncated) streams skew them the other way.
type LatencyRow struct {
	Provider string  `json:"provider"`
	P50      float64 `json:"p50_ms"`
	P95      float64 `json:"p95_ms"`
	P99      float64 `json:"p99_ms"`
	Requests int64   `json:"requests"`
}

func (a *Aggregator) Latency(ctx context.Context, from, to time.Time) ([]LatencyRow, error) {
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("usage latency: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT provider,
			percentile_cont(0.50) WITHIN GROUP (ORDER BY latency_ms),
			percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms),
			percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms),
			COUNT(*)
		FROM cost_ledger
		WHERE ts >= $1 AND ts < $2 AND status = 'ok' AND `+notTest+`
		GROUP BY provider ORDER BY provider`, from, to)
	if err != nil {
		return nil, fmt.Errorf("usage latency: %w", err)
	}
	defer rows.Close()

	out := []LatencyRow{}
	for rows.Next() {
		var l LatencyRow
		if err := rows.Scan(&l.Provider, &l.P50, &l.P95, &l.P99, &l.Requests); err != nil {
			return nil, fmt.Errorf("usage latency: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CacheRow reports how much of a provider's input arrived from cache.
type CacheRow struct {
	Provider        string  `json:"provider"`
	CacheReadTokens int64   `json:"cache_read_tokens"`
	InputTokens     int64   `json:"input_tokens"`
	HitRatio        float64 `json:"hit_ratio"`
}

func (a *Aggregator) Cache(ctx context.Context, from, to time.Time) ([]CacheRow, error) {
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("usage cache: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT provider,
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(input_tokens), 0)
		FROM cost_ledger
		WHERE ts >= $1 AND ts < $2 AND `+notTest+`
		GROUP BY provider ORDER BY provider`, from, to)
	if err != nil {
		return nil, fmt.Errorf("usage cache: %w", err)
	}
	defer rows.Close()

	out := []CacheRow{}
	for rows.Next() {
		var c CacheRow
		if err := rows.Scan(&c.Provider, &c.CacheReadTokens, &c.InputTokens); err != nil {
			return nil, fmt.Errorf("usage cache: %w", err)
		}
		if total := c.CacheReadTokens + c.InputTokens; total > 0 {
			c.HitRatio = float64(c.CacheReadTokens) / float64(total)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
