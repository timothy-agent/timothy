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
	`COALESCE(supersedes::text, ''), ` +
	`status, COALESCE(confidence, 0), retrieval_hits`

// Insert stores a new memory and returns its id. Status is derived,
// not caller-chosen: user-explicit memories activate immediately
// unless policy requires review; everything else lands pending.
// Embedding may be empty (backfilled by extraction).
func (s *Store) Insert(ctx context.Context, m Memory) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("insert memory: %w", err)
	}
	id, err := insertMemory(ctx, db, m, initialStatus(m))
	if err != nil {
		return "", fmt.Errorf("insert memory: %w", err)
	}
	return id, nil
}

// rowQuerier is the pool or a transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func insertMemory(ctx context.Context, q rowQuerier, m Memory, status Status) (string, error) {
	var id string
	err := q.QueryRow(ctx, `INSERT INTO memories
		(type, content, embedding, entity_refs, source_session, source_seq, actor, status, confidence, supersedes)
		VALUES ($1, $2, NULLIF($3, '')::vector, $4, NULLIF($5, '')::uuid, NULLIF($6, 0), $7, $8, $9, NULLIF($10, '')::uuid)
		RETURNING id`,
		m.Type, m.Content, m.Embedding.String(), refs(m.EntityRefs),
		m.SourceSession, m.SourceSeq, actor(m.Actor), status, m.Confidence, m.Supersedes).Scan(&id)
	return id, err
}

// EntityKey names an entity by its unique (type, name).
type EntityKey struct {
	Type string
	Name string
}

// Proposal is one extracted fact to persist: the memory, the entities
// it references (upserted with it), and whether promotion policy
// activates it on insert.
type Proposal struct {
	Memory   Memory
	Entities []EntityKey
	Promote  bool
}

// ApplyExtraction writes one extraction run in a single transaction
// (D-144): confirmations of restated active rows, entity upserts,
// inserts and promotions. Any failure rolls back the whole run, so no
// half-applied batch and no entity left behind by a failed insert.
// Returns the inserted ids in proposal order.
func (s *Store) ApplyExtraction(ctx context.Context, confirm []string, proposals []Proposal) ([]string, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("apply extraction: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("apply extraction begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if len(confirm) > 0 {
		// A row archived since the dedup read is skipped, not an error.
		if _, err := tx.Exec(ctx, `UPDATE memories SET last_confirmed_at = now()
			WHERE id = ANY($1::uuid[]) AND status = $2`, confirm, StatusActive); err != nil {
			return nil, fmt.Errorf("apply extraction confirm: %w", err)
		}
	}
	ids := make([]string, 0, len(proposals))
	for _, p := range proposals {
		m := p.Memory
		m.EntityRefs = make([]string, 0, len(p.Entities))
		for _, e := range p.Entities {
			id, err := upsertEntity(ctx, tx, e.Type, e.Name)
			if err != nil {
				return nil, fmt.Errorf("apply extraction: %w", err)
			}
			m.EntityRefs = append(m.EntityRefs, id)
		}
		status := initialStatus(m)
		if p.Promote {
			status = StatusActive
		}
		id, err := insertMemory(ctx, tx, m, status)
		if err != nil {
			return nil, fmt.Errorf("apply extraction insert: %w", err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("apply extraction commit: %w", err)
	}
	return ids, nil
}

// normalizedContent is the comparison form for text-equality dedup:
// trimmed, lowercased, whitespace runs collapsed, trailing .!? dropped.
func normalizedContent(expr string) string {
	return `regexp_replace(lower(regexp_replace(btrim(` + expr + `), '\s+', ' ', 'g')), '[.!?]+$', '')`
}

// RejectedWithContent returns a rejected memory whose normalized
// content equals content's: rejected suppression when no embedding is
// available to compare by.
func (s *Store) RejectedWithContent(ctx context.Context, content string) (id string, ok bool, err error) {
	db, err := s.db.Get()
	if err != nil {
		return "", false, fmt.Errorf("rejected with content: %w", err)
	}
	err = db.QueryRow(ctx, `SELECT id::text FROM memories
		WHERE status = $1 AND `+normalizedContent("content")+` = `+normalizedContent("$2")+`
		LIMIT 1`, StatusRejected, content).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("rejected with content: %w", err)
	}
	return id, true, nil
}

func initialStatus(m Memory) Status {
	if m.Actor == ActorUser && !m.RequireReview {
		return StatusActive
	}
	return StatusPending
}

// Promote moves a pending memory to active.
func (s *Store) Promote(ctx context.Context, id string) error {
	return s.transition(ctx, id, StatusPending, StatusActive, true)
}

// ConfirmSuperseding activates a pending correction and retires the
// active row it replaces in one transaction. A stale correction leaves
// both rows untouched.
func (s *Store) ConfirmSuperseding(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("confirm superseding memory: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("confirm superseding memory begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var oldID string
	err = tx.QueryRow(ctx, `SELECT COALESCE(supersedes::text, '') FROM memories
		WHERE id = $1 AND status = $2 FOR UPDATE`, id, StatusPending).Scan(&oldID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("confirm superseding %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("confirm superseding %s: %w", id, err)
	}
	if oldID == "" {
		return fmt.Errorf("confirm superseding %s: %w", id, ErrNotFound)
	}

	if err := retireHead(ctx, tx, oldID, id); err != nil {
		return fmt.Errorf("confirm superseding old memory %s: %w", oldID, err)
	}

	tag, err := tx.Exec(ctx, `UPDATE memories
		SET status = $2, last_confirmed_at = now()
		WHERE id = $1 AND status = $3 AND supersedes = $4`,
		id, StatusActive, StatusPending, oldID)
	if err != nil {
		return fmt.Errorf("activate superseding memory %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("activate superseding memory %s: %w", id, ErrNotFound)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("confirm superseding memory commit: %w", err)
	}
	return nil
}

// CorrectSuperseding inserts an edited, user-confirmed replacement and
// appends it after the pending correction in the supersede chain.
func (s *Store) CorrectSuperseding(ctx context.Context, id string, m Memory) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("correct superseding memory: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("correct superseding memory begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var oldID string
	err = tx.QueryRow(ctx, `SELECT COALESCE(supersedes::text, '') FROM memories
		WHERE id = $1 AND status = $2 FOR UPDATE`, id, StatusPending).Scan(&oldID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("correct superseding %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("correct superseding %s: %w", id, err)
	}
	if oldID == "" {
		return "", fmt.Errorf("correct superseding %s: %w", id, ErrNotFound)
	}

	if err := retireHead(ctx, tx, oldID, id); err != nil {
		return "", fmt.Errorf("correct superseding old memory %s: %w", oldID, err)
	}

	var newID string
	err = tx.QueryRow(ctx, `INSERT INTO memories
		(type, content, embedding, entity_refs, source_session, source_seq, actor, status, confidence)
		VALUES ($1, $2, NULLIF($3, '')::vector, $4, NULLIF($5, '')::uuid, NULLIF($6, 0), $7, $8, $9)
		RETURNING id`,
		m.Type, m.Content, m.Embedding.String(), refs(m.EntityRefs),
		m.SourceSession, m.SourceSeq, actor(m.Actor), StatusActive, m.Confidence).Scan(&newID)
	if err != nil {
		return "", fmt.Errorf("insert corrected memory: %w", err)
	}

	tag, err := tx.Exec(ctx, `UPDATE memories
		SET superseded_by = $2, status = $3
		WHERE id = $1 AND status = $4 AND supersedes = $5`,
		id, newID, StatusArchived, StatusPending, oldID)
	if err != nil {
		return "", fmt.Errorf("archive corrected proposal %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("archive corrected proposal %s: %w", id, ErrNotFound)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("correct superseding memory commit: %w", err)
	}
	return newID, nil
}

// retireHead archives the live end of oldID's supersede chain in favor
// of newID. When an earlier correction already replaced oldID, the chain
// is followed to the row that replaced it; when nothing in the chain is
// still active, there is nothing to retire and the correction simply
// activates.
func retireHead(ctx context.Context, tx pgx.Tx, oldID, newID string) error {
	seen := map[string]bool{}
	for id := oldID; id != "" && !seen[id]; {
		seen[id] = true
		var status Status
		var next string
		err := tx.QueryRow(ctx, `SELECT status, COALESCE(superseded_by::text, '')
			FROM memories WHERE id = $1 FOR UPDATE`, id).Scan(&status, &next)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if status == StatusActive {
			_, err := tx.Exec(ctx, `UPDATE memories SET superseded_by = $2, status = $3
				WHERE id = $1 AND status = $4`, id, newID, StatusArchived, StatusActive)
			return err
		}
		if next == newID {
			return nil
		}
		id = next
	}
	return nil
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
	if errors.Is(err, pgx.ErrNoRows) {
		return Memory{}, fmt.Errorf("get memory %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Memory{}, fmt.Errorf("get memory %s: %w", id, err)
	}
	return m, nil
}

// Contents returns the content of each id that exists, keyed by id, in
// one query (the review queue's supersede comparison).
func (s *Store) Contents(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("memory contents: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT id::text, content FROM memories WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("memory contents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, content string
		if err := rows.Scan(&id, &content); err != nil {
			return nil, fmt.Errorf("memory contents: %w", err)
		}
		out[id] = content
	}
	return out, rows.Err()
}

// HasPendingCorrection reports whether a pending row already proposes
// to supersede id.
func (s *Store) HasPendingCorrection(ctx context.Context, id string) (bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return false, fmt.Errorf("pending correction: %w", err)
	}
	var open bool
	err = db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memories WHERE supersedes = $1 AND status = $2)`,
		id, StatusPending).Scan(&open)
	if err != nil {
		return false, fmt.Errorf("pending correction: %w", err)
	}
	return open, nil
}

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

// Page is a keyset window over created_at DESC, id DESC: Before and
// BeforeID are the previous page's last row. Zero Before means the
// first page; Limit 0 means every row.
type Page struct {
	Before   time.Time
	BeforeID string
	Limit    int
}

// pageClause appends p's cursor predicate, order and limit to a query
// whose WHERE clause already binds args.
func pageClause(p Page, args []any) (string, []any) {
	q := ""
	if !p.Before.IsZero() {
		args = append(args, p.Before, p.BeforeID)
		q += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d::uuid)`, len(args)-1, len(args))
	}
	q += ` ORDER BY created_at DESC, id DESC`
	if p.Limit > 0 {
		args = append(args, p.Limit)
		q += fmt.Sprintf(` LIMIT $%d`, len(args))
	}
	return q, args
}

// ListByStatus returns one page of memories in a lifecycle stage,
// optionally narrowed to types, newest first.
func (s *Store) ListByStatus(ctx context.Context, status Status, page Page, types ...MemoryType) ([]Memory, error) {
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
	clause, args := pageClause(page, args)
	rows, err := db.Query(ctx, q+clause, args...)
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

// CountByStatus returns how many memories are in a lifecycle stage.
func (s *Store) CountByStatus(ctx context.Context, status Status) (int, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("count memories: %w", err)
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM memories WHERE status = $1`, status).Scan(&n); err != nil {
		return 0, fmt.Errorf("count memories: %w", err)
	}
	return n, nil
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
	return upsertEntity(ctx, db, typ, name)
}

func upsertEntity(ctx context.Context, q rowQuerier, typ, name string) (string, error) {
	var id string
	// DO UPDATE (not DO NOTHING) so RETURNING always yields the row.
	err := q.QueryRow(ctx, `INSERT INTO entities (type, name) VALUES ($1, $2)
		ON CONFLICT (type, name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`, typ, name).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert entity %s/%s: %w", typ, name, err)
	}
	return id, nil
}

// NearestActive returns the closest active, pending, or rejected
// memory to the embedding by cosine similarity, or ok=false when no
// such memory has an embedding. Pending rows must be visible too:
// dedup used to only see active rows, so a fact re-extracted before
// its first proposal was confirmed or promoted never found its match
// and near-duplicates piled up in the confirmation queue. Rejected
// rows must be visible for the same reason in the other direction:
// rejection is a durable teaching signal, so a candidate matching a
// rejected row is dropped instead of re-proposed forever. status
// tells the caller which branch applies - only an active match can be
// Confirmed (Confirm's UPDATE is active-only).
func (s *Store) NearestActive(ctx context.Context, embedding Vector) (id string, similarity float64, status Status, ok bool, err error) {
	db, err := s.db.Get()
	if err != nil {
		return "", 0, "", false, fmt.Errorf("nearest active: %w", err)
	}
	err = db.QueryRow(ctx, `SELECT id, 1 - (embedding <=> $1::vector), status
		FROM memories
		WHERE status IN ($2, $3, $4) AND embedding IS NOT NULL
		ORDER BY embedding <=> $1::vector
		LIMIT 1`, embedding.String(), StatusActive, StatusPending, StatusRejected).Scan(&id, &similarity, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, "", false, nil
	}
	if err != nil {
		return "", 0, "", false, fmt.Errorf("nearest active: %w", err)
	}
	return id, similarity, status, true, nil
}

// NearestActiveOnly returns the closest active memory. Extraction uses
// this before the broader pending/rejected lookup so a pending proposal
// cannot hide a correction to active knowledge.
func (s *Store) NearestActiveOnly(ctx context.Context, embedding Vector) (id string, similarity float64, ok bool, err error) {
	db, err := s.db.Get()
	if err != nil {
		return "", 0, false, fmt.Errorf("nearest active memory: %w", err)
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
		return "", 0, false, fmt.Errorf("nearest active memory: %w", err)
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

// RedundantPendingPairs returns every pair of pending embedded
// memories of the same type whose cosine similarity meets the
// threshold, older id first: unlike NearDupPairs (active-only, feeds
// the LLM merge path), this catches two still-unconfirmed proposals
// restating the same fact in different words before either reaches
// the confirm queue twice. No LLM merge needed here - neither side is
// confirmed knowledge yet, so the consolidator just rejects the newer
// half of each pair outright.
func (s *Store) RedundantPendingPairs(ctx context.Context, threshold float64) ([][2]string, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("redundant pending pairs: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT a.id, b.id
		FROM memories a
		JOIN memories b ON a.created_at < b.created_at
		WHERE a.status = $1 AND b.status = $1
		  AND a.type = b.type
		  AND a.embedding IS NOT NULL AND b.embedding IS NOT NULL
		  AND 1 - (a.embedding <=> b.embedding) >= $2`,
		StatusPending, threshold)
	if err != nil {
		return nil, fmt.Errorf("redundant pending pairs: %w", err)
	}
	defer rows.Close()
	var pairs [][2]string
	for rows.Next() {
		var p [2]string
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			return nil, fmt.Errorf("redundant pending pairs: %w", err)
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

// DemoteUnused archives pending memories that were never retrieved,
// never confirmed (still pending: confirmation is Promote to active),
// low confidence, and older than the cutoff. The last_retrieved_at
// IS NULL guard keeps rows retrieved before retrieval_hits existed
// immune despite their zero counter. This never overwrites content,
// only closes the row out (D-011). Returns the demoted ids.
func (s *Store) DemoteUnused(ctx context.Context, olderThan time.Time, confidenceBelow float64, limit int) ([]string, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("demote unused: %w", err)
	}
	rows, err := db.Query(ctx, `UPDATE memories SET status = $1
		WHERE id IN (
			SELECT id FROM memories
			WHERE status = $2 AND retrieval_hits = 0 AND last_retrieved_at IS NULL
			  AND confidence < $3 AND created_at < $4
			ORDER BY created_at
			LIMIT $5)
		RETURNING id`,
		StatusArchived, StatusPending, confidenceBelow, olderThan, limit)
	if err != nil {
		return nil, fmt.Errorf("demote unused: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("demote unused: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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
		&m.SupersededBy, &m.Supersedes, &m.Status, &m.Confidence, &m.RetrievalHits)
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
