package retrieval

import (
	"math"
	"sort"
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/store"
)

const (
	// rrfK is the standard Reciprocal Rank Fusion damping constant.
	rrfK = 60

	// recencyHalfLife: a memory unconfirmed for 90 days scores half of
	// a fresh one; two half-lives quarter it, and so on.
	recencyHalfLife = 90 * 24 * time.Hour

	// minScore is the confidently-stale cutoff (D-011: returning
	// nothing beats a bad guess). A fresh semantic memory at rank 30
	// on a single leg scores 1/(60+30) ≈ 0.011 and survives; the same
	// hit ~120 days unconfirmed (×0.40) falls to ≈ 0.0044 and drops.
	minScore = 0.005
)

// typeWeights bias durable knowledge over one-off observations.
var typeWeights = map[store.MemoryType]float64{
	store.TypeSemantic:   1.0,
	store.TypeProcedural: 0.9,
	store.TypeEpisodic:   0.7,
}

// Scored is a candidate after fusion, ready for budget packing.
type Scored struct {
	ID      string
	Type    store.MemoryType
	Content string
	Score   float64
	Legs    int // how many legs surfaced it (diagnostics)
}

// Fuse combines per-leg ranks with RRF, then multiplies recency decay
// and type weight. Summing RRF terms across legs is the multi-leg
// boost: a memory found three ways outranks any single-leg hit of
// equal rank. Results come back sorted best-first with the cutoff
// applied.
func Fuse(candidates map[string]*Candidate, now time.Time) []Scored {
	out := make([]Scored, 0, len(candidates))
	for _, c := range candidates {
		rrf := 0.0
		for _, rank := range c.ranks {
			rrf += 1.0 / float64(rrfK+rank)
		}
		score := rrf * recencyDecay(now.Sub(c.LastConfirmedAt)) * typeWeight(c.Type)
		if score < minScore {
			continue
		}
		out = append(out, Scored{
			ID: c.ID, Type: c.Type, Content: c.Content,
			Score: score, Legs: len(c.ranks),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID // deterministic ties
	})
	return out
}

func recencyDecay(age time.Duration) float64 {
	if age <= 0 {
		return 1
	}
	return math.Pow(0.5, float64(age)/float64(recencyHalfLife))
}

func typeWeight(t store.MemoryType) float64 {
	if w, ok := typeWeights[t]; ok {
		return w
	}
	return 0.7 // unknown types treated like episodic, never boosted
}
