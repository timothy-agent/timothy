package retrieval

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/memory/store"
)

func scoredItem(id string, words int, score float64) Scored {
	return Scored{
		ID: id, Type: store.TypeSemantic, Score: score,
		Content: strings.TrimSpace(strings.Repeat("word ", words)),
	}
}

func TestPackNeverExceedsBudget(t *testing.T) {
	t.Parallel()
	// Property test: random corpora and budgets, the packed token
	// count must never exceed the budget. Deterministic seed; not
	// security-sensitive.
	rng := rand.New(rand.NewSource(42)) //nolint:gosec // test fixture randomness
	for round := 0; round < 50; round++ {
		var scored []Scored
		n := 1 + rng.Intn(40)
		for i := 0; i < n; i++ {
			scored = append(scored, scoredItem(
				fmt.Sprintf("m%d", i), 1+rng.Intn(400), 1.0/float64(i+1)))
		}
		budget := 50 + rng.Intn(2000)
		picked, used, err := Pack(scored, budget)
		if err != nil {
			t.Fatalf("Pack: %v", err)
		}
		if used > budget {
			t.Fatalf("round %d: used %d > budget %d (%d items)", round, used, budget, len(picked))
		}
		// Recount independently against the RENDERED block — framing,
		// escaping and all — because that is what lands in the prompt.
		e, _ := encoder()
		total := 0
		if len(picked) > 0 {
			rendered := BlockOpen
			for _, s := range picked {
				rendered += RenderItem(string(s.Type), s.Content)
			}
			rendered += BlockClose
			total = len(e.Encode(rendered, nil, nil))
		}
		if total > budget {
			t.Fatalf("round %d: rendered block %d tokens > budget %d", round, total, budget)
		}
		// Per-part accounting must never under-report the whole: BPE
		// merges across boundaries only shrink the concatenation.
		if total > used {
			t.Fatalf("round %d: reported %d < rendered recount %d", round, used, total)
		}
	}
}

func TestPackPrefersHighScores(t *testing.T) {
	t.Parallel()
	scored := []Scored{
		scoredItem("best", 100, 0.9),
		scoredItem("mid", 100, 0.5),
		scoredItem("worst", 100, 0.1),
	}
	// Budget fits the block framing plus roughly two 100-word items.
	picked, _, err := Pack(scored, 300)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if len(picked) != 2 {
		t.Fatalf("picked %d items, want 2", len(picked))
	}
	got := map[string]bool{}
	for _, s := range picked {
		got[s.ID] = true
	}
	if !got["best"] || !got["mid"] {
		t.Fatalf("picked = %v, want best+mid", got)
	}
}

func TestPackSerialPositioning(t *testing.T) {
	t.Parallel()
	scored := []Scored{
		scoredItem("first", 10, 0.9),
		scoredItem("second", 10, 0.8),
		scoredItem("third", 10, 0.7),
		scoredItem("fourth", 10, 0.6),
	}
	picked, _, err := Pack(scored, 10_000)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if len(picked) != 4 {
		t.Fatalf("picked %d, want 4", len(picked))
	}
	// Best leads, runner-up closes, the rest fill the middle.
	if picked[0].ID != "first" || picked[len(picked)-1].ID != "second" {
		ids := make([]string, len(picked))
		for i, s := range picked {
			ids[i] = s.ID
		}
		t.Fatalf("order = %v, want first…second", ids)
	}
}

func TestPackSkipsOversizedButKeepsSmaller(t *testing.T) {
	t.Parallel()
	scored := []Scored{
		scoredItem("huge", 2000, 0.9),
		scoredItem("small", 20, 0.5),
	}
	picked, _, err := Pack(scored, 100)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if len(picked) != 1 || picked[0].ID != "small" {
		t.Fatalf("picked = %+v, want just small", picked)
	}
}

func TestPackNothingFitsMeansNoBlock(t *testing.T) {
	t.Parallel()
	// A budget below even the fence framing must yield an empty pick
	// and report zero tokens — no block gets injected at all.
	picked, used, err := Pack([]Scored{scoredItem("a", 200, 0.9)}, 10)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if len(picked) != 0 || used != 0 {
		t.Fatalf("picked=%d used=%d, want 0/0", len(picked), used)
	}
}

func TestPackDefaultBudget(t *testing.T) {
	t.Parallel()
	picked, used, err := Pack([]Scored{scoredItem("a", 10, 0.9)}, 0)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if len(picked) != 1 || used <= 0 {
		t.Fatalf("picked=%d used=%d", len(picked), used)
	}
}
