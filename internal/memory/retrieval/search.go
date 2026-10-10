// Package retrieval answers "what does Timothy remember about X" by
// fusing three search legs — vector k-NN, full-text, and entity match
// — over one Postgres (D-011). Results are scored, thresholded
// (returning nothing beats confidently-stale), and packed to a token
// budget.
package retrieval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/SumonMSelim/timothy/internal/memory/store"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Candidate is one memory with the legs that surfaced it.
type Candidate struct {
	ID              string
	Type            store.MemoryType
	Content         string
	LastConfirmedAt time.Time
	LastRetrievedAt time.Time // zero: never retrieved
	Confidence      float32
	// rank per leg name; missing key = leg didn't surface it
	ranks map[string]int
}

// NewCandidate assembles a full-confidence candidate with explicit leg
// ranks, for tests and callers that don't go through Search.
func NewCandidate(id string, typ store.MemoryType, content string, lastConfirmed time.Time, ranks map[string]int) *Candidate {
	return &Candidate{ID: id, Type: typ, Content: content, LastConfirmedAt: lastConfirmed, Confidence: 1, ranks: ranks}
}

// Metrics observes each leg; either field may be nil (tests).
type Metrics struct {
	LegDuration *prometheus.HistogramVec // leg: vector|text|entity
	LegErrors   *prometheus.CounterVec   // leg: vector|text|entity
}

// Searcher runs the three legs.
type Searcher struct {
	db      *pgpool.Pool
	log     *slog.Logger
	metrics Metrics
}

func NewSearcher(db *pgpool.Pool, log *slog.Logger, m Metrics) *Searcher {
	return &Searcher{db: db, log: log, metrics: m}
}

// observe records one leg run's duration and outcome.
func (s *Searcher) observe(leg string, start time.Time, err error) {
	if s.metrics.LegDuration != nil {
		s.metrics.LegDuration.WithLabelValues(leg).Observe(time.Since(start).Seconds())
	}
	if err != nil && s.metrics.LegErrors != nil {
		s.metrics.LegErrors.WithLabelValues(leg).Inc()
	}
}

// Search runs all legs in parallel and merges their hits into one
// candidate set. embedding may be empty (vector leg skipped — the
// other legs still answer); types narrows the corpus. One leg failing
// must not sink the others — partial recall beats none — so per-leg
// errors only log and Search errors only when EVERY leg failed.
func (s *Searcher) Search(ctx context.Context, query string, embedding store.Vector, types []store.MemoryType) (map[string]*Candidate, error) {
	m := &merger{into: make(map[string]*Candidate)}

	type legRun struct {
		name string
		sql  string
		args []any
	}
	legs := []legRun{
		{"text", textSQL, []any{query}},
		{"entity", entitySQL, []any{query}},
	}
	if len(embedding) > 0 {
		legs = append(legs, legRun{"vector", vectorSQL, []any{embedding.String(), 1 - minSimilarity}})
	}

	var wg sync.WaitGroup
	errs := make([]error, len(legs))
	for i, l := range legs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			errs[i] = s.leg(ctx, l.name, l.sql, l.args, types, m)
			s.observe(l.name, start, errs[i])
		}()
	}
	wg.Wait()

	failed := 0
	for _, err := range errs {
		if err != nil {
			failed++
			s.log.Warn("retrieval leg failed; continuing with the others", "error", err)
		}
	}
	if failed == len(legs) {
		return nil, errors.Join(errs...)
	}
	return m.into, nil
}

// minSimilarity is the vector leg's cosine floor (D-149): k-NN always
// returns k rows, so without it an off-topic turn gets the nearest
// anything. 0.25 matches the KB floor on the same embedding model.
const minSimilarity = 0.25

// Leg queries share a projection and differ only in match + order.
// All see active memories exclusively, minus decayed ones below the
// floor (D-147). $1 is the leg's own parameter; $2 is the optional
// type filter (empty array = all types); $3 is store.DecayFloor.
const (
	// $4 is the max cosine distance. The floor filters outside the
	// LIMIT so the index scan stays a plain k-NN.
	vectorSQL = `SELECT id, type, content, last_confirmed_at, last_retrieved_at, confidence
		FROM (
			SELECT id, type, content, last_confirmed_at, last_retrieved_at,
				COALESCE(confidence, 0) AS confidence, embedding <=> $1::vector AS dist
			FROM memories
			WHERE status = 'active' AND embedding IS NOT NULL
			  AND (decayed_at IS NULL OR confidence >= $3)
			  AND (cardinality($2::text[]) = 0 OR type = ANY($2))
			ORDER BY dist
			LIMIT ` + limitLit + `) nn
		WHERE dist <= $4
		ORDER BY dist`

	// The query's normalized lexemes OR together: a question spanning
	// several topics ("what seat do I prefer and when is my birthday")
	// must match each fact that answers PART of it — websearch/plainto
	// AND-semantics return nothing for multi-topic questions. ts_rank
	// orders by how much of the question a memory answers. Lexemes are
	// quoted (with '' doubling) so none can break the tsquery syntax.
	textSQL = `WITH q AS (
			SELECT to_tsquery('english',
				string_agg('''' || replace(lexeme, '''', '''''') || '''', ' | ')) AS query
			FROM unnest(tsvector_to_array(to_tsvector('english', $1))) AS lexeme
		)
		SELECT id, type, content, last_confirmed_at, last_retrieved_at, COALESCE(confidence, 0)
		FROM memories, q
		WHERE status = 'active'
		  AND (decayed_at IS NULL OR confidence >= $3)
		  AND tsv @@ q.query
		  AND (cardinality($2::text[]) = 0 OR type = ANY($2))
		ORDER BY ts_rank(tsv, q.query) DESC
		LIMIT ` + limitLit

	// Entities named in the query pull in every memory that references
	// them, case-insensitively. D-149: a name shorter than 4 characters
	// must stand alone (no letter or digit on either side, so "Go"
	// skips "good"); its other characters are escaped for the regex.
	// Longer names match as substrings. Memories citing more matched
	// entities rank first, then those citing the longest matched name
	// (the most specific), recency only breaks ties.
	entitySQL = `WITH hit AS (
			SELECT e.id, char_length(e.name) AS len
			FROM entities e
			WHERE e.name <> '' AND CASE WHEN char_length(e.name) < 4
				THEN lower($1) ~ ('(^|[^[:alnum:]])'
					|| regexp_replace(lower(e.name), '([^[:alnum:][:space:]])', '\\\1', 'g')
					|| '([^[:alnum:]]|$)')
				ELSE position(lower(e.name) IN lower($1)) > 0 END
		)
		SELECT m.id, m.type, m.content, m.last_confirmed_at, m.last_retrieved_at, COALESCE(m.confidence, 0)
		FROM memories m
		CROSS JOIN LATERAL (
			SELECT count(*) AS n, max(h.len) AS best
			FROM hit h WHERE h.id = ANY(m.entity_refs)) q
		WHERE m.status = 'active'
		  AND (m.decayed_at IS NULL OR m.confidence >= $3)
		  AND (cardinality($2::text[]) = 0 OR m.type = ANY($2))
		  AND m.entity_refs && ARRAY(SELECT id FROM hit)
		ORDER BY q.n DESC, q.best DESC, m.last_confirmed_at DESC, m.id
		LIMIT ` + limitLit

	limitLit = "30"
)

// merger serializes leg results into the shared candidate map.
type merger struct {
	mu   sync.Mutex
	into map[string]*Candidate
}

func (s *Searcher) leg(ctx context.Context, name, sql string, args []any, types []store.MemoryType, m *merger) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("retrieval %s leg: %w", name, err)
	}
	typeNames := make([]string, len(types))
	for i, t := range types {
		typeNames[i] = string(t)
	}
	params := append([]any{args[0], typeNames, store.DecayFloor}, args[1:]...)
	merge := func(rows pgx.Rows, err error) error {
		if err != nil {
			return err
		}
		defer rows.Close()
		rank := 0
		for rows.Next() {
			var c Candidate
			var retrieved *time.Time
			if err := rows.Scan(&c.ID, &c.Type, &c.Content, &c.LastConfirmedAt, &retrieved, &c.Confidence); err != nil {
				return err
			}
			if retrieved != nil {
				c.LastRetrievedAt = *retrieved
			}
			rank++
			m.mu.Lock()
			if existing, ok := m.into[c.ID]; ok {
				existing.ranks[name] = rank
			} else {
				c.ranks = map[string]int{name: rank}
				m.into[c.ID] = &c
			}
			m.mu.Unlock()
		}
		return rows.Err()
	}
	if name == "vector" {
		err = store.WithIterativeScan(ctx, db, func(tx pgx.Tx) error {
			return merge(tx.Query(ctx, sql, params...))
		})
	} else {
		err = merge(db.Query(ctx, sql, params...))
	}
	if err != nil {
		return fmt.Errorf("retrieval %s leg: %w", name, err)
	}
	return nil
}

// MarkRetrieved stamps last_retrieved_at and bumps retrieval_hits on
// the returned memories so the consolidation job can see what is
// actually used (archival window and usage-driven decay,
// memory-extraction-v2 slice 5) and Fuse can keep used facts fresh
// (D-148). This is metadata bookkeeping, deliberately NOT a supersede
// or last_confirmed_at bump (D-011); memory content stays
// supersede-only. Failures only log: retrieval already succeeded.
func (s *Searcher) MarkRetrieved(ctx context.Context, ids []string) {
	if len(ids) == 0 {
		return
	}
	db, err := s.db.Get()
	if err != nil {
		s.log.Warn("mark retrieved skipped", "error", err)
		return
	}
	if _, err := db.Exec(ctx,
		`UPDATE memories SET last_retrieved_at = now(),
			retrieval_hits = retrieval_hits + 1
			WHERE id = ANY($1)`, ids); err != nil {
		s.log.Warn("mark retrieved failed", "error", err, "ids", strings.Join(ids, ","))
	}
}
