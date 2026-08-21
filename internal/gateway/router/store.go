package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

const (
	pollInterval  = 30 * time.Second
	loadRetryWait = 5 * time.Second
	loadTimeout   = 10 * time.Second
)

// Store loads routing configuration from Postgres and serves immutable
// snapshots. Reload paths: initial load with retry, a 30s poll, and an
// explicit trigger (POST /internal/reload).
type Store struct {
	db     *pgpool.Pool
	lookup func(string) string
	cat    catalogLookup
	log    *slog.Logger
	snap   atomic.Pointer[Snapshot]
}

// NewStore wires a store; call Run to start loading. cat is the model
// catalog cache attemptCapable/Prices consult in place of the removed
// providers.models column — nil is safe (capability gating and
// pricing both fall back permissively/unpriced).
func NewStore(db *pgpool.Pool, lookup func(string) string, cat catalogLookup, log *slog.Logger) *Store {
	return &Store{db: db, lookup: lookup, cat: cat, log: log}
}

// Snapshot returns the current snapshot, nil before the first
// successful load (callers answer 503).
func (s *Store) Snapshot() *Snapshot {
	return s.snap.Load()
}

// Run loads until first success, then polls. It returns when ctx ends.
func (s *Store) Run(ctx context.Context) {
	for s.Snapshot() == nil {
		if err := s.Load(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.log.Warn("config load failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(loadRetryWait):
			}
			continue
		}
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Load(ctx); err != nil && ctx.Err() == nil {
				// Keep serving the last good snapshot.
				s.log.Warn("config reload failed", "error", err)
			}
		}
	}
}

// Load reads both tables and atomically swaps in a fresh snapshot.
// Both reads run in one REPEATABLE READ transaction so a concurrent
// admin edit cannot produce a snapshot whose routes reference provider
// rows it never saw.
func (s *Store) Load(ctx context.Context) error {
	db, err := s.db.Get()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("router: begin load tx: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rows, err := tx.Query(ctx, `SELECT id, name, kind, driver, base_url, default_model,
		credential_ref, headers, options, enabled FROM providers`)
	if err != nil {
		return fmt.Errorf("router: query providers: %w", err)
	}
	defer rows.Close()

	var provRows []ProviderRow
	for rows.Next() {
		var (
			row         ProviderRow
			headersJSON []byte
			optionsJSON []byte
		)
		if err := rows.Scan(&row.ID, &row.Name, &row.Kind, &row.Driver, &row.BaseURL,
			&row.DefaultModel, &row.CredentialRef, &headersJSON, &optionsJSON, &row.Enabled); err != nil {
			return fmt.Errorf("router: scan provider: %w", err)
		}
		if err := json.Unmarshal(headersJSON, &row.Headers); err != nil {
			return fmt.Errorf("router: provider %s headers: %w", row.Name, err)
		}
		if err := applyProviderOptions(&row, optionsJSON); err != nil {
			return fmt.Errorf("router: provider %s options: %w", row.Name, err)
		}
		provRows = append(provRows, row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("router: providers rows: %w", err)
	}

	routeRows, err := tx.Query(ctx, `SELECT name, chain, strategy, enabled, capability, role FROM routes`)
	if err != nil {
		return fmt.Errorf("router: query routes: %w", err)
	}
	defer routeRows.Close()

	var routes []RouteRow
	for routeRows.Next() {
		var (
			row       RouteRow
			chainJSON []byte
			role      *string
		)
		if err := routeRows.Scan(&row.Name, &chainJSON, &row.Strategy, &row.Enabled, &row.Capability, &role); err != nil {
			return fmt.Errorf("router: scan route: %w", err)
		}
		if err := json.Unmarshal(chainJSON, &row.Chain); err != nil {
			return fmt.Errorf("router: route %s chain: %w", row.Name, err)
		}
		if role != nil {
			row.Role = *role
		}
		routes = append(routes, row)
	}
	if err := routeRows.Err(); err != nil {
		return fmt.Errorf("router: route rows: %w", err)
	}

	snap, warnings := BuildSnapshot(provRows, routes, s.lookup, s.cat)
	for _, w := range warnings {
		s.log.Warn("router: provider failed to build; marked unhealthy", "provider", w.Provider, "error", w.Err)
	}
	// Ledger stats feed scored strategies; a failure only degrades
	// scoring to declared prices, never the snapshot itself.
	if stats, err := loadStats(ctx, tx); err != nil {
		s.log.Warn("router: ledger stats unavailable; scored strategies use prices only", "error", err)
	} else {
		snap.SetStats(stats)
	}
	s.snap.Store(snap)
	s.log.Info("routing config loaded", "providers", len(provRows), "routes", len(routes))
	return nil
}

// applyProviderOptions decodes a providers row's options jsonb into row.
// Only reasoning_effort, request_timeout, region, anthropic_base_url,
// and openai_responses are recognized today (D-040, D-041, D-048,
// D-051); unknown keys are ignored silently. An unparseable
// request_timeout fails the load outright — config honesty, never a
// silent fallback to the driver default. region gets no such
// validation beyond being present: AWS region ids change over time, so
// the gateway never hardcodes a valid set — an unknown region simply
// fails at the AWS SDK when used. openai_responses is tri-state
// ("true"/"false"/absent); absent leaves row.OpenAIResponses nil
// (unknown), anything else fails the load — Admin.Test/Patch are the
// only writers and only ever write those two literals.
func applyProviderOptions(row *ProviderRow, optionsJSON []byte) error {
	var opts struct {
		ReasoningEffort  string `json:"reasoning_effort"`
		RequestTimeout   string `json:"request_timeout"`
		Region           string `json:"region"`
		AnthropicBaseURL string `json:"anthropic_base_url"`
		OpenAIResponses  string `json:"openai_responses"`
		LitellmProvider  string `json:"litellm_provider"`
	}
	if err := json.Unmarshal(optionsJSON, &opts); err != nil {
		return err
	}
	row.ReasoningEffort = opts.ReasoningEffort
	row.Region = opts.Region
	row.AnthropicBaseURL = opts.AnthropicBaseURL
	row.LitellmProvider = opts.LitellmProvider
	if opts.RequestTimeout != "" {
		d, err := time.ParseDuration(opts.RequestTimeout)
		if err != nil {
			return fmt.Errorf("request_timeout %q: %w", opts.RequestTimeout, err)
		}
		row.Timeout = d
	}
	switch opts.OpenAIResponses {
	case "":
		// absent = unknown, row.OpenAIResponses stays nil.
	case "true":
		v := true
		row.OpenAIResponses = &v
	case "false":
		v := false
		row.OpenAIResponses = &v
	default:
		return fmt.Errorf("openai_responses %q must be \"true\" or \"false\"", opts.OpenAIResponses)
	}
	return nil
}

// loadStats aggregates the last hour of the cost ledger per
// provider+model with exponential time decay (τ = 30 min), so scored
// strategies react to recent reality without whiplashing on one bad
// request. Test probes are excluded — a connection test is not
// serving traffic. Executor runs (purpose='executor') are excluded
// too — those are whole CLI-harness invocations booked under the same
// provider/model as gateway traffic, with latency spanning minutes and
// near-zero tps, not comparable to a served chat request. Latency and
// tps average only over status='ok' rows — an error row's weight
// would otherwise inflate/deflate the average without ever landing in
// its denominator; uptime alone counts every row, ok or not.
func loadStats(ctx context.Context, tx pgx.Tx) (map[string]ModelStats, error) {
	rows, err := tx.Query(ctx, `
		SELECT provider, model,
		       COALESCE(SUM(CASE WHEN status = 'ok' THEN w END) / NULLIF(SUM(w), 0), 0),
		       COALESCE(SUM(CASE WHEN status = 'ok' THEN w * latency_ms END) / NULLIF(SUM(CASE WHEN status = 'ok' THEN w END), 0), 0),
		       COALESCE(SUM(CASE WHEN status = 'ok' THEN w * tps END) / NULLIF(SUM(CASE WHEN status = 'ok' THEN w END), 0), 0)
		FROM (
			SELECT provider, model, status, latency_ms,
			       COALESCE(output_tokens, 0) * 1000.0 / GREATEST(latency_ms, 1) AS tps,
			       EXP(-EXTRACT(EPOCH FROM (now() - ts)) / 1800.0) AS w
			FROM cost_ledger
			WHERE ts > now() - interval '60 minutes'
			  AND purpose IS DISTINCT FROM 'test'
			  AND purpose IS DISTINCT FROM 'executor'
		) recent
		GROUP BY provider, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ModelStats{}
	for rows.Next() {
		var (
			prov, model string
			st          ModelStats
		)
		if err := rows.Scan(&prov, &model, &st.Uptime, &st.LatencyMS, &st.TokensPerS); err != nil {
			return nil, err
		}
		out[prov+"/"+model] = st
	}
	return out, rows.Err()
}
