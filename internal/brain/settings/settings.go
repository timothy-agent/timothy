// Package settings holds brain's global feature switches: durable in
// one Postgres table, defaulting to enabled, cached briefly so the
// per-turn checks cost nothing.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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

// Typed value settings (runtime_settings table): strings where empty
// means the built-in default. These replaced the SESSION_TOKEN_BUDGET
// and SKILLS_ALLOWLIST env vars.
const (
	// ValueTokenBudget caps the projected context in tokens; empty
	// defers to the model window / built-in default.
	ValueTokenBudget = "session_token_budget"
	// ValueSkillsAllowlist restricts the agent to these skill packs
	// (comma-separated names); empty allows all loaded packs.
	ValueSkillsAllowlist = "skills_allowlist"
)

var knownValueKeys = map[string]bool{
	ValueTokenBudget: true, ValueSkillsAllowlist: true,
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

	valMu      sync.Mutex
	valCached  map[string]string
	valFetched time.Time
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

// Value returns one typed setting; empty string means "use the
// built-in default" (absent row, unknown key, or database outage).
func (s *Store) Value(ctx context.Context, key string) string {
	return s.AllValues(ctx)[key]
}

// TokenBudget parses the session token budget, falling back to def
// when unset or unparsable.
func (s *Store) TokenBudget(ctx context.Context, def int) int {
	if v := s.Value(ctx, ValueTokenBudget); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// SkillAllowed reports whether the allowlist admits a pack name;
// an empty allowlist admits everything.
func (s *Store) SkillAllowed(ctx context.Context, name string) bool {
	allow := s.Value(ctx, ValueSkillsAllowlist)
	if allow == "" {
		return true
	}
	for _, n := range strings.Split(allow, ",") {
		if strings.TrimSpace(n) == name {
			return true
		}
	}
	return false
}

// AllValues returns every known value setting; missing rows come back
// as empty strings so callers apply their own defaults.
func (s *Store) AllValues(ctx context.Context) map[string]string {
	s.valMu.Lock()
	defer s.valMu.Unlock()
	if s.valCached != nil && time.Since(s.valFetched) < cacheTTL {
		return s.valCached
	}

	out := map[string]string{}
	for k := range knownValueKeys {
		out[k] = ""
	}
	db, err := s.db.Get()
	if err != nil {
		s.log.Warn("runtime settings read degraded to defaults", "error", err)
		return out
	}
	rows, err := db.Query(ctx, `SELECT key, value FROM runtime_settings`)
	if err != nil {
		s.log.Warn("runtime settings read degraded to defaults", "error", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil && knownValueKeys[k] {
			out[k] = v
		}
	}
	s.valCached, s.valFetched = out, time.Now()
	return out
}

// SetValue stores one typed setting (empty clears back to default),
// audits the change, and invalidates the cache.
func (s *Store) SetValue(ctx context.Context, key, value string) error {
	if !knownValueKeys[key] {
		return fmt.Errorf("unknown setting %q", key)
	}
	value = strings.TrimSpace(value)
	if key == ValueTokenBudget && value != "" {
		if n, err := strconv.Atoi(value); err != nil || n <= 0 {
			return fmt.Errorf("%s must be a positive integer or empty", key)
		}
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	before := s.Value(ctx, key)
	if _, err := db.Exec(ctx, `INSERT INTO runtime_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(value)
	if _, err := db.Exec(ctx, `INSERT INTO admin_audit (action, entity, entity_id, before, after)
		VALUES ('update', 'setting', $1, $2, $3)`, key, b, a); err != nil {
		s.log.Warn("settings audit failed", "key", key, "error", err)
	}

	s.valMu.Lock()
	s.valCached = nil // next read refetches
	s.valMu.Unlock()
	return nil
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
