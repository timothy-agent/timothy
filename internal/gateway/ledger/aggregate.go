package ledger

import (
	"context"
	"fmt"
	"time"

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
	"route": "route",
}

const notTest = `purpose IS DISTINCT FROM 'test'`

// Summary is the header-tile answer: totals for a range. Unpriced
// fields count rows where cost_usd is NULL (D-013: unknown price is
// recorded as NULL, never guessed) — without them, unpriced usage is
// invisible because SUM(cost_usd) silently treats NULL as free.
type Summary struct {
	CostUSD              float64 `json:"cost_usd"`
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

func (a *Aggregator) Summary(ctx context.Context, from, to time.Time) (Summary, error) {
	db, err := a.db.Get()
	if err != nil {
		return Summary{}, fmt.Errorf("usage summary: %w", err)
	}
	var s Summary
	err = db.QueryRow(ctx, `SELECT
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0),
			COUNT(*), COUNT(*) FILTER (WHERE status = 'error'),
			COUNT(*) FILTER (WHERE cost_usd IS NULL),
			COALESCE(SUM(input_tokens) FILTER (WHERE cost_usd IS NULL), 0),
			COALESCE(SUM(output_tokens) FILTER (WHERE cost_usd IS NULL), 0)
		FROM cost_ledger
		WHERE ts >= $1 AND ts < $2 AND `+notTest,
		from, to).Scan(&s.CostUSD, &s.InputTokens, &s.OutputTokens,
		&s.CacheReadTokens, &s.CacheWriteTokens, &s.Requests, &s.Errors,
		&s.UnpricedRequests, &s.UnpricedInputTokens, &s.UnpricedOutputTokens)
	if err != nil {
		return Summary{}, fmt.Errorf("usage summary: %w", err)
	}
	return s, nil
}

// SeriesPoint is one (time bucket, group) cell of a stacked chart.
// Unpriced token sums (rows where cost_usd is NULL) let the dashboard
// estimate what unpriced usage would cost from its advisory catalog —
// the estimate stays client-side, the server never guesses (D-013).
type SeriesPoint struct {
	Bucket               time.Time `json:"bucket"`
	Group                string    `json:"group"`
	CostUSD              float64   `json:"cost_usd"`
	InputTokens          int64     `json:"input_tokens"`
	OutputTokens         int64     `json:"output_tokens"`
	Requests             int64     `json:"requests"`
	Errors               int64     `json:"errors"`
	UnpricedInputTokens  int64     `json:"unpriced_input_tokens"`
	UnpricedOutputTokens int64     `json:"unpriced_output_tokens"`
}

// Series returns bucketed usage grouped by provider, model, or
// category — the shape every dashboard time chart consumes.
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
			date_trunc('`+b+`', ts) AS bucket, `+col+`,
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COUNT(*), COUNT(*) FILTER (WHERE status = 'error'),
			COALESCE(SUM(input_tokens) FILTER (WHERE cost_usd IS NULL), 0),
			COALESCE(SUM(output_tokens) FILTER (WHERE cost_usd IS NULL), 0)
		FROM cost_ledger
		WHERE ts >= $1 AND ts < $2 AND `+notTest+`
		GROUP BY 1, 2 ORDER BY 1, 2`, from, to)
	if err != nil {
		return nil, fmt.Errorf("usage series: %w", err)
	}
	defer rows.Close()

	out := []SeriesPoint{}
	for rows.Next() {
		var p SeriesPoint
		if err := rows.Scan(&p.Bucket, &p.Group, &p.CostUSD,
			&p.InputTokens, &p.OutputTokens, &p.Requests, &p.Errors,
			&p.UnpricedInputTokens, &p.UnpricedOutputTokens); err != nil {
			return nil, fmt.Errorf("usage series: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MissionUsage totals one mission's ledger footprint — every turn the
// missions engine ran for it, across all its sessions.
type MissionUsage struct {
	MissionID        string  `json:"mission_id"`
	CostUSD          float64 `json:"cost_usd"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	Requests         int64   `json:"requests"`
	UnpricedRequests int64   `json:"unpriced_requests"`
}

func (a *Aggregator) Mission(ctx context.Context, missionID string) (MissionUsage, error) {
	db, err := a.db.Get()
	if err != nil {
		return MissionUsage{}, fmt.Errorf("usage mission: %w", err)
	}
	m := MissionUsage{MissionID: missionID}
	err = db.QueryRow(ctx, `SELECT
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COUNT(*), COUNT(*) FILTER (WHERE cost_usd IS NULL)
		FROM cost_ledger
		WHERE mission_id = $1 AND `+notTest, missionID).
		Scan(&m.CostUSD, &m.InputTokens, &m.OutputTokens, &m.Requests, &m.UnpricedRequests)
	if err != nil {
		return MissionUsage{}, fmt.Errorf("usage mission: %w", err)
	}
	return m, nil
}

// SessionUsage ranks sessions by spend for the top-N table.
type SessionUsage struct {
	SessionID    string  `json:"session_id"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Requests     int64   `json:"requests"`
}

func (a *Aggregator) TopSessions(ctx context.Context, from, to time.Time, limit int) ([]SessionUsage, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("usage sessions: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT session_id,
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COUNT(*)
		FROM cost_ledger
		WHERE ts >= $1 AND ts < $2 AND session_id IS NOT NULL AND `+notTest+`
		GROUP BY session_id ORDER BY 2 DESC LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("usage sessions: %w", err)
	}
	defer rows.Close()

	out := []SessionUsage{}
	for rows.Next() {
		var s SessionUsage
		if err := rows.Scan(&s.SessionID, &s.CostUSD, &s.InputTokens, &s.OutputTokens, &s.Requests); err != nil {
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
