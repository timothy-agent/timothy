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
	log    *slog.Logger
	snap   atomic.Pointer[Snapshot]
}

// NewStore wires a store; call Run to start loading.
func NewStore(db *pgpool.Pool, lookup func(string) string, log *slog.Logger) *Store {
	return &Store{db: db, lookup: lookup, log: log}
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
		models, credential_ref, headers, enabled FROM providers`)
	if err != nil {
		return fmt.Errorf("router: query providers: %w", err)
	}
	defer rows.Close()

	var provRows []ProviderRow
	for rows.Next() {
		var (
			row         ProviderRow
			modelsJSON  []byte
			headersJSON []byte
		)
		if err := rows.Scan(&row.ID, &row.Name, &row.Kind, &row.Driver, &row.BaseURL,
			&row.DefaultModel, &modelsJSON, &row.CredentialRef, &headersJSON, &row.Enabled); err != nil {
			return fmt.Errorf("router: scan provider: %w", err)
		}
		if err := json.Unmarshal(modelsJSON, &row.Models); err != nil {
			return fmt.Errorf("router: provider %s models: %w", row.Name, err)
		}
		if err := json.Unmarshal(headersJSON, &row.Headers); err != nil {
			return fmt.Errorf("router: provider %s headers: %w", row.Name, err)
		}
		provRows = append(provRows, row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("router: providers rows: %w", err)
	}

	routeRows, err := tx.Query(ctx, `SELECT task_category, chain, enabled FROM task_routes`)
	if err != nil {
		return fmt.Errorf("router: query routes: %w", err)
	}
	defer routeRows.Close()

	var routes []RouteRow
	for routeRows.Next() {
		var (
			row       RouteRow
			chainJSON []byte
		)
		if err := routeRows.Scan(&row.TaskCategory, &chainJSON, &row.Enabled); err != nil {
			return fmt.Errorf("router: scan route: %w", err)
		}
		if err := json.Unmarshal(chainJSON, &row.Chain); err != nil {
			return fmt.Errorf("router: route %s chain: %w", row.TaskCategory, err)
		}
		routes = append(routes, row)
	}
	if err := routeRows.Err(); err != nil {
		return fmt.Errorf("router: route rows: %w", err)
	}

	snap, err := BuildSnapshot(provRows, routes, s.lookup)
	if err != nil {
		return fmt.Errorf("router: build snapshot: %w", err)
	}
	s.snap.Store(snap)
	s.log.Info("routing config loaded", "providers", len(provRows), "routes", len(routes))
	return nil
}
