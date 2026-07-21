package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Store persists session rows and their append-only event logs.
type Store struct {
	db *pgpool.Pool
}

func NewStore(db *pgpool.Pool) *Store {
	return &Store{db: db}
}

// Create opens a session and writes its session_started event.
func (s *Store) Create(ctx context.Context, title string) (string, error) {
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
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Archived     bool      `json:"archived"`
	LastRoute string    `json:"last_route,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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

	var sql string
	var args []any
	if query == "" {
		sql = `SELECT id, COALESCE(title, ''), archived, last_route, created_at, updated_at
		       FROM sessions WHERE NOT archived AND (updated_at, id) < ($1, $2::uuid)
		       ORDER BY updated_at DESC, id DESC LIMIT $3`
		args = []any{before, beforeID, listLimit}
	} else {
		// ILIKE wildcards in the user's query are literals, not patterns.
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
		sql = `SELECT DISTINCT s.id, COALESCE(s.title, ''), s.archived, s.last_route, s.created_at, s.updated_at
		       FROM sessions s
		       LEFT JOIN session_events e ON e.session_id = s.id AND e.kind = 'user_message'
		       WHERE (s.updated_at, s.id) < ($2, $3::uuid)
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
		if err := rows.Scan(&m.ID, &m.Title, &m.Archived, &m.LastRoute, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("session: list scan: %w", err)
		}
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
	err = db.QueryRow(ctx,
		`SELECT id, COALESCE(title, ''), archived, last_route, created_at, updated_at
		 FROM sessions WHERE id = $1`, id,
	).Scan(&m.ID, &m.Title, &m.Archived, &m.LastRoute, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return Meta{}, fmt.Errorf("session: get %s: %w", id, err)
	}
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

// SetLastRoute remembers the session's most recent route and agent so
// the UI composer can restore it.
func (s *Store) SetLastRoute(ctx context.Context, id, route, agent string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("session: set route: %w", err)
	}
	_, err = db.Exec(ctx,
		"UPDATE sessions SET last_route = $2, agent = $3 WHERE id = $1", id, route, agent)
	return err
}
