package store

import (
	"context"
	"fmt"
	"sort"

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
// brain's own (internal/brain/kb): memoryd only writes kb_chunks and
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
		SET status = 'ready', chunk_count = $2, error = '', retry_count = 0, next_retry_at = NULL,
			ingested_at = now(), updated_at = now()
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
// the new set in one transaction: re-ingest is delete-and-rewrite,
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
	Provenance    string
}

// KBSearchMode selects which leg(s) KBSearch runs.
type KBSearchMode string

const (
	KBSearchHybrid   KBSearchMode = "hybrid"
	KBSearchSemantic KBSearchMode = "semantic"
	KBSearchKeyword  KBSearchMode = "keyword"
)

// legLimitLit/rrfKLit are string literals (not ints) so they concatenate
// directly into the const SQL below: legLimit bounds each leg's
// candidate set before RRF fusion, rrfK is the standard Reciprocal Rank
// Fusion damping constant; both mirror the memories retrieval package's
// own values (internal/memory/retrieval/search.go, fuse.go).
const legLimitLit = "30"
const rrfKLit = "60"

// vecLegWeightLit/ftsLegWeightLit (D-083, issue #424) scale each leg's
// RRF contribution before the legs sum into a fused score (KB hybrid
// search fuses exactly these two legs, vector and keyword; there is
// no third entity leg here, that only exists in the separate memories
// retrieval package). The keyword (fts) leg is OR-semantics ts_rank
// with no similarity floor (issue #425 covers the tsquery itself), so
// generic word overlap wins it high ranks on off-topic documents;
// discounting its contribution relative to the vector leg lets
// on-topic documents outrank bystanders whose overlap is lexical
// only. Equal weights (1.0/1.0) reproduce prior behavior. Tuned
// against make kb-eval (recall@5 stays 1.0). Fusion weights only
// re-rank across legs: a bystander chunk that also wins the raw
// vector leg outright cannot be reversed by any leg weight, so the
// per-document cap (D-082) remains the guard for that case.
const vecLegWeightLit = "1.0"
const ftsLegWeightLit = "0.1"

// minSimilarityLit is the vector leg's relevance floor (cosine
// similarity): below it a chunk is noise, not a match. Nearest-neighbor
// search always returns SOMETHING: without a floor an off-topic query
// still fills k slots with whatever is least-far away, and the model
// may cite it. Returning nothing beats confidently-irrelevant (same
// principle as memories retrieval). The keyword leg needs no floor: a
// tsquery either matches lexemes or returns nothing.
const minSimilarityLit = "0.25"

// boostFactorLit multiplies a chunk's score when its collection is in
// boostCollections (D-078: an agent's configured collections are a
// ranking boost, not an access gate: issue #368). A plain literal, not
// a config knob: no existing settings pattern fits one retrieval-tuning
// constant.
const boostFactorLit = "1.5"

// provenanceWeight*Lit multiply a chunk's score by its document's
// provenance tier (D-080, issue #372): curated (operator-vetted) stays
// at parity, mission-generated content is discounted, web-clipped
// content discounted further, so a comparably-relevant model-written
// or web-clipped doc never silently outranks a curated one and the
// KB can't reinforce its own errors. Values are spaced widely enough
// to reorder same-relevance docs across tiers (fused RRF scores for
// hits at similar ranks differ by a few percent at most, see the
// golden tests) but not so steep that an off-tier doc with a
// meaningfully higher fused score gets buried under an unrelated
// curated one.
const (
	provenanceWeightCuratedLit = "1.0"
	provenanceWeightMissionLit = "0.8"
	provenanceWeightWebLit     = "0.6"
)

// kbPerDocumentCap bounds how many chunks of one document can land in
// top-k (D-082, issue #419): fusion is per chunk, so a long relevant
// document can fill every slot and starve the rest of the corpus. Cap
// drops, never reorders; freed slots go to the next-ranked chunks of
// other documents, and a two-pass backfill still fills k when the
// corpus is too narrow to satisfy the cap.
const kbPerDocumentCap = 2

// kbCandidateFetchMultiplier over-fetches from SQL so the Go-side cap
// has enough lower-ranked candidates to promote after dropping
// over-cap chunks, without a second round trip.
const kbCandidateFetchMultiplier = 3

// KBSearch runs hybrid (vector + full-text, RRF-fused), semantic-only,
// or keyword-only retrieval over kb_chunks. Empty collectionNames
// searches the whole knowledge base; non-empty scopes to it (an
// explicit narrowing, still enforced here in SQL, never a prompt).
// boostCollections gets a score multiplier at ranking time regardless
// of collectionNames: a relevance signal, never a filter: chunks
// outside it are still returned. embedding may be nil only when mode is
// keyword; hybrid/semantic without an embedding return no results from
// the vector leg (callers should not call semantic/hybrid without one).
// Results are capped at kbPerDocumentCap chunks per document (D-082),
// backfilled to k when the corpus can't otherwise fill it.
func (s *KBStore) KBSearch(ctx context.Context, query string, embedding Vector, collectionNames, boostCollections []string, mode KBSearchMode, k int) ([]KBSearchHit, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("kb search: %w", err)
	}
	if k <= 0 {
		k = 8
	}
	fetchN := k * kbCandidateFetchMultiplier

	var sql string
	var args []any
	switch mode {
	case KBSearchSemantic:
		sql, args = semanticSQL, []any{embedding.String(), collectionNames, fetchN, boostCollections}
	case KBSearchKeyword:
		sql, args = keywordSQL, []any{query, collectionNames, fetchN, boostCollections}
	default:
		sql, args = hybridSQL, []any{embedding.String(), query, collectionNames, fetchN, boostCollections}
	}

	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("kb search: %w", err)
	}
	defer rows.Close()

	var candidates []KBSearchHit
	for rows.Next() {
		var h KBSearchHit
		if err := rows.Scan(&h.ChunkID, &h.DocumentID, &h.DocumentTitle, &h.Collection,
			&h.Breadcrumb, &h.Content, &h.SourceRef, &h.Provenance, &h.Score); err != nil {
			return nil, fmt.Errorf("kb search: %w", err)
		}
		candidates = append(candidates, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return applyPerDocumentCap(candidates, k), nil
}

// applyPerDocumentCap takes candidates (already score-descending) and
// selects up to k, never more than kbPerDocumentCap per document. First
// pass: walk candidates in order, keep a hit if its document is still
// under cap. Second pass: if the cap left fewer than min(k, len(candidates))
// selected, backfill remaining slots from the leftover candidates in
// score order regardless of document. Both passes preserve the input's
// score-descending order within their own selections; the final result
// is re-sorted by score so backfilled hits merge in at the right rank.
func applyPerDocumentCap(candidates []KBSearchHit, k int) []KBSearchHit {
	if len(candidates) == 0 {
		return nil
	}
	perDoc := make(map[string]int, len(candidates))
	selected := make([]KBSearchHit, 0, k)
	var leftover []KBSearchHit
	for _, h := range candidates {
		if len(selected) < k && perDoc[h.DocumentID] < kbPerDocumentCap {
			selected = append(selected, h)
			perDoc[h.DocumentID]++
		} else {
			leftover = append(leftover, h)
		}
	}
	want := k
	if len(candidates) < want {
		want = len(candidates)
	}
	for _, h := range leftover {
		if len(selected) >= want {
			break
		}
		selected = append(selected, h)
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].Score > selected[j].Score })
	return selected
}

const (
	// kbProjection joins chunk -> document -> collection; col.name backs
	// both the optional collection filter and the boost multiplier.
	kbProjection = `SELECT c.id, d.id, d.title, col.name, c.breadcrumb, c.content, d.source_ref, d.provenance`

	// kbQueryCTEq1/q2 normalize the query's lexemes and OR them
	// together: same rationale as memories retrieval's textSQL: a
	// multi-topic question must match each chunk that answers PART of
	// it, which websearch/plainto AND-semantics would return nothing
	// for. Lexemes are quoted (with '' doubling) so none can break the
	// tsquery syntax. Two variants because the query text is $1 in
	// keyword mode and $2 in hybrid.
	kbQueryCTEq1 = `SELECT to_tsquery('english',
			string_agg('''' || replace(lexeme, '''', '''''') || '''', ' | ')) AS query
		FROM unnest(tsvector_to_array(to_tsvector('english', $1))) AS lexeme`
	kbQueryCTEq2 = `SELECT to_tsquery('english',
			string_agg('''' || replace(lexeme, '''', '''''') || '''', ' | ')) AS query
		FROM unnest(tsvector_to_array(to_tsvector('english', $2))) AS lexeme`

	// collectionFilter matches every row when names is empty/null
	// (whole-KB search), or scopes to it when non-empty (an explicit
	// narrowing, still enforced in SQL).
	collectionFilterQ2 = `(array_length($2::text[], 1) IS NULL OR col.name = ANY($2))`
	collectionFilterQ3 = `(array_length($3::text[], 1) IS NULL OR col.name = ANY($3))`

	// boostMultiplier scores a boosted-collection chunk higher without
	// excluding anything else: CASE, not a WHERE filter.
	boostMultiplierQ4 = `(CASE WHEN col.name = ANY($4::text[]) THEN ` + boostFactorLit + ` ELSE 1 END)`
	boostMultiplierQ5 = `(CASE WHEN col.name = ANY($5::text[]) THEN ` + boostFactorLit + ` ELSE 1 END)`

	// provenanceMultiplier weights by document provenance (D-080):
	// composes with boostMultiplier by plain multiplication, applied
	// once post-fusion alongside it, same as the boost.
	provenanceMultiplier = `(CASE d.provenance
		WHEN 'mission' THEN ` + provenanceWeightMissionLit + `
		WHEN 'web' THEN ` + provenanceWeightWebLit + `
		ELSE ` + provenanceWeightCuratedLit + ` END)`

	semanticSQL = kbProjection + `, (1 - (c.embedding <=> $1::vector)) * ` + boostMultiplierQ4 + ` * ` + provenanceMultiplier + ` AS score
		FROM kb_chunks c
		JOIN kb_documents d ON d.id = c.document_id
		JOIN kb_collections col ON col.id = d.collection_id
		WHERE ` + collectionFilterQ2 + ` AND c.embedding IS NOT NULL
		  AND 1 - (c.embedding <=> $1::vector) >= ` + minSimilarityLit + `
		ORDER BY score DESC
		LIMIT $3`

	keywordSQL = `WITH q AS (` + kbQueryCTEq1 + `)
		` + kbProjection + `, ts_rank(c.tsv, q.query) * ` + boostMultiplierQ4 + ` * ` + provenanceMultiplier + ` AS score
		FROM kb_chunks c
		JOIN kb_documents d ON d.id = c.document_id
		JOIN kb_collections col ON col.id = d.collection_id, q
		WHERE ` + collectionFilterQ2 + ` AND c.tsv @@ q.query
		ORDER BY score DESC
		LIMIT $3`

	// hybridSQL fuses a vector top-30 and an FTS top-30 leg via
	// Reciprocal Rank Fusion (k=60), same fusion constant as the
	// memories retrieval package, with each leg's contribution scaled
	// by vecLegWeightLit/ftsLegWeightLit before summing (D-083). Each
	// leg carries its own relevance gate (similarity floor / lexeme
	// match), so an off-topic query yields an empty result instead of
	// the k least-far chunks. The boost multiplies the fused score,
	// applied once at the end (not per-leg) so it can't double-count a
	// chunk found by both legs.
	// $1 embedding, $2 query, $3 collection names, $4 result count,
	// $5 boost collection names.
	hybridSQL = `WITH q AS (` + kbQueryCTEq2 + `), vec AS (
			SELECT c.id, row_number() OVER (ORDER BY c.embedding <=> $1::vector) AS rank
			FROM kb_chunks c
			JOIN kb_documents d ON d.id = c.document_id
			JOIN kb_collections col ON col.id = d.collection_id
			WHERE ` + collectionFilterQ3 + ` AND c.embedding IS NOT NULL
			  AND 1 - (c.embedding <=> $1::vector) >= ` + minSimilarityLit + `
			ORDER BY c.embedding <=> $1::vector
			LIMIT ` + legLimitLit + `
		), fts AS (
			SELECT c.id, row_number() OVER (ORDER BY ts_rank(c.tsv, q.query) DESC) AS rank
			FROM kb_chunks c
			JOIN kb_documents d ON d.id = c.document_id
			JOIN kb_collections col ON col.id = d.collection_id, q
			WHERE ` + collectionFilterQ3 + ` AND c.tsv @@ q.query
			ORDER BY rank
			LIMIT ` + legLimitLit + `
		), fused AS (
			SELECT id, sum(weight / (` + rrfKLit + ` + rank)) AS score
			FROM (
				SELECT id, rank, ` + vecLegWeightLit + `::float8 AS weight FROM vec
				UNION ALL
				SELECT id, rank, ` + ftsLegWeightLit + `::float8 AS weight FROM fts
			) legs
			GROUP BY id
		)
		` + kbProjection + `, fused.score * ` + boostMultiplierQ5 + ` * ` + provenanceMultiplier + ` AS score
		FROM fused
		JOIN kb_chunks c ON c.id = fused.id
		JOIN kb_documents d ON d.id = c.document_id
		JOIN kb_collections col ON col.id = d.collection_id
		ORDER BY score DESC
		LIMIT $4`
)
