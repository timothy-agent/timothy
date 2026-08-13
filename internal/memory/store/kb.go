package store

import (
	"context"
	"fmt"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// KBChunk is one chunk row ready to insert during ingestion.
type KBChunk struct {
	Seq            int
	Breadcrumb     string
	Content        string
	Embedding      Vector
	EmbeddingModel string
}

// KBStore persists knowledge-base chunks (D-060) and reports ingestion
// outcomes onto brain's kb_documents row. Collection/document CRUD is
// brain's own (internal/brain/kb) — memoryd only writes kb_chunks and
// the narrow status/chunk_count/error columns ingestion owns, sharing
// the one Postgres instance (D-060 doesn't split the database, just
// the code that owns each table).
type KBStore struct {
	db *pgpool.Pool
}

func NewKBStore(db *pgpool.Pool) *KBStore {
	return &KBStore{db: db}
}

// SetIngested marks a document ready after a successful ingest.
func (s *KBStore) SetIngested(ctx context.Context, documentID string, chunkCount int) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb set ingested: %w", err)
	}
	_, err = db.Exec(ctx, `UPDATE kb_documents
		SET status = 'ready', chunk_count = $2, error = '', ingested_at = now(), updated_at = now()
		WHERE id = $1`, documentID, chunkCount)
	if err != nil {
		return fmt.Errorf("kb set ingested: %w", err)
	}
	return nil
}

// SetFailed marks a document failed with the given error message.
func (s *KBStore) SetFailed(ctx context.Context, documentID string, errMsg string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb set failed: %w", err)
	}
	_, err = db.Exec(ctx, `UPDATE kb_documents
		SET status = 'failed', error = $2, updated_at = now()
		WHERE id = $1`, documentID, errMsg)
	if err != nil {
		return fmt.Errorf("kb set failed: %w", err)
	}
	return nil
}

// ReplaceChunks deletes every existing chunk for documentID and inserts
// the new set in one transaction — re-ingest is delete-and-rewrite,
// never an in-place update (KB documents are mutable reference data,
// but a chunk's identity is only meaningful within one ingestion pass).
func (s *KBStore) ReplaceChunks(ctx context.Context, documentID string, chunks []KBChunk) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb replace chunks: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("kb replace chunks begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `DELETE FROM kb_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("kb replace chunks delete: %w", err)
	}
	for _, c := range chunks {
		if _, err := tx.Exec(ctx, `INSERT INTO kb_chunks
			(document_id, seq, breadcrumb, content, embedding, embedding_model)
			VALUES ($1, $2, $3, $4, NULLIF($5, '')::vector, $6)`,
			documentID, c.Seq, c.Breadcrumb, c.Content, c.Embedding.String(), c.EmbeddingModel); err != nil {
			return fmt.Errorf("kb replace chunks insert: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("kb replace chunks commit: %w", err)
	}
	return nil
}

// KBSearchHit is one hybrid-retrieval result row.
type KBSearchHit struct {
	ChunkID       string
	DocumentID    string
	DocumentTitle string
	Collection    string
	Breadcrumb    string
	Content       string
	Score         float64
	SourceRef     string
}

// KBSearchMode selects which leg(s) KBSearch runs.
type KBSearchMode string

const (
	KBSearchHybrid   KBSearchMode = "hybrid"
	KBSearchSemantic KBSearchMode = "semantic"
	KBSearchKeyword  KBSearchMode = "keyword"
)

// legLimitLit/rrfKLit are string literals (not ints) so they concatenate
// directly into the const SQL below — legLimit bounds each leg's
// candidate set before RRF fusion, rrfK is the standard Reciprocal Rank
// Fusion damping constant; both mirror the memories retrieval package's
// own values (internal/memory/retrieval/search.go, fuse.go).
const legLimitLit = "30"
const rrfKLit = "60"

// KBSearch runs hybrid (vector + full-text, RRF-fused), semantic-only,
// or keyword-only retrieval over kb_chunks, scoped to collectionNames
// (required, non-empty — collection scoping is enforced here in SQL,
// never left to a prompt). embedding may be nil only when mode is
// keyword; hybrid/semantic without an embedding return no results from
// the vector leg (callers should not call semantic/hybrid without one).
func (s *KBStore) KBSearch(ctx context.Context, query string, embedding Vector, collectionNames []string, mode KBSearchMode, k int) ([]KBSearchHit, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("kb search: %w", err)
	}
	if len(collectionNames) == 0 {
		return nil, fmt.Errorf("kb search: collection_names is required")
	}
	if k <= 0 {
		k = 8
	}

	var sql string
	var args []any
	switch mode {
	case KBSearchSemantic:
		sql, args = semanticSQL, []any{embedding.String(), collectionNames, k}
	case KBSearchKeyword:
		sql, args = keywordSQL, []any{query, collectionNames, k}
	default:
		sql, args = hybridSQL, []any{embedding.String(), query, collectionNames, k}
	}

	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("kb search: %w", err)
	}
	defer rows.Close()

	var out []KBSearchHit
	for rows.Next() {
		var h KBSearchHit
		if err := rows.Scan(&h.ChunkID, &h.DocumentID, &h.DocumentTitle, &h.Collection,
			&h.Breadcrumb, &h.Content, &h.SourceRef, &h.Score); err != nil {
			return nil, fmt.Errorf("kb search: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

const (
	// kbProjection joins chunk -> document -> collection, scoped by
	// collection name — the sole point collection access control is
	// enforced (D-060: Go code, never a prompt).
	kbProjection = `SELECT c.id, d.id, d.title, col.name, c.breadcrumb, c.content, d.source_ref`

	semanticSQL = kbProjection + `, 1 - (c.embedding <=> $1::vector) AS score
		FROM kb_chunks c
		JOIN kb_documents d ON d.id = c.document_id
		JOIN kb_collections col ON col.id = d.collection_id
		WHERE col.name = ANY($2) AND c.embedding IS NOT NULL
		ORDER BY c.embedding <=> $1::vector
		LIMIT $3`

	keywordSQL = kbProjection + `, ts_rank(c.tsv, websearch_to_tsquery('english', $1)) AS score
		FROM kb_chunks c
		JOIN kb_documents d ON d.id = c.document_id
		JOIN kb_collections col ON col.id = d.collection_id
		WHERE col.name = ANY($2) AND c.tsv @@ websearch_to_tsquery('english', $1)
		ORDER BY score DESC
		LIMIT $3`

	// hybridSQL fuses a vector top-30 and an FTS top-30 leg via
	// Reciprocal Rank Fusion (k=60), same fusion constant as the
	// memories retrieval package. $1 embedding, $2 query, $3 collection
	// names, $4 result count.
	hybridSQL = `WITH vec AS (
			SELECT c.id, row_number() OVER (ORDER BY c.embedding <=> $1::vector) AS rank
			FROM kb_chunks c
			JOIN kb_documents d ON d.id = c.document_id
			JOIN kb_collections col ON col.id = d.collection_id
			WHERE col.name = ANY($3) AND c.embedding IS NOT NULL
			ORDER BY c.embedding <=> $1::vector
			LIMIT ` + legLimitLit + `
		), fts AS (
			SELECT c.id, row_number() OVER (ORDER BY ts_rank(c.tsv, websearch_to_tsquery('english', $2)) DESC) AS rank
			FROM kb_chunks c
			JOIN kb_documents d ON d.id = c.document_id
			JOIN kb_collections col ON col.id = d.collection_id
			WHERE col.name = ANY($3) AND c.tsv @@ websearch_to_tsquery('english', $2)
			ORDER BY rank
			LIMIT ` + legLimitLit + `
		), fused AS (
			SELECT id, sum(1.0 / (` + rrfKLit + ` + rank)) AS score
			FROM (SELECT id, rank FROM vec UNION ALL SELECT id, rank FROM fts) legs
			GROUP BY id
		)
		` + kbProjection + `, fused.score
		FROM fused
		JOIN kb_chunks c ON c.id = fused.id
		JOIN kb_documents d ON d.id = c.document_id
		JOIN kb_collections col ON col.id = d.collection_id
		ORDER BY fused.score DESC
		LIMIT $4`
)
