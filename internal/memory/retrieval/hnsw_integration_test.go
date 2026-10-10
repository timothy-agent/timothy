//go:build integration

package retrieval

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/SumonMSelim/timothy/internal/memory/store"
)

// towards returns a random unit vector at cosine sim to the unit q.
func towards(r *rand.Rand, q store.Vector, sim float64) store.Vector {
	v := make([]float64, len(q))
	var dot float64
	for i := range v {
		v[i] = r.NormFloat64()
		dot += v[i] * float64(q[i])
	}
	var norm float64
	for i := range v {
		v[i] -= dot * float64(q[i])
		norm += v[i] * v[i]
	}
	norm = math.Sqrt(norm)
	out := make(store.Vector, len(q))
	for i := range v {
		out[i] = float32(sim*float64(q[i]) + math.Sqrt(1-sim*sim)*v[i]/norm)
	}
	return out
}

// TestFilteredVectorLegIterativeScan documents the filtered HNSW gap
// on 5000 rows: archived rows crowd the query's neighborhood, so the
// plain index scan (ef_search 40) returns fewer than 30 active hits
// and the D-150 iterative scan returns the full 30.
func TestFilteredVectorLegIterativeScan(t *testing.T) {
	s, _ := seedGolden(t)
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	q := basis(600)
	r := rand.New(rand.NewPCG(880, 5000))
	batch := &pgx.Batch{}
	for i := range 5000 {
		// One neighborhood, 49 archived rows to every active one: the
		// 40 index candidates of a plain scan hold about one active row.
		status := "archived"
		if i%50 == 0 {
			status = "active"
		}
		batch.Queue(`INSERT INTO memories (type, content, embedding, status, confidence)
			VALUES ('semantic', $1, $2::vector, $3, 0.9)`,
			fmt.Sprintf("%s hnsw filter %d", goldenMarker, i), towards(r, q, 0.9).String(), status)
	}
	if err := db.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Dead index entries from earlier fixtures count toward
	// hnsw.max_scan_tuples; a vacuumed index measures the live corpus.
	if _, err := db.Exec(ctx, `VACUUM ANALYZE memories`); err != nil {
		t.Fatalf("vacuum: %v", err)
	}

	// Plan knobs pin the HNSW path, the one the filter gap lives on;
	// with few active rows the planner may otherwise pick a btree and
	// sort, which is exact and hides the gap.
	hits := func(iterative string) (int, string) {
		var n int
		var plan strings.Builder
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			for _, set := range []string{
				"SET LOCAL enable_seqscan = off", "SET LOCAL enable_bitmapscan = off",
				"SET LOCAL enable_sort = off", "SET LOCAL hnsw.iterative_scan = " + iterative,
			} {
				if _, err := tx.Exec(ctx, set); err != nil {
					return err
				}
			}
			args := []any{q.String(), []string{}, store.DecayFloor, 1 - minSimilarity}
			rows, err := tx.Query(ctx, "EXPLAIN "+vectorSQL, args...)
			if err != nil {
				return err
			}
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			plan.WriteString(strings.Join(lines, "\n"))
			return tx.QueryRow(ctx, `SELECT count(*) FROM (`+vectorSQL+`) v`, args...).Scan(&n)
		})
		if err != nil {
			t.Fatalf("vector leg (%s): %v", iterative, err)
		}
		return n, plan.String()
	}

	without, plan := hits("off")
	if !strings.Contains(plan, "memories_embedding_hnsw") {
		t.Fatalf("plan skips the HNSW index; the test no longer exercises it:\n%s", plan)
	}
	with, _ := hits("strict_order")
	t.Logf("filtered vector leg over 5000 rows: %d hits without iterative scan, %d with", without, with)
	if without >= 30 {
		t.Fatalf("plain scan returned %d hits; the fixture no longer shows the gap", without)
	}
	if with != 30 {
		t.Fatalf("iterative scan returned %d hits, want 30", with)
	}

	// Search itself runs the leg under the iterative scan.
	cands, err := s.Search(ctx, "zzqx", q, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	vector := 0
	for _, c := range cands {
		if _, ok := c.ranks["vector"]; ok {
			vector++
		}
	}
	if vector != 30 {
		t.Fatalf("Search vector hits = %d, want 30", vector)
	}
}

// TestSearchRecordsLegMetrics: every leg run lands in the duration
// histogram, and failures count per leg.
func TestSearchRecordsLegMetrics(t *testing.T) {
	seeded, _ := seedGolden(t)
	m := testMetrics()
	s := NewSearcher(seeded.db, seeded.log, m)
	if _, err := s.Search(t.Context(), "postgres pool", basis(20), nil); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if n := testutil.CollectAndCount(m.LegDuration); n != 3 {
		t.Fatalf("duration series = %d, want 3 legs", n)
	}
	if n := testutil.CollectAndCount(m.LegErrors); n != 0 {
		t.Fatalf("error series = %d after a clean search, want 0", n)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Search(ctx, "postgres pool", basis(20), nil); err == nil {
		t.Fatal("want error when every leg fails")
	}
	for _, leg := range []string{"vector", "text", "entity"} {
		if got := testutil.ToFloat64(m.LegErrors.WithLabelValues(leg)); got != 1 {
			t.Fatalf("%s errors = %v, want 1", leg, got)
		}
	}
}
