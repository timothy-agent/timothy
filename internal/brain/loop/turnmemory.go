// Package loop will own the agent think/act loop; for now it carries
// turn distillation — the structured residue extracted after each
// assistant turn (D-007). Within a turn the model sees full traffic;
// across turns only this residue survives.
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// Gateway is the slice of the gateway client distillation needs.
type Gateway interface {
	Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error)
	RouteForRole(ctx context.Context, role string) (string, bool, error)
}

const (
	// distillTimeout must cover the reasoning route: qwen3 (the local
	// route's model) spends completion budget THINKING before it emits
	// the strict-JSON answer, so both the timeout and MaxTokens below
	// are sized for a thinking model, not a plain instruct one.
	distillTimeout     = 90 * time.Second
	maxKeyFindings     = 5
	distillMaxAttempts = 2
)

// distillSystem demands strict JSON. The schema mirrors
// session.TurnMemory exactly; unknown fields are rejected on parse.
const distillSystem = `You extract structured residue from one conversation turn of an AI assistant. Reply with ONLY a JSON object — no prose, no markdown fences:
{"files_changed":["path"],"failures":[{"what":"command or tool","why":"reason"}],"key_findings":["one sentence"]}
Rules: key_findings has at most 5 items, one sentence each, only durable facts worth carrying into later turns — names, dates, numbers, decisions, commitments. files_changed lists files actually modified this turn, empty otherwise. failures lists failed commands or tool calls only. Use empty arrays when nothing qualifies.`

// DistillTurn extracts a TurnMemory from one turn's raw text via a
// mini-category call. Invalid output retries once; a second failure
// returns nil — distillation must never block or fail the turn
// (callers log and move on). route overrides sideRoute for this
// call — the caller passes the sensitive route pin when the turn
// executed a sensitive tool (same pattern as extract.Request.Route,
// internal/memory/extract/extract.go), "" otherwise.
func DistillTurn(ctx context.Context, gw Gateway, sessionID, turnText, route string) *session.TurnMemory {
	if route == "" {
		if name, ok, err := gw.RouteForRole(ctx, "summarize"); err == nil && ok {
			route = name
		}
	}
	for attempt := 0; attempt < distillMaxAttempts; attempt++ {
		tm, err := distillOnce(ctx, gw, sessionID, turnText, route)
		if err == nil {
			return tm
		}
		if ctx.Err() != nil {
			return nil
		}
	}
	return nil
}

func distillOnce(ctx context.Context, gw Gateway, sessionID, turnText, route string) (*session.TurnMemory, error) {
	ctx, cancel := context.WithTimeout(ctx, distillTimeout)
	defer cancel()

	events, err := gw.Stream(ctx, gwclient.StreamRequest{
		Route:        route,
		Purpose:      "distill",
		System:       distillSystem,
		Messages:     []provider.Message{{Role: "user", Content: turnText}},
		MaxTokens:    1500,
		SessionID:    sessionID,
	})
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	for ev := range events {
		switch ev.Type {
		case stream.EventChunk:
			b.WriteString(ev.Text)
		case stream.EventError:
			return nil, fmt.Errorf("distill: %s", ev.Err.Message)
		}
	}
	return parseTurnMemory(b.String())
}

// parseTurnMemory decodes the model's reply strictly: fences stripped,
// unknown fields rejected, findings clamped to the schema's cap.
func parseTurnMemory(raw string) (*session.TurnMemory, error) {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var tm session.TurnMemory
	if err := dec.Decode(&tm); err != nil {
		return nil, fmt.Errorf("distill: invalid JSON: %w", err)
	}
	if len(tm.KeyFindings) > maxKeyFindings {
		tm.KeyFindings = tm.KeyFindings[:maxKeyFindings]
	}
	return &tm, nil
}
