//go:build integration

package store

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// nearDupBudget5k is the documented NearDupPairs budget (D-150) for
// 5000 active rows; the lateral k-NN scales close to linearly, so 20k
// rows land near four times this.
const nearDupBudget5k = 30 * time.Second

func randUnit(r *rand.Rand) Vector {
	v := make(Vector, 1024)
	var norm float64
	for i := range v {
		f := r.NormFloat64()
		v[i] = float32(f)
		norm += f * f
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

// jitter returns base nudged by small noise: cosine ~0.995 to base.
func jitter(r *rand.Rand, base Vector) Vector {
	n := randUnit(r)
	v := make(Vector, len(base))
	var norm float64
	for i := range v {
		f := float64(base[i]) + 0.1*float64(n[i])
		v[i] = float32(f)
		norm += f * f
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

// seedVectors inserts active semantic marker rows in one batch and
// returns their ids in order.
func seedVectors(t *testing.T, s *Store, label string, vecs []Vector) []string {
	t.Helper()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	batch := &pgx.Batch{}
	for i, v := range vecs {
		batch.Queue(`INSERT INTO memories (type, content, embedding, status, confidence)
			VALUES ('semantic', $1, $2::vector, 'active', 0.9) RETURNING id`,
			fmt.Sprintf("%s %s %d", testMarker, label, i), v.String())
	}
	br := db.SendBatch(t.Context(), batch)
	defer func() { _ = br.Close() }()
	ids := make([]string, len(vecs))
	for i := range vecs {
		if err := br.QueryRow().Scan(&ids[i]); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	return ids
}

// clustered builds singles random rows plus clusters of the given sizes.
func clustered(r *rand.Rand, singles int, sizes []int) []Vector {
	var out []Vector
	for range singles {
		out = append(out, randUnit(r))
	}
	for _, size := range sizes {
		base := randUnit(r)
		for range size {
			out = append(out, jitter(r, base))
		}
	}
	return out
}

func onlyIDs(pairs [][2]string, ids []string) [][2]string {
	in := map[string]bool{}
	for _, id := range ids {
		in[id] = true
	}
	var out [][2]string
	for _, p := range pairs {
		if in[p[0]] && in[p[1]] {
			out = append(out, p)
		}
	}
	return out
}

// TestNearDupPairsMatchesBruteForce: on a 200-row sample the per-row
// k-NN join returns exactly the pairs the old full self-join did.
func TestNearDupPairsMatchesBruteForce(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	r := rand.New(rand.NewPCG(874, 880))
	ids := seedVectors(t, s, "neardup sample", clustered(r, 120, []int{2, 2, 3, 3, 4, 5, 6, 2, 3, 4, 6, 5, 5, 2, 4, 6, 3, 5}))

	got, err := s.NearDupPairs(ctx, 0.95)
	if err != nil {
		t.Fatalf("NearDupPairs: %v", err)
	}
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rows, err := db.Query(ctx, `SELECT a.id, b.id
		FROM memories a
		JOIN memories b ON a.id < b.id
		WHERE a.id = ANY($1::uuid[]) AND b.id = ANY($1::uuid[])
		  AND a.type = b.type
		  AND 1 - (a.embedding <=> b.embedding) >= 0.95
		ORDER BY 1, 2`, ids)
	if err != nil {
		t.Fatalf("brute force: %v", err)
	}
	want, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]string, error) {
		var p [2]string
		return p, row.Scan(&p[0], &p[1])
	})
	if err != nil {
		t.Fatalf("brute force rows: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("fixture produced no near-dup pairs")
	}
	if mine := onlyIDs(got, ids); !slices.Equal(mine, want) {
		t.Fatalf("k-NN pairs (%d) differ from brute force (%d):\n got %v\nwant %v", len(mine), len(want), mine, want)
	}
}

// TestNearDupPairs5kWithinBudget: 5000 active rows finish inside the
// D-150 budget and every seeded cluster pair is found.
func TestNearDupPairs5kWithinBudget(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	r := rand.New(rand.NewPCG(5000, 880))
	sizes := make([]int, 50)
	for i := range sizes {
		sizes[i] = 4
	}
	ids := seedVectors(t, s, "neardup 5k", clustered(r, 4800, sizes))
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Dead index entries from earlier fixtures count toward
	// hnsw.max_scan_tuples; a vacuumed index measures the live corpus.
	if _, err := db.Exec(ctx, `VACUUM ANALYZE memories`); err != nil {
		t.Fatalf("vacuum: %v", err)
	}

	start := time.Now()
	pairs, err := s.NearDupPairs(ctx, 0.95)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("NearDupPairs: %v", err)
	}
	t.Logf("NearDupPairs over %d seeded rows: %v", len(ids), took)
	if took > nearDupBudget5k {
		t.Fatalf("NearDupPairs took %v, budget %v", took, nearDupBudget5k)
	}
	// 50 clusters of 4: 6 pairs each, nothing across singles.
	if mine := onlyIDs(pairs, ids); len(mine) != 300 {
		t.Fatalf("found %d seeded pairs, want 300", len(mine))
	}
}
