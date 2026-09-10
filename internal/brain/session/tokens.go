package session

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

// Token estimation uses tiktoken's o200k encoding with embedded BPE
// data (no network). Anthropic and GLM publish no tokenizers, so this
// is a principled approximation for pre-send budgeting — the spec's
// ban is on len/3-style byte heuristics, and actual billing always
// comes from provider-reported usage in the ledger. Recorded as a
// deviation in docs/architecture.md.

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
			encErr = fmt.Errorf("session: load tokenizer: %w", encErr)
		}
	})
	return enc, encErr
}

// perMessageOverhead approximates chat-format framing tokens.
const perMessageOverhead = 4

// EstimateTokens counts tokens across projected messages.
func EstimateTokens(msgs []provider.Message) (int, error) {
	e, err := encoder()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, m := range msgs {
		total += len(e.Encode(m.Content, nil, nil)) + perMessageOverhead
	}
	return total, nil
}

// EstimateToolTokens counts tokens in the serialized tool definitions
// a turn offers. Measurement only: the figure is logged and recorded
// alongside prompt tokens so tool-surface overhead is visible, and is
// never used for cost (billing stays on provider-reported usage).
// json.Marshal reproduces the Anthropic wire bytes for the tool array
// exactly and is close for the OpenAI shapes.
func EstimateToolTokens(defs []provider.ToolDef) (int, error) {
	if len(defs) == 0 {
		return 0, nil
	}
	e, err := encoder()
	if err != nil {
		return 0, err
	}
	b, err := json.Marshal(defs)
	if err != nil {
		return 0, fmt.Errorf("session: marshal tool defs: %w", err)
	}
	return len(e.Encode(string(b), nil, nil)), nil
}
