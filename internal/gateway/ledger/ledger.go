// Package ledger records one cost row per gateway request — success or
// failure. The dashboard, metrics, and spend alerts all derive from
// these rows.
package ledger

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

const writeTimeout = 5 * time.Second

// Entry is one request's accounting. ID may be pre-generated (NewID)
// so callers can reference the row before it is written; empty lets
// the database assign one.
type Entry struct {
	ID        string
	Provider  string
	Model     string
	Route     string
	Agent     string
	Purpose   string // optional: why the call happened (chat|distill|title|compaction|...)
	SessionID string // optional
	MissionID string // optional
	Usage     *stream.Usage
	LatencyMS int64
	Status    string // ok | error | incomplete
	ErrorCode string
	Cost      *float64
	// Currency is the provider's billing currency for Cost. Blank
	// defaults to USD at write time (all current providers bill USD).
	Currency string
	// Notional marks Cost as the CLI-reported API-equivalent price for
	// a subscription/oauth-billed delegated executor run — a real
	// figure, but not actual marginal spend.
	Notional bool
}

// Recorder is what the API layer depends on; tests supply an in-memory
// implementation.
type Recorder interface {
	Record(ctx context.Context, e Entry)
}

// Ledger writes entries to cost_ledger.
type Ledger struct {
	db  *pgpool.Pool
	log *slog.Logger
}

func New(db *pgpool.Pool, log *slog.Logger) *Ledger {
	return &Ledger{db: db, log: log}
}

// Record inserts the entry. Failures are logged, never propagated —
// accounting must not break serving (a degraded database already shows
// in /health).
func (l *Ledger) Record(ctx context.Context, e Entry) {
	db, err := l.db.Get()
	if err != nil {
		l.log.Warn("ledger write skipped", "error", err, "provider", e.Provider, "status", e.Status)
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()

	var in, out, cr, cw *int
	if e.Usage != nil {
		in, out, cr, cw = &e.Usage.InputTokens, &e.Usage.OutputTokens, &e.Usage.CacheReadTokens, &e.Usage.CacheWriteTokens
	}
	currency := e.Currency
	if currency == "" {
		currency = "USD"
	}
	_, err = db.Exec(wctx, `INSERT INTO cost_ledger
		(id, provider, model, route, agent, purpose, session_id, mission_id,
		 input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		 latency_ms, status, error_code, cost, currency, notional)
		VALUES (COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()),
		 $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), $9, $10, $11, $12, $13, $14, NULLIF($15, ''), $16, $17, $18)`,
		e.ID, e.Provider, e.Model, e.Route, e.Agent, e.Purpose, e.SessionID, e.MissionID,
		in, out, cr, cw, e.LatencyMS, e.Status, e.ErrorCode, e.Cost, currency, e.Notional)
	if err != nil {
		l.log.Warn("ledger write failed", "error", err, "provider", e.Provider, "status", e.Status)
	}
}

// LastSuccess returns the provider+model that last served this
// session successfully on this route — the stickiness signal that
// keeps a session on one provider's warm prompt cache (D-018).
//
// Sticky resets when the route is edited: a ledger row older than the
// route's own updated_at is ignored, so an operator's deliberate chain
// reorder or model swap (router.go's Resolve otherwise prefers this
// hint over the chain's written order) takes effect on the very next
// request instead of being overridden by a stale sticky pick. Losing
// prompt-cache affinity once per route edit is the accepted cost —
// relies on routes.updated_at being bumped on every chain-affecting
// PATCH (see internal/gateway/admin.go's route update paths).
func (l *Ledger) LastSuccess(ctx context.Context, sessionID, route string) (providerName, model string, ok bool) {
	db, err := l.db.Get()
	if err != nil {
		return "", "", false
	}
	err = db.QueryRow(ctx, `SELECT provider, model FROM cost_ledger
		WHERE session_id = $1 AND route = $2 AND status = 'ok'
		AND ts > COALESCE((SELECT updated_at FROM routes WHERE name = $2), '-infinity'::timestamptz)
		ORDER BY ts DESC LIMIT 1`, sessionID, route).Scan(&providerName, &model)
	if err != nil {
		return "", "", false
	}
	return providerName, model, true
}

// NewID returns a random UUIDv4 for pre-assigning ledger rows.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "" // database assigns instead
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Cost computes spend for usage under a price table, in the price
// table's billing currency. nil prices or nil usage mean unknown →
// nil, never guessed (D-013).
func Cost(prices *router.ModelPrices, u *stream.Usage) *float64 {
	if prices == nil || u == nil {
		return nil
	}
	const mtok = 1_000_000.0
	c := float64(u.InputTokens)*prices.InputPerMTok/mtok +
		float64(u.OutputTokens)*prices.OutputPerMTok/mtok +
		float64(u.CacheReadTokens)*prices.CacheReadPerMTok/mtok +
		float64(u.CacheWriteTokens)*prices.CacheWritePerMTok/mtok
	return &c
}
