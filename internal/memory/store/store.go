// Package store persists Timothy's long-term memories (D-011). Writes
// are staged: agent-extracted facts land pending and are promoted by
// policy or user confirmation; user-explicit facts activate directly.
// Facts are never updated in place — corrections insert a new row and
// supersede the old one.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// ErrNotFound reports that no memory matched the given id (or the id
// was in a state the operation does not apply to).
var ErrNotFound = errors.New("memory not found")

// Store reads and writes the memories table.
type Store struct {
	db  *pgpool.Pool
	log *slog.Logger
}

func New(db *pgpool.Pool, log *slog.Logger) *Store {
	return &Store{db: db, log: log}
}

const memoryColumns = `id, type, content, entity_refs, ` +
	`COALESCE(source_session::text, ''), COALESCE(source_seq, 0), actor, ` +
	`created_at, last_confirmed_at, COALESCE(superseded_by::text, ''), ` +
	`status, COALESCE(confidence, 0)`

// Insert stores a new memory and returns its id. Status is derived,
// not caller-chosen: user-explicit memories activate immediately,
// everything else lands pending for the promotion policy or the
// confirmation queue. Embedding may be empty (backfilled by
// extraction).
func (s *Store) Insert(ctx context.Context, m Memory) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("insert memory: %w", err)
	}
	status := StatusPending
	if m.Actor == ActorUser {
		status = StatusActive
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO memories
		(type, content, embedding, entity_refs, source_session, source_seq, actor, status, confidence)
		VALUES ($1, $2, NULLIF($3, '')::vector, $4, NULLIF($5, '')::uuid, NULLIF($6, 0), $7, $8, $9)
		RETURNING id`,
		m.Type, m.Content, m.Embedding.String(), refs(m.EntityRefs),
		m.SourceSession, m.SourceSeq, actor(m.Actor), status, m.Confidence).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert memory: %w", err)
	}
	return id, nil
}

// Promote moves a pending memory to active.
func (s *Store) Promote(ctx context.Context, id string) error {
	return s.transition(ctx, id, StatusPending, StatusActive, true)
}

// Reject marks a pending memory rejected; it will never be retrieved.
func (s *Store) Reject(ctx context.Context, id string) error {
	return s.transition(ctx, id, StatusPending, StatusRejected, false)
}

// Archive retires an active memory without a successor.
func (s *Store) Archive(ctx context.Context, id string) error {
	return s.transition(ctx, id, StatusActive, StatusArchived, false)
}

// Supersede records that newID replaces oldID: the old row keeps its
// content but is archived and points at its successor. It applies to
// active or pending rows (a correction can land before the original
// was ever confirmed).
func (s *Store) Supersede(ctx context.Context, oldID, newID string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("supersede memory: %w", err)
	}
	tag, err := db.Exec(ctx, `UPDATE memories
		SET superseded_by = $2, status = $3
		WHERE id = $1 AND status IN ($4, $5) AND superseded_by IS NULL`,
		oldID, newID, StatusArchived, StatusActive, StatusPending)
	if err != nil {
		return fmt.Errorf("supersede memory: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("supersede %s: %w", oldID, ErrNotFound)
	}
	return nil
}

// Get returns one memory by id.
func (s *Store) Get(ctx context.Context, id string) (Memory, error) {
	db, err := s.db.Get()
	if err != nil {
		return Memory{}, fmt.Errorf("get memory: %w", err)
	}
	row := db.QueryRow(ctx, `SELECT `+memoryColumns+` FROM memories WHERE id = $1`, id)
	m, err := scanMemory(row)
	if err != nil {
		return Memory{}, fmt.Errorf("get memory %s: %w", id, err)
	}
	return m, nil
}

// ListByStatus returns memories in a lifecycle stage, optionally
// narrowed to types, oldest first (queue order).
func (s *Store) ListByStatus(ctx context.Context, status Status, types ...MemoryType) ([]Memory, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	q := `SELECT ` + memoryColumns + ` FROM memories WHERE status = $1`
	args := []any{status}
	if len(types) > 0 {
		q += ` AND type = ANY($2)`
		names := make([]string, len(types))
		for i, t := range types {
			names[i] = string(t)
		}
		args = append(args, names)
	}
	q += ` ORDER BY created_at`
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("list memories: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Chain walks a supersede chain forward from id and returns every
// memory in order, oldest first. A row that was never superseded
// returns a one-element chain.
func (s *Store) Chain(ctx context.Context, id string) ([]Memory, error) {
	var chain []Memory
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		m, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		chain = append(chain, m)
		id = m.SupersededBy
	}
	return chain, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMemory(r rowScanner) (Memory, error) {
	var m Memory
	err := r.Scan(&m.ID, &m.Type, &m.Content, &m.EntityRefs, &m.SourceSession,
		&m.SourceSeq, &m.Actor, &m.CreatedAt, &m.LastConfirmedAt,
		&m.SupersededBy, &m.Status, &m.Confidence)
	return m, err
}

// transition moves id from one status to another; bump refreshes
// last_confirmed_at (promotion counts as confirmation; rejection and
// archival do not).
func (s *Store) transition(ctx context.Context, id string, from, to Status, bump bool) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("%s memory: %w", to, err)
	}
	q := `UPDATE memories SET status = $2 WHERE id = $1 AND status = $3`
	if bump {
		q = `UPDATE memories SET status = $2, last_confirmed_at = now() WHERE id = $1 AND status = $3`
	}
	tag, err := db.Exec(ctx, q, id, to, from)
	if err != nil {
		return fmt.Errorf("%s memory: %w", to, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s %s: %w", to, id, ErrNotFound)
	}
	return nil
}

// refs normalizes nil to an empty array so the NOT NULL column always
// gets a value.
func refs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// actor defaults empty to "agent" (the column default, made explicit
// so Insert's status derivation and the stored row agree).
func actor(a string) string {
	if a == "" {
		return "agent"
	}
	return a
}
