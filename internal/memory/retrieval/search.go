// Package retrieval answers "what does Timothy remember about X" by
// fusing three search legs — vector k-NN, full-text, and entity match
// — over one Postgres (D-011). Results are scored, thresholded
// (returning nothing beats confidently-stale), and packed to a token
// budget.
package retrieval

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

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
// other legs still answer); types narrows the corpus.
func (s *Searcher) Search(ctx context.Context, query string, embedding store.Vector, types []store.MemoryType) (map[string]*Candidate, error) {
	m := &merger{into: make(map[string]*Candidate)}

	g, gctx := errgroup.WithContext(ctx)
	if len(embedding) > 0 {
		g.Go(func() error { return s.leg(gctx, "vector", vectorSQL, []any{embedding.String()}, types, m) })
	}
	g.Go(func() error { return s.leg(gctx, "text", textSQL, []any{query}, types, m) })
	g.Go(func() error { return s.leg(gctx, "entity", entitySQL, []any{query}, types, m) })
	if err := g.Wait(); err != nil {
		return nil, err
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

	textSQL = `SELECT id, type, content, last_confirmed_at
		FROM memories
		WHERE status = 'active'
		  AND tsv @@ websearch_to_tsquery('english', $1)
		  AND (cardinality($2::text[]) = 0 OR type = ANY($2))
		ORDER BY ts_rank(tsv, websearch_to_tsquery('english', $1)) DESC
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

// MarkRetrieved stamps last_retrieved_at on the returned memories so
// the consolidation job can see what is actually used. Deliberately
// NOT last_confirmed_at (D-011). Failures only log — retrieval
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
		`UPDATE memories SET last_retrieved_at = now() WHERE id = ANY($1)`, ids); err != nil {
		s.log.Warn("mark retrieved failed", "error", err, "ids", strings.Join(ids, ","))
	}
}
