package retrieval

import (
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/store"
)

func cand(id string, typ store.MemoryType, confirmedAgo time.Duration, ranks map[string]int, now time.Time) *Candidate {
	return &Candidate{
		ID: id, Type: typ, Content: "content of " + id,
		LastConfirmedAt: now.Add(-confirmedAgo), Confidence: 1, ranks: ranks,
	}
}

func TestFuseHigherConfidenceRanksHigher(t *testing.T) {
	t.Parallel()
	now := time.Now()
	// "a" sorts first on an ID tie, so only confidence can put "b" on top.
	low := cand("a", store.TypeSemantic, 0, map[string]int{"vector": 2}, now)
	low.Confidence = 0.4
	high := cand("b", store.TypeSemantic, 0, map[string]int{"vector": 2}, now)
	out := Fuse(map[string]*Candidate{"a": low, "b": high}, now)
	if len(out) != 2 || out[0].ID != "b" {
		t.Fatalf("out = %+v, want confidence 1.0 above 0.4", out)
	}
	if ratio := out[1].Score / out[0].Score; ratio < 0.69 || ratio > 0.71 {
		t.Fatalf("score ratio = %f, want 0.7 (weight 0.5+0.5*0.4)", ratio)
	}
}

// TestFuseAgeHorizon pins the documented survival horizons (minScore
// and D-148 comments) at full confidence.
func TestFuseAgeHorizon(t *testing.T) {
	t.Parallel()
	const day = 24 * time.Hour
	legs := func(rank, n int) map[string]int {
		r := map[string]int{}
		for _, l := range []string{"vector", "text", "entity"}[:n] {
			r[l] = rank
		}
		return r
	}
	tests := []struct {
		rank, legs    int
		confirmedDays int
		retrievedDays int // -1: never retrieved
		survives      bool
	}{
		// rank 1, one leg: drops after ~154 days.
		{1, 1, 0, -1, true}, {1, 1, 100, -1, true}, {1, 1, 150, -1, true}, {1, 1, 300, -1, false},
		// rank 30, one leg: drops after ~104 days.
		{30, 1, 0, -1, true}, {30, 1, 100, -1, true}, {30, 1, 150, -1, false}, {30, 1, 300, -1, false},
		// rank 1, three legs: drops after ~297 days.
		{1, 3, 0, -1, true}, {1, 3, 100, -1, true}, {1, 3, 150, -1, true}, {1, 3, 300, -1, false},
		// rank 30, three legs: drops after ~246 days.
		{30, 3, 0, -1, true}, {30, 3, 100, -1, true}, {30, 3, 150, -1, true}, {30, 3, 300, -1, false},
		// D-148: a retrieval restarts recency at the slower half-life.
		{1, 1, 300, 0, true}, {1, 1, 300, 150, true}, {1, 1, 400, 300, true}, {1, 1, 500, 350, false},
		// A retrieval older than the confirmation changes nothing.
		{1, 1, 300, 400, false},
	}
	now := time.Now()
	for _, tc := range tests {
		c := cand("x", store.TypeSemantic, time.Duration(tc.confirmedDays)*day, legs(tc.rank, tc.legs), now)
		if tc.retrievedDays >= 0 {
			c.LastRetrievedAt = now.Add(-time.Duration(tc.retrievedDays) * day)
		}
		got := len(Fuse(map[string]*Candidate{"x": c}, now)) == 1
		if got != tc.survives {
			t.Errorf("rank %d legs %d confirmed %dd retrieved %dd: survives = %v, want %v",
				tc.rank, tc.legs, tc.confirmedDays, tc.retrievedDays, got, tc.survives)
		}
	}
}

func TestConfidenceWeight(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		conf float32
		want float64
	}{{-1, 0.5}, {0, 0.5}, {0.2, 0.6}, {0.4, 0.7}, {1, 1}, {2, 1}} {
		if got := confidenceWeight(tc.conf); got < tc.want-1e-6 || got > tc.want+1e-6 {
			t.Errorf("confidenceWeight(%v) = %v, want %v", tc.conf, got, tc.want)
		}
	}
}

func TestFuseMultiLegBeatsSingleLeg(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cands := map[string]*Candidate{
		"multi":  cand("multi", store.TypeSemantic, 0, map[string]int{"vector": 3, "text": 3, "entity": 3}, now),
		"single": cand("single", store.TypeSemantic, 0, map[string]int{"vector": 1}, now),
	}
	out := Fuse(cands, now)
	if len(out) != 2 {
		t.Fatalf("got %d results, want 2", len(out))
	}
	if out[0].ID != "multi" {
		t.Fatalf("top = %s, want multi (3 legs at rank 3 beat 1 leg at rank 1)", out[0].ID)
	}
	if out[0].Legs != 3 || out[1].Legs != 1 {
		t.Fatalf("legs = %d/%d", out[0].Legs, out[1].Legs)
	}
}

func TestFuseRecencyDecay(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cands := map[string]*Candidate{
		"fresh": cand("fresh", store.TypeSemantic, 0, map[string]int{"vector": 5}, now),
		"stale": cand("stale", store.TypeSemantic, 180*24*time.Hour, map[string]int{"vector": 5}, now),
	}
	out := Fuse(cands, now)
	if len(out) < 1 || out[0].ID != "fresh" {
		t.Fatalf("out = %+v, want fresh first", out)
	}
	// Two half-lives → quarter score.
	if len(out) == 2 {
		ratio := out[1].Score / out[0].Score
		if ratio < 0.2 || ratio > 0.3 {
			t.Fatalf("180d decay ratio = %f, want ~0.25", ratio)
		}
	}
}

func TestFuseTypeWeights(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cands := map[string]*Candidate{
		"sem": cand("sem", store.TypeSemantic, 0, map[string]int{"text": 2}, now),
		"pro": cand("pro", store.TypeProcedural, 0, map[string]int{"text": 2}, now),
		"epi": cand("epi", store.TypeEpisodic, 0, map[string]int{"text": 2}, now),
	}
	out := Fuse(cands, now)
	if len(out) != 3 || out[0].ID != "sem" || out[1].ID != "pro" || out[2].ID != "epi" {
		ids := make([]string, len(out))
		for i, s := range out {
			ids[i] = s.ID
		}
		t.Fatalf("order = %v, want [sem pro epi]", ids)
	}
}

func TestFuseDropsConfidentlyStale(t *testing.T) {
	t.Parallel()
	now := time.Now()
	// Rank-30 single-leg episodic hit unconfirmed for a year:
	// 1/90 × ~0.06 × 0.7 ≈ 0.0005 — far below cutoff.
	cands := map[string]*Candidate{
		"stale": cand("stale", store.TypeEpisodic, 365*24*time.Hour, map[string]int{"text": 30}, now),
	}
	if out := Fuse(cands, now); len(out) != 0 {
		t.Fatalf("confidently-stale hit survived: %+v", out)
	}
}

func TestFuseDeterministicOnTies(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cands := map[string]*Candidate{
		"b": cand("b", store.TypeSemantic, 0, map[string]int{"text": 1}, now),
		"a": cand("a", store.TypeSemantic, 0, map[string]int{"vector": 1}, now),
	}
	for i := 0; i < 5; i++ {
		out := Fuse(cands, now)
		if len(out) != 2 || out[0].ID != "a" {
			t.Fatalf("tie order unstable: %+v", out)
		}
	}
}
