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

	"github.com/SumonMSelim/timothy/internal/memory/store"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Candidate is one memory with the legs that surfaced it.
type Candidate struct {
	ID              string
	Type            store.MemoryType
	Content         string
	LastConfirmedAt time.Time
	// rank per leg name; missing key = leg didn't surface it
	ranks map[string]int
}

// NewCandidate assembles a candidate with explicit leg ranks — for
// tests and callers that don't go through Search.
func NewCandidate(id string, typ store.MemoryType, content string, lastConfirmed time.Time, ranks map[string]int) *Candidate {
	return &Candidate{ID: id, Type: typ, Content: content, LastConfirmedAt: lastConfirmed, ranks: ranks}
}

// Searcher runs the three legs.
type Searcher struct {
	db  *pgpool.Pool
	log *slog.Logger
}

func NewSearcher(db *pgpool.Pool, log *slog.Logger) *Searcher {
	return &Searcher{db: db, log: log}
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
		legs = append(legs, legRun{"vector", vectorSQL, []any{embedding.String()}})
	}

	var wg sync.WaitGroup
	errs := make([]error, len(legs))
	for i, l := range legs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.leg(ctx, l.name, l.sql, l.args, types, m)
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

// Leg queries share a projection and differ only in match + order.
// All see active memories exclusively. $1 is the leg's own parameter;
// $2 is the optional type filter (empty array = all types).
const (
	vectorSQL = `SELECT id, type, content, last_confirmed_at
		FROM memories
		WHERE status = 'active' AND embedding IS NOT NULL
		  AND (cardinality($2::text[]) = 0 OR type = ANY($2))
		ORDER BY embedding <=> $1::vector
		LIMIT ` + limitLit

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
		SELECT id, type, content, last_confirmed_at
		FROM memories, q
		WHERE status = 'active'
		  AND tsv @@ q.query
		  AND (cardinality($2::text[]) = 0 OR type = ANY($2))
		ORDER BY ts_rank(tsv, q.query) DESC
		LIMIT ` + limitLit

	// Entities literally named in the query pull in every memory that
	// references them. position() over ILIKE keeps it index-friendly
	// enough at this scale and case-insensitivity comes from lower().
	entitySQL = `SELECT m.id, m.type, m.content, m.last_confirmed_at
		FROM memories m
		WHERE m.status = 'active'
		  AND (cardinality($2::text[]) = 0 OR m.type = ANY($2))
		  AND m.entity_refs && ARRAY(
			SELECT e.id FROM entities e
			WHERE position(lower(e.name) IN lower($1)) > 0)
		ORDER BY m.last_confirmed_at DESC
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
	rows, err := db.Query(ctx, sql, append(args, typeNames)...)
	if err != nil {
		return fmt.Errorf("retrieval %s leg: %w", name, err)
	}
	defer rows.Close()

	rank := 0
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.ID, &c.Type, &c.Content, &c.LastConfirmedAt); err != nil {
			return fmt.Errorf("retrieval %s leg: %w", name, err)
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

// MarkRetrieved stamps last_retrieved_at and bumps retrieval_hits on
// the returned memories so the consolidation job can see what is
// actually used (archival window and usage-driven decay,
// memory-extraction-v2 slice 5). This is metadata bookkeeping,
// deliberately NOT a supersede or last_confirmed_at bump (D-011);
// memory content stays supersede-only. Failures only log — retrieval
// already succeeded.
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
