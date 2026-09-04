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

	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/missions/executor"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Known switch keys. The scheduler switch stores now and gains a
// consumer with the harness phase.
const (
	KeyTools            = "tools_enabled"
	KeyMemoryExtraction = "memory_extraction_enabled"
	KeyCompaction       = "compaction_enabled"
	KeyScheduler        = "scheduler_enabled"
	// KeyKBImageCaptioning gates spending on vision-model calls to
	// caption images at KB ingest (issues #349/#350); unlike every other
	// switch here it defaults OFF for an absent row (knownKeysOff), since
	// enabling it means real gateway spend the operator must opt into.
	KeyKBImageCaptioning = "kb_image_captioning_enabled"
)

var knownKeys = map[string]bool{
	KeyTools: true, KeyMemoryExtraction: true, KeyCompaction: true, KeyScheduler: true,
	KeyKBImageCaptioning: true,
}

// knownKeysOff lists switches from knownKeys whose absent-row default is
// false instead of the package's normal true (a database outage still
// degrades this key to false, same fail-closed-on-spend reasoning).
var knownKeysOff = map[string]bool{
	KeyKBImageCaptioning: true,
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
	// ValueSensitiveToolRoute names the route that turns using a
	// connector marked sensitive (e.g. gmail) and their side-calls
	// (memory extraction, compaction) pin to; empty means the feature is
	// off. Set from the web settings panel.
	ValueSensitiveToolRoute = "sensitive_tool_route"
	// ValueDefaultCurrency is the ISO 4217 code new budgets/missions
	// default to when the caller doesn't specify one; empty defers to
	// "USD" via DefaultCurrency below.
	ValueDefaultCurrency = "default_currency"
	// ValueCodingExecutor names the harness a coding mission's create
	// request defaults to when it doesn't specify one (D-051); "" (the
	// default) means native. Never applies to kind=general missions.
	ValueCodingExecutor = "coding_executor"
	// ValueGitBranchPattern is the default branch-name template a coding
	// mission's Provision expands (missions/branchtemplate.go); ""
	// defers to missions.DefaultBranchPattern ("{type}/{slug}"), the
	// original behavior. A mission's own branch_pattern column
	// overrides this per create request.
	ValueGitBranchPattern = "git_branch_pattern"
	// ValueGitCommitStyle is the default commit-message style
	// ("conventional" or "plain") a mission's unit commits use; "" defers
	// to missions.CommitStyleConventional, the original behavior. A
	// mission's own commit_style column overrides this per create
	// request.
	ValueGitCommitStyle = "git_commit_style"
	// ValueWebBaseURL is Timothy's own web UI base address (e.g.
	// https://timothy.example.lan), used to build a mission detail link
	// in destination deliveries (destinations/render.go); "" omits the
	// link entirely rather than guessing a LAN address.
	ValueWebBaseURL = "web_base_url"
	// ValueTimezone is the operator's IANA timezone name (e.g.
	// "Europe/Amsterdam"), used to render delivery timestamps
	// (destinations/render.go); "" defers to time.UTC.
	ValueTimezone = "timezone"
	// ValuePermissionTimeoutSeconds bounds how long a mission may sit
	// parked on pending_permission before the periodic sweep
	// (missions/sweep.go) auto-denies it (issue #445); "" or "0" (the
	// default) disables the sweep entirely, preserving park-forever
	// behavior. A mission's own permission_timeout_seconds column
	// overrides this per mission.
	ValuePermissionTimeoutSeconds = "permission_timeout_seconds"
	// ValueAskTimeoutSeconds bounds how long a mission may sit parked on
	// pending_input (ask_user, D-088, issue #457) before the periodic
	// sweep (missions/sweep.go) applies the question's proposed_default
	// and resumes; "" or "0" (the default) disables the sweep, so an
	// ask_user park waits forever unless the operator opts in, same
	// 0-means-off convention as ValuePermissionTimeoutSeconds.
	ValueAskTimeoutSeconds = "ask_timeout_seconds"
	// ValueExecutorRunBudgetMinutes caps one delegated executor run's
	// wall clock (missions/delegated.go, issue #498); "" (the default)
	// defers to DefaultExecutorRunBudget. A runaway backstop only: the
	// idle timeout, cost budget and max_iterations are what actually
	// stop a broken run, so this stays large enough that a healthy
	// multi-hour coding mission never hits it.
	ValueExecutorRunBudgetMinutes = "executor_run_budget_minutes"
	// ValueReviewTokenCeiling caps the input tokens one mission may spend
	// on review turns (missions/driver.go, D-097, issue #527), summed
	// from the cost ledger before every review round; "" (the default)
	// defers to DefaultReviewTokenCeiling, "0" disables the ceiling.
	ValueReviewTokenCeiling = "mission_review_token_ceiling"
)

// DefaultReviewTokenCeiling is the per-mission review input token cap
// when ValueReviewTokenCeiling is unset.
const DefaultReviewTokenCeiling int64 = 1_500_000

// DefaultExecutorRunBudget is the wall-clock cap a delegated executor
// run gets when ValueExecutorRunBudgetMinutes is unset.
const DefaultExecutorRunBudget = 8 * time.Hour

var knownValueKeys = map[string]bool{
	ValueTokenBudget: true, ValueSkillsAllowlist: true,
	ValueGitAuthorName: true, ValueGitAuthorEmail: true,
	ValueSensitiveToolRoute: true, ValueDefaultCurrency: true,
	ValueCodingExecutor:   true,
	ValueGitBranchPattern: true, ValueGitCommitStyle: true,
	ValueWebBaseURL: true, ValueTimezone: true,
	ValuePermissionTimeoutSeconds: true, ValueAskTimeoutSeconds: true,
	ValueExecutorRunBudgetMinutes: true, ValueReviewTokenCeiling: true,
}

// allowedCurrencies is the flat, fixed list of ISO 4217 codes the
// default-currency setting accepts — no FX conversion, no per-currency
// formatting rules, just a membership check.
var allowedCurrencies = map[string]bool{
	"USD": true, "EUR": true, "GBP": true, "JPY": true, "CNY": true,
	"INR": true, "BDT": true, "CHF": true, "CAD": true, "AUD": true,
	"NZD": true, "SEK": true, "NOK": true, "DKK": true, "PLN": true,
	"CZK": true, "HUF": true, "TRY": true, "AED": true, "SAR": true,
	"SGD": true, "HKD": true, "KRW": true, "TWD": true, "THB": true,
	"MYR": true, "IDR": true, "PHP": true, "VND": true, "BRL": true,
	"MXN": true, "ZAR": true, "ILS": true,
}

// AllowedCurrencies returns a copy of the supported ISO 4217 code set
// — internal/brain/fxrates' daily fetcher uses this as the set of
// quote currencies to store, so the two lists never drift apart.
func AllowedCurrencies() map[string]bool {
	out := make(map[string]bool, len(allowedCurrencies))
	for k, v := range allowedCurrencies {
		out[k] = v
	}
	return out
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
// unknown keys, except knownKeysOff members which default to false.
func (s *Store) Enabled(ctx context.Context, key string) bool {
	all := s.All(ctx)
	if v, ok := all[key]; ok {
		return v
	}
	return !knownKeysOff[key]
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

// PermissionTimeoutSeconds parses the global parked-permission timeout,
// falling back to 0 (disabled) when unset or unparsable. 0 means the
// sweep never auto-denies, preserving the original park-forever
// behavior.
func (s *Store) PermissionTimeoutSeconds(ctx context.Context) int {
	if v := s.Value(ctx, ValuePermissionTimeoutSeconds); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 0
}

// AskTimeoutSeconds parses the global ask_user timeout (D-088), falling
// back to 0 (disabled) when unset or unparsable, same shape as
// PermissionTimeoutSeconds.
func (s *Store) AskTimeoutSeconds(ctx context.Context) int {
	if v := s.Value(ctx, ValueAskTimeoutSeconds); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 0
}

// ExecutorRunBudget parses the delegated executor wall-clock cap,
// falling back to DefaultExecutorRunBudget when unset or unparsable.
func (s *Store) ExecutorRunBudget(ctx context.Context) time.Duration {
	if v := s.Value(ctx, ValueExecutorRunBudgetMinutes); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Minute
		}
	}
	return DefaultExecutorRunBudget
}

// ReviewTokenCeiling parses the per-mission review input token cap:
// unset or unparsable falls back to DefaultReviewTokenCeiling, 0
// disables the ceiling.
func (s *Store) ReviewTokenCeiling(ctx context.Context) int64 {
	if v := s.Value(ctx, ValueReviewTokenCeiling); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return DefaultReviewTokenCeiling
}

// DefaultCurrency returns the configured default currency, falling
// back to "USD" when unset — mirrors TokenBudget's parse-with-
// fallback pattern.
func (s *Store) DefaultCurrency(ctx context.Context) string {
	if v := s.Value(ctx, ValueDefaultCurrency); v != "" {
		return v
	}
	return "USD"
}

// CodingExecutor returns the configured default harness for a coding
// mission's create request, "" (native) when unset.
func (s *Store) CodingExecutor(ctx context.Context) string {
	return s.Value(ctx, ValueCodingExecutor)
}

// GitBranchPattern returns the configured default branch-name template,
// "" (missions.DefaultBranchPattern) when unset.
func (s *Store) GitBranchPattern(ctx context.Context) string {
	return s.Value(ctx, ValueGitBranchPattern)
}

// GitCommitStyle returns the configured default commit-message style,
// "" (missions.CommitStyleConventional) when unset.
func (s *Store) GitCommitStyle(ctx context.Context) string {
	return s.Value(ctx, ValueGitCommitStyle)
}

// WebBaseURL returns the configured web UI base address, "" (omit the
// mission link) when unset.
func (s *Store) WebBaseURL(ctx context.Context) string {
	return s.Value(ctx, ValueWebBaseURL)
}

// Location returns the operator's configured timezone, time.UTC when
// unset or when the stored value fails to load (SetValue already
// rejects a non-IANA value at write time, so a load failure here means
// stale data, not a fresh mistake).
func (s *Store) Location(ctx context.Context) *time.Location {
	v := s.Value(ctx, ValueTimezone)
	if v == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(v)
	if err != nil {
		return time.UTC
	}
	return loc
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
		flags[k] = !knownKeysOff[k]
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
	if (key == ValueTokenBudget || key == ValueExecutorRunBudgetMinutes) && value != "" {
		if n, err := strconv.Atoi(value); err != nil || n <= 0 {
			return fmt.Errorf("%s must be a positive integer or empty", key)
		}
	}
	if (key == ValuePermissionTimeoutSeconds || key == ValueAskTimeoutSeconds || key == ValueReviewTokenCeiling) && value != "" {
		if n, err := strconv.Atoi(value); err != nil || n < 0 {
			return fmt.Errorf("%s must be a non-negative integer or empty", key)
		}
	}
	if key == ValueDefaultCurrency && value != "" {
		value = strings.ToUpper(value)
		if !allowedCurrencies[value] {
			return fmt.Errorf("%s: unsupported currency code %q", key, value)
		}
	}
	if key == ValueCodingExecutor && value != "" {
		if value == "native" {
			value = ""
		} else if _, ok := executor.Lookup(value); !ok {
			return fmt.Errorf("%s: unknown harness %q", key, value)
		}
	}
	if key == ValueGitBranchPattern && value != "" {
		if err := missions.ValidateBranchPattern(value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	if key == ValueGitCommitStyle && value != "" {
		if err := missions.ValidateCommitStyle(value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	if key == ValueTimezone && value != "" {
		if _, err := time.LoadLocation(value); err != nil {
			return fmt.Errorf("%s: unknown IANA timezone %q", key, value)
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
