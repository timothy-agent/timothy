// Package store persists Timothy's long-term memories (D-011). Writes
// are staged: agent-extracted facts land pending and are promoted by
// policy or user confirmation; user-explicit facts activate directly.
// Facts are never updated in place - corrections insert a new row and
// supersede the old one.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

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
// RecentEpisodic returns active episodic memories created since the
// cutoff, newest first - the reflection pass's raw material.
func (s *Store) RecentEpisodic(ctx context.Context, since time.Time, limit int) ([]Memory, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("recent episodic: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+memoryColumns+` FROM memories
		WHERE status = $1 AND type = $2 AND created_at >= $3
		ORDER BY created_at DESC LIMIT $4`,
		StatusActive, TypeEpisodic, since, limit)
	if err != nil {
		return nil, fmt.Errorf("recent episodic: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("recent episodic: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

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

// UpsertEntity returns the id for a (type, name) entity, creating it
// on first sight.
func (s *Store) UpsertEntity(ctx context.Context, typ, name string) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("upsert entity: %w", err)
	}
	var id string
	// DO UPDATE (not DO NOTHING) so RETURNING always yields the row.
	err = db.QueryRow(ctx, `INSERT INTO entities (type, name) VALUES ($1, $2)
		ON CONFLICT (type, name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`, typ, name).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert entity %s/%s: %w", typ, name, err)
	}
	return id, nil
}

// NearestActive returns the closest active memory to the embedding by
// cosine similarity, or ok=false when no active memory has an
// embedding. Extraction uses it for near-duplicate detection.
func (s *Store) NearestActive(ctx context.Context, embedding Vector) (id string, similarity float64, ok bool, err error) {
	db, err := s.db.Get()
	if err != nil {
		return "", 0, false, fmt.Errorf("nearest active: %w", err)
	}
	err = db.QueryRow(ctx, `SELECT id, 1 - (embedding <=> $1::vector)
		FROM memories
		WHERE status = $2 AND embedding IS NOT NULL
		ORDER BY embedding <=> $1::vector
		LIMIT 1`, embedding.String(), StatusActive).Scan(&id, &similarity)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("nearest active: %w", err)
	}
	return id, similarity, true, nil
}

// NearDupPairs returns every pair of active embedded memories of the
// same type whose cosine similarity meets the threshold. The
// consolidation job builds merge groups from these edges - a
// semantic+episodic pair never merges, even at similarity 1.0: they
// answer different questions (a durable fact vs. something that
// happened) and collapsing them silently loses that distinction.
// O(n²) join - fine for a single-user corpus; revisit if the active
// set grows past tens of thousands.
func (s *Store) NearDupPairs(ctx context.Context, threshold float64) ([][2]string, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("near-dup pairs: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT a.id, b.id
		FROM memories a
		JOIN memories b ON a.id < b.id
		WHERE a.status = $1 AND b.status = $1
		  AND a.type = b.type
		  AND a.embedding IS NOT NULL AND b.embedding IS NOT NULL
		  AND 1 - (a.embedding <=> b.embedding) >= $2`,
		StatusActive, threshold)
	if err != nil {
		return nil, fmt.Errorf("near-dup pairs: %w", err)
	}
	defer rows.Close()
	var pairs [][2]string
	for rows.Next() {
		var p [2]string
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			return nil, fmt.Errorf("near-dup pairs: %w", err)
		}
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}

// ApplyMerge inserts the consolidator's merged fact and supersedes
// every member in a single transaction: a crash mid-sequence can no
// longer leave the merged row active alongside still-active members
// (D-011 double-count). The merged row activates directly - it
// replaces confirmed knowledge - with last_confirmed_at defaulting to
// now(). Any member whose Supersede predicate no longer matches (already
// superseded, or no longer active/pending) aborts the whole tx: the
// merged content was computed from a stale read, and the deferred
// Rollback undoes the insert along with every supersede already
// applied this call.
func (s *Store) ApplyMerge(ctx context.Context, m Memory, memberIDs []string) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("apply merge: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("apply merge begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var id string
	err = tx.QueryRow(ctx, `INSERT INTO memories
		(type, content, embedding, entity_refs, source_session, source_seq, actor, status, confidence)
		VALUES ($1, $2, NULLIF($3, '')::vector, $4, NULLIF($5, '')::uuid, NULLIF($6, 0), $7, $8, $9)
		RETURNING id`,
		m.Type, m.Content, m.Embedding.String(), refs(m.EntityRefs),
		m.SourceSession, m.SourceSeq, actor(m.Actor), StatusActive, m.Confidence).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("apply merge insert: %w", err)
	}

	for _, memberID := range memberIDs {
		tag, err := tx.Exec(ctx, `UPDATE memories
			SET superseded_by = $2, status = $3
			WHERE id = $1 AND status IN ($4, $5) AND superseded_by IS NULL`,
			memberID, id, StatusArchived, StatusActive, StatusPending)
		if err != nil {
			return "", fmt.Errorf("apply merge supersede %s: %w", memberID, err)
		}
		if tag.RowsAffected() == 0 {
			return "", fmt.Errorf("apply merge supersede %s: %w", memberID, ErrNotFound)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("apply merge commit: %w", err)
	}
	return id, nil
}

// Confirm bumps an active memory's last_confirmed_at without touching
// its content - lifecycle metadata, not a fact UPDATE (D-011).
// Extraction calls it when a proposed fact turns out to be an exact
// duplicate of an active memory: dropping the duplicate would
// otherwise discard the confirmation signal entirely.
func (s *Store) Confirm(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("confirm memory: %w", err)
	}
	tag, err := db.Exec(ctx, `UPDATE memories SET last_confirmed_at = now()
		WHERE id = $1 AND status = $2`, id, StatusActive)
	if err != nil {
		return fmt.Errorf("confirm memory: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("confirm %s: %w", id, ErrNotFound)
	}
	return nil
}

// ArchiveStaleEpisodic retires active episodic memories neither
// retrieved nor created within the window. Returns how many.
func (s *Store) ArchiveStaleEpisodic(ctx context.Context, olderThan time.Time) (int64, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("archive stale: %w", err)
	}
	tag, err := db.Exec(ctx, `UPDATE memories SET status = $1
		WHERE status = $2 AND type = $3
		  AND created_at < $4
		  AND (last_retrieved_at IS NULL OR last_retrieved_at < $4)`,
		StatusArchived, StatusActive, TypeEpisodic, olderThan)
	if err != nil {
		return 0, fmt.Errorf("archive stale: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DecayStaleSemantic multiplies confidence by factor for active
// semantic memories unconfirmed since the cutoff and returns their
// ids, stalest first (capped) - the reconfirmation queue. Confidence
// is lifecycle metadata; decaying it is not a fact UPDATE (D-011).
func (s *Store) DecayStaleSemantic(ctx context.Context, olderThan time.Time, factor float64, limit int) ([]string, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("decay stale: %w", err)
	}
	rows, err := db.Query(ctx, `UPDATE memories SET confidence = confidence * $4
		WHERE id IN (
			SELECT id FROM memories
			WHERE status = $1 AND type = $2 AND last_confirmed_at < $3
			ORDER BY last_confirmed_at
			LIMIT $5)
		RETURNING id`,
		StatusActive, TypeSemantic, olderThan, factor, limit)
	if err != nil {
		return nil, fmt.Errorf("decay stale: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("decay stale: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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
