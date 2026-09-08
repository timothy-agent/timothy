// Package agents is brain's agent registry (D-030, D-034): who serves
// a session. An agent is configuration — a prompt overlay, a route
// (its model chain), skill and tool allowlists, and a memory switch —
// stored as rows, edited from the settings panel, resolved per turn.
// Exactly one agent is the default: the zero-click choice.
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/brain/missions/executor"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Agent is the API shape of one agents row. Empty Skills or Tools
// means none of that surface is allowed (both opt-in only, resolved
// by internal/brain/chat's resolveToolAllow/allowedPacks — retrieve_
// output and, when Skills is non-empty, load_skill stay available
// regardless); empty Route means the default route.
//
// ReviewRoute is meaningless to a chat-only agent and stays at its
// zero value for one; a mission-capable agent (internal/brain/missions)
// sets it — missions reference this table directly rather than a
// parallel agent_profiles table. ApprovalAllowlist
// applies to both: missions grant it at provisioning time
// (missions/driver.go), chat seeds the same session_grants rows on a
// session's first turn under the agent (chat.Service.SetApprovalGrants).
type Agent struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	PromptOverlay     string   `json:"prompt_overlay"`
	Route             string   `json:"route"`
	Skills            []string `json:"skills"`
	Tools             []string `json:"tools"`
	Memory            bool     `json:"memory"`
	IsDefault         bool     `json:"is_default"`
	Enabled           bool     `json:"enabled"`
	ReviewRoute       string   `json:"review_route"`
	ApprovalAllowlist []string `json:"approval_allowlist"`
	// Harness is the coding executor this agent's missions delegate
	// to when the mission itself leaves harness empty (mission.harness
	// -> agent.harness -> settings.coding_executor -> native). Empty
	// means inherit from settings; meaningless outside kind=coding.
	Harness string `json:"harness"`
	// Knowledge names the kb_collections this agent may search with
	// search_kb (D-060) — empty means none (opt-in only, same as
	// Skills/Tools). Collection scoping is enforced in Go at the tool
	// call, never left to a prompt.
	Knowledge []string `json:"knowledge"`
}

// validateName trims a.Name and checks it against the plain-text rule
// (issue #615): 1..64 runes, any printable character, no control
// characters. Returns the trimmed name.
func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if utf8.RuneCountInString(name) > 64 {
		return "", fmt.Errorf("name must be at most 64 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("name must not contain control characters")
		}
	}
	return name, nil
}

func validateHarness(harness string) error {
	if harness != "" {
		if _, ok := executor.Lookup(harness); !ok {
			return fmt.Errorf("harness: unknown harness %q", harness)
		}
	}
	return nil
}

// Sentinel errors the HTTP layer maps onto status codes.
var (
	ErrNotFound     = errors.New("not found")
	ErrInUse        = errors.New("in use")
	ErrNameConflict = errors.New("an agent with this name already exists")
)

const cacheTTL = 10 * time.Second

// Store is the agents table's CRUD plus a cached per-turn resolver.
type Store struct {
	db  *pgpool.Pool
	log *slog.Logger

	mu      sync.Mutex
	cached  map[string]Agent // by name, enabled only
	def     string           // default agent's name
	fetched time.Time
}

func NewStore(db *pgpool.Pool, log *slog.Logger) *Store {
	return &Store{db: db, log: log}
}

const agentColumns = `id, name, description, prompt_overlay, route, skills, tools, memory, is_default, enabled, review_route, approval_allowlist, knowledge, harness`

func scanAgent(row pgx.Row) (Agent, error) {
	var (
		a                              Agent
		skills, tools, appr, knowledge []byte
	)
	if err := row.Scan(&a.ID, &a.Name, &a.Description, &a.PromptOverlay, &a.Route,
		&skills, &tools, &a.Memory, &a.IsDefault, &a.Enabled,
		&a.ReviewRoute, &appr, &knowledge, &a.Harness); err != nil {
		return Agent{}, err
	}
	_ = json.Unmarshal(skills, &a.Skills)
	_ = json.Unmarshal(tools, &a.Tools)
	_ = json.Unmarshal(appr, &a.ApprovalAllowlist)
	_ = json.Unmarshal(knowledge, &a.Knowledge)
	if a.Skills == nil {
		a.Skills = []string{}
	}
	if a.Tools == nil {
		a.Tools = []string{}
	}
	if a.ApprovalAllowlist == nil {
		a.ApprovalAllowlist = []string{}
	}
	if a.Knowledge == nil {
		a.Knowledge = []string{}
	}
	return a, nil
}

// List returns every agent, default first then by name.
func (s *Store) List(ctx context.Context) ([]Agent, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("agents: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+agentColumns+` FROM agents ORDER BY is_default DESC, name`)
	if err != nil {
		return nil, fmt.Errorf("agents list: %w", err)
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("agents list: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Enabled returns every enabled agent from the same short cache Resolve
// reads — the candidate set for auto-dispatch (D-034 follow-up). A
// degraded read (DB outage, or no agent configured at all) has no real
// agent to offer, so it returns empty rather than the synthetic
// zero-value profile load() substitutes to keep chat serving.
func (s *Store) Enabled(ctx context.Context) []Agent {
	byName, _ := s.load(ctx)
	out := make([]Agent, 0, len(byName))
	for _, a := range byName {
		if a.Name == "" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Resolve returns the enabled agent serving this name; empty name (or
// a vanished agent) resolves to the default. The bool reports whether
// the requested name actually resolved — false only when a non-empty
// name matched nothing.
//
// Callers should prefer ResolveByID: this is now the read-time
// compatibility path for session_events/cost_ledger rows written
// before agents were addressed by id (issue #615), same idea as
// missions' parsePhase.
func (s *Store) Resolve(ctx context.Context, name string) (Agent, bool) {
	byName, def := s.load(ctx)
	if name == "" {
		return byName[def], true
	}
	a, ok := byName[name]
	if !ok {
		return byName[def], false
	}
	return a, true
}

// ResolveByID is Resolve's counterpart for callers that hold an
// agent's id rather than its name (e.g. the mission-create UI, which
// picks from a list keyed by id) — same empty-means-default and
// degrade-to-default-on-miss behavior as Resolve.
func (s *Store) ResolveByID(ctx context.Context, id string) (Agent, bool) {
	byName, def := s.load(ctx)
	if id == "" {
		return byName[def], true
	}
	for _, a := range byName {
		if a.ID == id {
			return a, true
		}
	}
	return byName[def], false
}

// load returns the enabled agents and the default's name from a short
// cache. A database outage degrades to the zero-value agent (no
// tools/skills beyond chat's always-on exemptions, default route,
// memory on) — chat must keep working when config storage hiccups.
func (s *Store) load(ctx context.Context) (map[string]Agent, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && time.Since(s.fetched) < cacheTTL {
		return s.cached, s.def
	}
	db, err := s.db.Get()
	if err != nil {
		s.log.Warn("agents read degraded to defaults", "error", err)
		return map[string]Agent{"": {Memory: true}}, ""
	}
	rows, err := db.Query(ctx, `SELECT `+agentColumns+` FROM agents WHERE enabled`)
	if err != nil {
		s.log.Warn("agents read degraded to defaults", "error", err)
		return map[string]Agent{"": {Memory: true}}, ""
	}
	defer rows.Close()
	byName := map[string]Agent{}
	def := ""
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			continue
		}
		byName[a.Name] = a
		if a.IsDefault {
			def = a.Name
		}
	}
	// No default row (all disabled, empty table): a zero-value profile
	// keeps chat serving.
	if _, ok := byName[def]; !ok {
		byName[def] = Agent{Memory: true}
	}
	s.cached, s.def, s.fetched = byName, def, time.Now()
	return byName, def
}

func (s *Store) invalidate() {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
}

// Create inserts an agent and audits. name is trimmed; a case-
// insensitive duplicate (agents_name_ci) reports ErrNameConflict
// rather than a raw pg error.
func (s *Store) Create(ctx context.Context, a Agent) (string, error) {
	name, err := validateName(a.Name)
	if err != nil {
		return "", err
	}
	a.Name = name
	if err := validateHarness(a.Harness); err != nil {
		return "", err
	}
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("agents create: %w", err)
	}
	skills, tools, appr, knowledge := jsonArr(a.Skills), jsonArr(a.Tools), jsonArr(a.ApprovalAllowlist), jsonArr(a.Knowledge)
	var id string
	err = db.QueryRow(ctx, `INSERT INTO agents
			(name, description, prompt_overlay, route, skills, tools, memory, enabled,
			 review_route, approval_allowlist, knowledge, harness)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		a.Name, a.Description, a.PromptOverlay, a.Route, skills, tools, a.Memory, a.Enabled,
		a.ReviewRoute, appr, knowledge, a.Harness).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrNameConflict
		}
		return "", fmt.Errorf("agents create: %w", err)
	}
	s.audit(ctx, "create", id, nil, a)
	s.invalidate()
	return id, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Patch applies a partial update. is_default moves via SetDefault. A
// non-nil Name renames the agent (audited with before/after).
type Patch struct {
	Name              *string   `json:"name"`
	Description       *string   `json:"description"`
	PromptOverlay     *string   `json:"prompt_overlay"`
	Route             *string   `json:"route"`
	Skills            *[]string `json:"skills"`
	Tools             *[]string `json:"tools"`
	Memory            *bool     `json:"memory"`
	Enabled           *bool     `json:"enabled"`
	ReviewRoute       *string   `json:"review_route"`
	ApprovalAllowlist *[]string `json:"approval_allowlist"`
	Knowledge         *[]string `json:"knowledge"`
	Harness           *string   `json:"harness"`
}

func (s *Store) Patch(ctx context.Context, id string, p Patch) error {
	if p.Name != nil {
		name, err := validateName(*p.Name)
		if err != nil {
			return err
		}
		p.Name = &name
	}
	if p.Harness != nil {
		if err := validateHarness(*p.Harness); err != nil {
			return err
		}
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("agents patch: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("agents patch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	before, err := scanAgent(tx.QueryRow(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return fmt.Errorf("agent %s: %w", id, ErrNotFound)
	}
	after := before
	if p.Name != nil {
		after.Name = *p.Name
	}
	if p.Description != nil {
		after.Description = *p.Description
	}
	if p.PromptOverlay != nil {
		after.PromptOverlay = *p.PromptOverlay
	}
	if p.Route != nil {
		after.Route = *p.Route
	}
	if p.Skills != nil {
		after.Skills = *p.Skills
	}
	if p.Tools != nil {
		after.Tools = *p.Tools
	}
	if p.Memory != nil {
		after.Memory = *p.Memory
	}
	if p.Enabled != nil {
		if !*p.Enabled && before.IsDefault {
			return fmt.Errorf("the default agent cannot be disabled; set another default first")
		}
		after.Enabled = *p.Enabled
	}
	if p.ReviewRoute != nil {
		after.ReviewRoute = *p.ReviewRoute
	}
	if p.ApprovalAllowlist != nil {
		after.ApprovalAllowlist = *p.ApprovalAllowlist
	}
	if p.Knowledge != nil {
		after.Knowledge = *p.Knowledge
	}
	if p.Harness != nil {
		after.Harness = *p.Harness
	}
	if _, err := tx.Exec(ctx, `UPDATE agents SET name = $2, description = $3, prompt_overlay = $4,
			route = $5, skills = $6, tools = $7, memory = $8, enabled = $9,
			review_route = $10, approval_allowlist = $11, knowledge = $12,
			harness = $13, updated_at = now()
		WHERE id = $1`,
		id, after.Name, after.Description, after.PromptOverlay, after.Route,
		jsonArr(after.Skills), jsonArr(after.Tools), after.Memory, after.Enabled,
		after.ReviewRoute, jsonArr(after.ApprovalAllowlist), jsonArr(after.Knowledge),
		after.Harness); err != nil {
		if isUniqueViolation(err) {
			return ErrNameConflict
		}
		return fmt.Errorf("agents patch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("agents patch: %w", err)
	}
	s.audit(ctx, "update", id, before, after)
	s.invalidate()
	return nil
}

// SetDefault moves the single default flag to this agent.
func (s *Store) SetDefault(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("agents default: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("agents default: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM agents WHERE id = $1 FOR UPDATE`, id).Scan(&enabled); err != nil {
		return fmt.Errorf("agent %s: %w", id, ErrNotFound)
	}
	if !enabled {
		return fmt.Errorf("a disabled agent cannot be the default")
	}
	if _, err := tx.Exec(ctx, `UPDATE agents SET is_default = false WHERE is_default`); err != nil {
		return fmt.Errorf("agents default: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agents SET is_default = true, updated_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("agents default: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("agents default: %w", err)
	}
	s.audit(ctx, "update", id, nil, map[string]bool{"is_default": true})
	s.invalidate()
	return nil
}

// Delete removes an agent; the default is protected (sessions must
// always have somewhere to land).
func (s *Store) Delete(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("agents delete: %w", err)
	}
	before, err := scanAgent(db.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE id = $1`, id))
	if err != nil {
		return fmt.Errorf("agent %s: %w", id, ErrNotFound)
	}
	if before.IsDefault {
		return fmt.Errorf("the default agent cannot be deleted: %w", ErrInUse)
	}
	if _, err := db.Exec(ctx, `DELETE FROM agents WHERE id = $1`, id); err != nil {
		return fmt.Errorf("agents delete: %w", err)
	}
	s.audit(ctx, "delete", id, before, nil)
	s.invalidate()
	return nil
}

func (s *Store) audit(ctx context.Context, action, id string, before, after any) {
	db, err := s.db.Get()
	if err != nil {
		s.log.Warn("agents audit skipped", "action", action, "error", err)
		return
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if _, err := db.Exec(ctx, `INSERT INTO admin_audit (action, entity, entity_id, before, after)
		VALUES ($1, 'agent', $2, $3, $4)`, action, id, b, a); err != nil {
		s.log.Warn("agents audit failed", "action", action, "error", err)
	}
}

func jsonArr(v []string) []byte {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return b
}
