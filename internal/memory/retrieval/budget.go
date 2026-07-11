package retrieval

import (
	"fmt"
	"sync"

	"github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"
)

// DefaultBudgetTokens bounds the retrieval block when the caller does
// not choose (spec default).
const DefaultBudgetTokens = 1500

// Token counting mirrors session's approach: tiktoken o200k with
// embedded BPE data. The budget is a promise to the prompt assembler,
// so a real tokenizer — never a bytes/3 heuristic — enforces it.
var (
	encOnce sync.Once
	enc     *tiktoken.Tiktoken
	encErr  error
)

func encoder() (*tiktoken.Tiktoken, error) {
	encOnce.Do(func() {
		tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())
		enc, encErr = tiktoken.GetEncoding("o200k_base")
		if encErr != nil {
			encErr = fmt.Errorf("retrieval: load tokenizer: %w", encErr)
		}
	})
	return enc, encErr
}

// Pack selects the highest-scored memories that fit budgetTokens and
// orders them for mid-context attention loss: the best item leads,
// the runner-up closes, the rest fill the middle best-first
// (serial-position effect). Input must be sorted best-first (Fuse's
// contract). The budget bounds the FINAL injected block: each item is
// costed in its rendered, escaped form and the fence + preamble are
// reserved up front — not just the raw contents.
func Pack(scored []Scored, budgetTokens int) ([]Scored, int, error) {
	e, err := encoder()
	if err != nil {
		return nil, 0, err
	}
	if budgetTokens <= 0 {
		budgetTokens = DefaultBudgetTokens
	}

	var picked []Scored
	used := len(e.Encode(BlockOpen+BlockClose, nil, nil))
	for _, s := range scored {
		cost := len(e.Encode(RenderItem(string(s.Type), s.Content), nil, nil))
		if used+cost > budgetTokens {
			continue // a smaller later item may still fit
		}
		picked = append(picked, s)
		used += cost
	}
	if len(picked) == 0 {
		return nil, 0, nil // nothing fits: no block, no framing cost
	}
	return positionForAttention(picked), used, nil
}

// positionForAttention returns [best, 3rd, 4th, …, 2nd]: strongest
// memory first and second-strongest last, weakest buried mid-list.
func positionForAttention(items []Scored) []Scored {
	if len(items) < 3 {
		return items
	}
	out := make([]Scored, 0, len(items))
	out = append(out, items[0])
	out = append(out, items[2:]...)
	out = append(out, items[1])
	return out
}
