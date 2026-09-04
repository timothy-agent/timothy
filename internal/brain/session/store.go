package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// ErrMissionReferenced refuses deletion of a session a mission points
// at — mission history must keep its transcript.
var ErrMissionReferenced = errors.New("session referenced by a mission")

// Store persists session rows and their append-only event logs.
type Store struct {
	db  *pgpool.Pool
	log *slog.Logger
}

func NewStore(db *pgpool.Pool, log *slog.Logger) *Store {
	return &Store{db: db, log: log}
}

// Create opens a session and writes its session_started event. Debug-
// logs the immediate caller (file:line) and whether title was blank:
// an untitled session with no obvious owner (chat.Chat's "no session
// yet" path, the API's POST /v1/sessions, a mission's bookkeeping
// session) has shown up unexplained before — this pinpoints which
// call site created it without threading a caller-identity param
// through every one of them.
func (s *Store) Create(ctx context.Context, title string) (string, error) {
	if s.log != nil {
		_, file, line, ok := runtime.Caller(1)
		if ok {
			s.log.Debug("session: create", "caller", fmt.Sprintf("%s:%d", file, line), "titled", title != "")
		}
	}
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("session: create: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("session: create begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var id string
	if err := tx.QueryRow(ctx,
		"INSERT INTO sessions (title) VALUES (NULLIF($1, '')) RETURNING id", title,
	).Scan(&id); err != nil {
		return "", fmt.Errorf("session: create insert: %w", err)
	}
	payload, err := json.Marshal(SessionStarted{Title: title})
	if err != nil {
		return "", fmt.Errorf("session: create payload: %w", err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO session_events (session_id, seq, kind, payload) VALUES ($1, 1, $2, $3)",
		id, KindSessionStarted, payload,
	); err != nil {
		return "", fmt.Errorf("session: create event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("session: create commit: %w", err)
	}
	return id, nil
}

// Append writes one event, assigning the next sequence number inside
// the transaction. The session row is locked FOR UPDATE so concurrent
// appenders to the same session serialize; different sessions never
// contend.
func (s *Store) Append(ctx context.Context, sessionID, kind string, payload any) (int64, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("session: append: %w", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("session: append payload: %w", err)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("session: append begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var exists bool
	if err := tx.QueryRow(ctx,
		"SELECT true FROM sessions WHERE id = $1 FOR UPDATE", sessionID,
	).Scan(&exists); err != nil {
		return 0, fmt.Errorf("session: append lock %s: %w", sessionID, err)
	}

	var seq int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO session_events (session_id, seq, kind, payload)
		 SELECT $1, COALESCE(MAX(seq), 0) + 1, $2, $3 FROM session_events WHERE session_id = $1
		 RETURNING seq`,
		sessionID, kind, data,
	).Scan(&seq); err != nil {
		return 0, fmt.Errorf("session: append insert: %w", err)
	}
	if _, err := tx.Exec(ctx,
		"UPDATE sessions SET updated_at = now() WHERE id = $1", sessionID,
	); err != nil {
		return 0, fmt.Errorf("session: append touch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("session: append commit: %w", err)
	}
	return seq, nil
}

// Events reads a session's full log in order.
func (s *Store) Events(ctx context.Context, sessionID string) ([]Event, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("session: events: %w", err)
	}
	rows, err := db.Query(ctx,
		`SELECT session_id, seq, kind, payload, created_at
		 FROM session_events WHERE session_id = $1 ORDER BY seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session: events query: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var ev Event
		if err := rows.Scan(&ev.SessionID, &ev.Seq, &ev.Kind, &ev.Payload, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("session: events scan: %w", err)
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// Meta is a session listing row.
type Meta struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Archived  bool      `json:"archived"`
	LastRoute string    `json:"last_route,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Knowledge names kb_collections the user pinned to this session
	// (composer # mentions); unioned with the serving agent's own
	// Knowledge list per turn (D-060).
	Knowledge []string `json:"knowledge"`
	// Mission is true when a missions row points at this session via
	// missions.session_id: tool-audit bookkeeping, not chat.
	Mission bool `json:"mission,omitempty"`
}

const listLimit = 100

// List returns sessions newest-first, one page at a time, ordered by
// (updated_at, id) descending. A non-empty query matches the title or
// full-text over user messages; archived sessions appear only in query
// results. A non-zero cursor (before, beforeID — the last row of the
// previous page) returns rows strictly earlier in that ordering: the
// id tiebreaker keeps pages stable when timestamps collide.
func (s *Store) List(ctx context.Context, query string, before time.Time, beforeID string) ([]Meta, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("session: list: %w", err)
	}
	if before.IsZero() {
		// First page: a cursor no real row can reach.
		before = time.Now().Add(24 * time.Hour)
		beforeID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	}

	// Mission bookkeeping sessions (missions.session_id) are not chat:
	// they exist so tool audit rows have a session FK (migration 0028)
	// and would otherwise litter the list as untitled sessions.
	const notMission = `NOT EXISTS (SELECT 1 FROM missions m WHERE m.session_id = %s.id)`

	var sql string
	var args []any
	if query == "" {
		sql = `SELECT id, COALESCE(title, ''), archived, last_route, created_at, updated_at, knowledge
		       FROM sessions WHERE NOT archived AND (updated_at, id) < ($1, $2::uuid)
		         AND ` + fmt.Sprintf(notMission, "sessions") + `
		       ORDER BY updated_at DESC, id DESC LIMIT $3`
		args = []any{before, beforeID, listLimit}
	} else {
		// ILIKE wildcards in the user's query are literals, not patterns.
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
		sql = `SELECT DISTINCT s.id, COALESCE(s.title, ''), s.archived, s.last_route, s.created_at, s.updated_at, s.knowledge
		       FROM sessions s
		       LEFT JOIN session_events e ON e.session_id = s.id AND e.kind = 'user_message'
		       WHERE (s.updated_at, s.id) < ($2, $3::uuid)
		         AND ` + fmt.Sprintf(notMission, "s") + `
		         AND (s.title ILIKE '%' || $4 || '%'
		          OR (e.payload IS NOT NULL
		              AND to_tsvector('english', e.payload->>'text') @@ plainto_tsquery('english', $1)))
		       ORDER BY s.updated_at DESC, s.id DESC LIMIT $5`
		args = []any{query, before, beforeID, escaped, listLimit}
	}

	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("session: list query: %w", err)
	}
	defer rows.Close()

	var out []Meta
	for rows.Next() {
		var m Meta
		var knowledge []byte
		if err := rows.Scan(&m.ID, &m.Title, &m.Archived, &m.LastRoute, &m.CreatedAt, &m.UpdatedAt, &knowledge); err != nil {
			return nil, fmt.Errorf("session: list scan: %w", err)
		}
		_ = json.Unmarshal(knowledge, &m.Knowledge)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Get returns one session's metadata.
func (s *Store) Get(ctx context.Context, id string) (Meta, error) {
	db, err := s.db.Get()
	if err != nil {
		return Meta{}, fmt.Errorf("session: get: %w", err)
	}
	var m Meta
	var knowledge []byte
	err = db.QueryRow(ctx,
		`SELECT id, COALESCE(title, ''), archived, last_route, created_at, updated_at, knowledge,
		        EXISTS (SELECT 1 FROM missions m WHERE m.session_id = sessions.id)
		 FROM sessions WHERE id = $1`, id,
	).Scan(&m.ID, &m.Title, &m.Archived, &m.LastRoute, &m.CreatedAt, &m.UpdatedAt, &knowledge, &m.Mission)
	if err != nil {
		return Meta{}, fmt.Errorf("session: get %s: %w", id, err)
	}
	_ = json.Unmarshal(knowledge, &m.Knowledge)
	return m, nil
}

// Update patches title and/or archived; nil means unchanged.
func (s *Store) Update(ctx context.Context, id string, title *string, archived *bool) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("session: update: %w", err)
	}
	tag, err := db.Exec(ctx,
		`UPDATE sessions SET
		   title = COALESCE($2, title),
		   archived = COALESCE($3, archived),
		   updated_at = now()
		 WHERE id = $1`, id, title, archived)
	if err != nil {
		return fmt.Errorf("session: update %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session: update %s: not found", id)
	}
	return nil
}

// Delete permanently removes a session and every session-scoped
// record: events, permission grants, tool audit rows, and stored tool
// outputs (D-035). This is user-initiated data control, not an edit of
// history — the append-only rule forbids mutating events in place, not
// discarding a whole session. Cost-ledger rows and extracted memories
// survive on purpose: spend history stays honest and the supersede-only
// memory store keeps its facts (their source_session link dangles).
// A session referenced by a mission refuses with ErrMissionReferenced.
func (s *Store) Delete(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("session: delete: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("session: delete begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var referenced bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM missions WHERE session_id = $1)`, id,
	).Scan(&referenced); err != nil {
		return fmt.Errorf("session: delete mission check: %w", err)
	}
	if referenced {
		return fmt.Errorf("session: delete %s: %w", id, ErrMissionReferenced)
	}

	for _, stmt := range []string{
		`DELETE FROM session_grants WHERE session_id = $1`,
		`DELETE FROM tool_audit WHERE session_id = $1`,
		`DELETE FROM tool_outputs WHERE session_id = $1`,
		`DELETE FROM session_events WHERE session_id = $1`,
	} {
		if _, err := tx.Exec(ctx, stmt, id); err != nil {
			return fmt.Errorf("session: delete %s: %w", id, err)
		}
	}
	tag, err := tx.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("session: delete %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session: delete %s: not found", id)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("session: delete commit: %w", err)
	}
	return nil
}

// SetTitleIfEmpty writes an auto-generated title without clobbering a
// user-chosen one.
func (s *Store) SetTitleIfEmpty(ctx context.Context, id, title string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("session: set title: %w", err)
	}
	_, err = db.Exec(ctx,
		"UPDATE sessions SET title = $2, updated_at = now() WHERE id = $1 AND (title IS NULL OR title = '')",
		id, title)
	return err
}

// SetLastRoute remembers the session's most recent route so the UI
// composer can restore it. agent is unused (per-turn agent attribution
// lives in message events, not on the session row) but stays in the
// signature to match the Directory interface chat.go calls through.
func (s *Store) SetLastRoute(ctx context.Context, id, route, agent string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("session: set route: %w", err)
	}
	_, err = db.Exec(ctx,
		"UPDATE sessions SET last_route = $2 WHERE id = $1", id, route)
	return err
}

// Knowledge returns a session's pinned kb_collection names.
func (s *Store) Knowledge(ctx context.Context, id string) ([]string, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("session: knowledge %s: %w", id, err)
	}
	var data []byte
	if err := db.QueryRow(ctx, "SELECT knowledge FROM sessions WHERE id = $1", id).Scan(&data); err != nil {
		return nil, fmt.Errorf("session: knowledge %s: %w", id, err)
	}
	var names []string
	_ = json.Unmarshal(data, &names)
	return names, nil
}

// SetKnowledge replaces a session's pinned kb_collection names outright.
func (s *Store) SetKnowledge(ctx context.Context, id string, names []string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("session: knowledge %s: %w", id, err)
	}
	if names == nil {
		names = []string{}
	}
	data, err := json.Marshal(names)
	if err != nil {
		return fmt.Errorf("session: knowledge %s: %w", id, err)
	}
	tag, err := db.Exec(ctx,
		"UPDATE sessions SET knowledge = $2, updated_at = now() WHERE id = $1", id, data)
	if err != nil {
		return fmt.Errorf("session: knowledge %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session: knowledge %s: not found", id)
	}
	return nil
}

// AddKnowledge unions names into a session's pinned kb_collection list
// in a single statement, so concurrent turns naming different
// collections can't lose entries to a lost read-modify-write.
func (s *Store) AddKnowledge(ctx context.Context, id string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("session: knowledge %s: %w", id, err)
	}
	tag, err := db.Exec(ctx,
		`UPDATE sessions SET knowledge = (
		     SELECT coalesce(jsonb_agg(DISTINCT v), '[]'::jsonb)
		     FROM (
		         SELECT jsonb_array_elements_text(knowledge) AS v
		         UNION
		         SELECT unnest($2::text[])
		     ) merged
		 ), updated_at = now() WHERE id = $1`, id, names)
	if err != nil {
		return fmt.Errorf("session: knowledge %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session: knowledge %s: not found", id)
	}
	return nil
}

// PendingPermission is one unresolved permission_request, joined with
// its session's title for the global "you have a permission waiting"
// indicator.
type PendingPermission struct {
	SessionID    string    `json:"session_id"`
	SessionTitle string    `json:"session_title"`
	Tool         string    `json:"tool"`
	Rationale    string    `json:"rationale"`
	RequestedAt  time.Time `json:"requested_at"`
}

// PendingPermissions returns every unresolved permission_request among
// sessionIDs — a targeted query, not a full-table scan: the caller
// (the API handler) passes chat.Service.ActiveSessions(), so a
// permission_request whose turn already died (crash, abandoned) is
// scoped out before this query even runs, rather than filtered after
// fetching every session's full event log. "Unresolved" means no
// later permission_resolved event with the same payload->>'id' exists
// in that session's log. Empty sessionIDs returns nil without a query.
func (s *Store) PendingPermissions(ctx context.Context, sessionIDs []string) ([]PendingPermission, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("session: pending permissions: %w", err)
	}
	rows, err := db.Query(ctx,
		`SELECT req.session_id, COALESCE(s.title, ''), req.payload->>'tool',
		        req.payload->>'rationale', req.created_at
		 FROM session_events req
		 JOIN sessions s ON s.id = req.session_id
		 WHERE req.kind = 'permission_request'
		   AND req.session_id = ANY($1)
		   AND NOT EXISTS (
		       SELECT 1 FROM session_events res
		       WHERE res.session_id = req.session_id
		         AND res.kind = 'permission_resolved'
		         AND res.payload->>'id' = req.payload->>'id'
		   )
		 ORDER BY req.created_at`,
		sessionIDs)
	if err != nil {
		return nil, fmt.Errorf("session: pending permissions query: %w", err)
	}
	defer rows.Close()

	var out []PendingPermission
	for rows.Next() {
		var p PendingPermission
		if err := rows.Scan(&p.SessionID, &p.SessionTitle, &p.Tool, &p.Rationale, &p.RequestedAt); err != nil {
			return nil, fmt.Errorf("session: pending permissions scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
