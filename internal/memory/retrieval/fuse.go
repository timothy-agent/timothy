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

	// retrievalHalfLife (D-148): a retrieval newer than the last
	// confirmation restarts recency from last_retrieved_at, decaying at
	// half the speed, so facts in use stay recallable without anyone
	// confirming them. Recency is the larger of the two curves. A
	// rank-1 single-leg hit survives ~308 days past its last retrieval
	// against ~154 days past its last confirmation.
	retrievalHalfLife = 180 * 24 * time.Hour

	// minScore is the confidently-stale cutoff (D-011: returning
	// nothing beats a bad guess). Full confidence, never retrieved: a
	// single-leg hit drops after ~154 days unconfirmed at rank 1 and
	// ~104 days at rank 30; a 3-leg rank-1 hit after ~297 days.
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

// Fuse combines per-leg ranks with RRF, then multiplies recency decay,
// type weight and confidence weight. Summing RRF terms across legs is
// the multi-leg boost: a memory found three ways outranks any
// single-leg hit of equal rank. Results come back sorted best-first
// with the cutoff applied.
func Fuse(candidates map[string]*Candidate, now time.Time) []Scored {
	out := make([]Scored, 0, len(candidates))
	for _, c := range candidates {
		rrf := 0.0
		for _, rank := range c.ranks {
			rrf += 1.0 / float64(rrfK+rank)
		}
		score := rrf * recency(c, now) * typeWeight(c.Type) * confidenceWeight(c.Confidence)
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

// recency anchors decay on the newer of confirmation and retrieval
// (D-148).
func recency(c *Candidate, now time.Time) float64 {
	r := decay(now.Sub(c.LastConfirmedAt), recencyHalfLife)
	if c.LastRetrievedAt.After(c.LastConfirmedAt) {
		r = math.Max(r, decay(now.Sub(c.LastRetrievedAt), retrievalHalfLife))
	}
	return r
}

func decay(age, halfLife time.Duration) float64 {
	if age <= 0 {
		return 1
	}
	return math.Pow(0.5, float64(age)/float64(halfLife))
}

// confidenceWeight maps confidence [0,1] onto [0.5,1] (D-147): decay
// and weak extractions rank lower, but confidence alone never cuts a
// score by more than half, so an unscored row (0) still competes on
// rank and recency.
func confidenceWeight(c float32) float64 {
	return 0.5 + 0.5*math.Min(math.Max(float64(c), 0), 1)
}

func typeWeight(t store.MemoryType) float64 {
	if w, ok := typeWeights[t]; ok {
		return w
	}
	return 0.7 // unknown types treated like episodic, never boosted
}
