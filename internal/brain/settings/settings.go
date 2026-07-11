// Package settings holds brain's global feature switches: durable in
// one Postgres table, defaulting to enabled, cached briefly so the
// per-turn checks cost nothing.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Known switch keys. The scheduler switch stores now and gains a
// consumer with the harness phase.
const (
	KeyTools            = "tools_enabled"
	KeyMemoryExtraction = "memory_extraction_enabled"
	KeyCompaction       = "compaction_enabled"
	KeyScheduler        = "scheduler_enabled"
)

var knownKeys = map[string]bool{
	KeyTools: true, KeyMemoryExtraction: true, KeyCompaction: true, KeyScheduler: true,
}

const cacheTTL = 10 * time.Second

// Store reads and writes switches. Reads serve from a short cache;
// a database outage degrades to "everything enabled" — features
// failing closed because config storage hiccupped would be worse.
type Store struct {
	db  *pgpool.Pool
	log *slog.Logger

	mu      sync.Mutex
	cached  map[string]bool
	fetched time.Time
}

func New(db *pgpool.Pool, log *slog.Logger) *Store {
	return &Store{db: db, log: log}
}

// Enabled reports one switch, defaulting to true for absent rows and
// unknown keys.
func (s *Store) Enabled(ctx context.Context, key string) bool {
	all := s.All(ctx)
	if v, ok := all[key]; ok {
		return v
	}
	return true
}

// All returns every known switch with defaults applied.
func (s *Store) All(ctx context.Context) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && time.Since(s.fetched) < cacheTTL {
		return s.cached
	}

	out := map[string]bool{}
	for k := range knownKeys {
		out[k] = true
	}
	db, err := s.db.Get()
	if err != nil {
		s.log.Warn("settings read degraded to defaults", "error", err)
		return out
	}
	rows, err := db.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		s.log.Warn("settings read degraded to defaults", "error", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var (
			k string
			v bool
		)
		if err := rows.Scan(&k, &v); err == nil && knownKeys[k] {
			out[k] = v
		}
	}
	s.cached, s.fetched = out, time.Now()
	return out
}

// Set flips one switch, audits the change, and invalidates the cache.
func (s *Store) Set(ctx context.Context, key string, value bool) error {
	if !knownKeys[key] {
		return fmt.Errorf("unknown setting %q", key)
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	before := s.Enabled(ctx, key)
	if _, err := db.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(value)
	if _, err := db.Exec(ctx, `INSERT INTO admin_audit (action, entity, entity_id, before, after)
		VALUES ('update', 'setting', $1, $2, $3)`, key, b, a); err != nil {
		s.log.Warn("settings audit failed", "key", key, "error", err)
	}

	s.mu.Lock()
	s.cached = nil // next read refetches
	s.mu.Unlock()
	return nil
}
