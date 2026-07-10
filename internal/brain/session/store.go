package session

import (
	"context"
	"encoding/json"
	"fmt"
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
	LastCategory string    `json:"last_category,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
