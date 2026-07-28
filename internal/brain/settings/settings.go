// Package settings holds brain's global runtime configuration in one
// Postgres table (D-032): boolean feature switches and typed string
// knobs, cached briefly so per-turn checks cost nothing.
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

// Typed value settings: strings where empty means the built-in
// default. These replaced the SESSION_TOKEN_BUDGET and
// SKILLS_ALLOWLIST env vars.
const (
	// ValueTokenBudget caps the projected context in tokens; empty
	// defers to the model window / built-in default.
	ValueTokenBudget = "session_token_budget"
	// ValueSkillsAllowlist restricts the agent to these skill packs
	// (comma-separated names); empty allows all loaded packs.
	ValueSkillsAllowlist = "skills_allowlist"
	// ValueGitAuthorName/ValueGitAuthorEmail set the git identity mission
	// commits are authored under; empty falls back to the built-in
	// timothy/timothy@localhost identity.
	ValueGitAuthorName  = "git_author_name"
	ValueGitAuthorEmail = "git_author_email"
	// ValueSensitiveToolRoute names the route that sensitive-tool turns
	// (e.g. gmail_read) and their side-calls (memory extraction,
	// compaction) pin to; empty means the feature is off. Set from the
	// web settings panel.
	ValueSensitiveToolRoute = "sensitive_tool_route"
)

var knownValueKeys = map[string]bool{
	ValueTokenBudget: true, ValueSkillsAllowlist: true,
	ValueGitAuthorName: true, ValueGitAuthorEmail: true,
	ValueSensitiveToolRoute: true,
}

const cacheTTL = 10 * time.Second

// Store reads and writes settings. Reads serve from a short cache; a
// database outage degrades to defaults (switches enabled, values
// empty) — features failing closed because config storage hiccupped
// would be worse.
type Store struct {
	db  *pgpool.Pool
	log *slog.Logger

	mu      sync.Mutex
	flags   map[string]bool
	values  map[string]string
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
	flags, _ := s.load(ctx)
	return flags
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
	_, values := s.load(ctx)
	return values
}

// load returns both maps from one cached read of the settings table.
// Rows decode by their key's declared type; rows that fail to decode
// (or carry unknown keys) are ignored rather than fatal.
func (s *Store) load(ctx context.Context) (map[string]bool, map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flags != nil && time.Since(s.fetched) < cacheTTL {
		return s.flags, s.values
	}

	flags := map[string]bool{}
	for k := range knownKeys {
		flags[k] = true
	}
	values := map[string]string{}
	for k := range knownValueKeys {
		values[k] = ""
	}
	db, err := s.db.Get()
	if err != nil {
		s.log.Warn("settings read degraded to defaults", "error", err)
		return flags, values
	}
	rows, err := db.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		s.log.Warn("settings read degraded to defaults", "error", err)
		return flags, values
	}
	defer rows.Close()
	for rows.Next() {
		var (
			k   string
			raw json.RawMessage
		)
		if err := rows.Scan(&k, &raw); err != nil {
			continue
		}
		switch {
		case knownKeys[k]:
			var v bool
			if json.Unmarshal(raw, &v) == nil {
				flags[k] = v
			}
		case knownValueKeys[k]:
			var v string
			if json.Unmarshal(raw, &v) == nil {
				values[k] = v
			}
		}
	}
	s.flags, s.values, s.fetched = flags, values, time.Now()
	return flags, values
}

// Set flips one switch, audits the change, and invalidates the cache.
func (s *Store) Set(ctx context.Context, key string, value bool) error {
	if !knownKeys[key] {
		return fmt.Errorf("unknown setting %q", key)
	}
	return s.write(ctx, key, s.Enabled(ctx, key), value)
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
	return s.write(ctx, key, s.Value(ctx, key), value)
}

// write upserts one row (value marshaled to jsonb), audits before →
// after, and invalidates the cache.
func (s *Store) write(ctx context.Context, key string, before, value any) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, raw); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(value)
	if _, err := db.Exec(ctx, `INSERT INTO admin_audit (action, entity, entity_id, before, after)
		VALUES ('update', 'setting', $1, $2, $3)`, key, b, a); err != nil {
		s.log.Warn("settings audit failed", "key", key, "error", err)
	}

	s.mu.Lock()
	s.flags = nil // next read refetches
	s.mu.Unlock()
	return nil
}
