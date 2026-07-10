// Package chat orchestrates one conversation turn: project the
// session's event log into context, stream through the gateway,
// persist the turn as events (with distilled residue), and keep the
// projection under budget. State lives in the log — a restart loses
// nothing (D-006).
package chat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

const (
	// defaultCategory serves plain chat turns when the caller picks
	// nothing; the web UI exposes a per-message picker.
	defaultCategory = "coding"
	// Each persistence stage gets its OWN deadline: LLM-backed stages
	// (distill, compaction) must never eat the database writes' clock.
	persistTimeout = 10 * time.Second
	distillBudget  = 65 * time.Second // two 30s attempts + slack
	compactBudget  = 90 * time.Second
	titleTimeout   = 15 * time.Second
)

// Gateway is the slice of the gateway client chat needs.
type Gateway interface {
	Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error)
}

// SessionLog is the slice of the session store chat needs; tests fake
// it, session.Store satisfies it.
type SessionLog interface {
	Create(ctx context.Context, title string) (string, error)
	Events(ctx context.Context, sessionID string) ([]session.Event, error)
	Append(ctx context.Context, sessionID, kind string, payload any) (int64, error)
	SetTitleIfEmpty(ctx context.Context, id, title string) error
	SetLastCategory(ctx context.Context, id, category string) error
}

// Distill extracts turn residue; loop.DistillTurn curried with the
// gateway in main, stubbed in tests. May return nil.
type Distill func(ctx context.Context, sessionID, turnText string) *session.TurnMemory

// Compactor keeps a session's projection under budget.
type Compactor interface {
	MaybeCompact(ctx context.Context, sessionID string) error
}

// Service orchestrates turns against the event store.
type Service struct {
	gw         Gateway
	log        SessionLog
	distill    Distill
	compactor  Compactor
	budget     int
	flushEvery time.Duration // pending-state flush cadence mid-stream
	logger     *slog.Logger
}

func New(gw Gateway, log SessionLog, distill Distill, compactor Compactor, budget int, logger *slog.Logger) *Service {
	logger.Info("chat service ready", "system_prompt_version", systemPromptVersion, "token_budget", budget)
	return &Service{gw: gw, log: log, distill: distill, compactor: compactor, budget: budget, flushEvery: 2 * time.Second, logger: logger}
}

// Request is one chat turn.
type Request struct {
	SessionID    string `json:"session_id,omitempty"`
	Message      string `json:"message"`
	TaskCategory string `json:"task_category,omitempty"`
	ModelHint    string `json:"model_hint,omitempty"`
}

// Chat streams one turn. The user message is durably appended before
// the provider is called; the assistant turn (or a pending_state on
// abnormal end) is appended when the stream finishes. The returned
// channel follows the stream package's terminal contract.
func (s *Service) Chat(ctx context.Context, req Request) (string, <-chan stream.StreamEvent, error) {
	if strings.TrimSpace(req.Message) == "" {
		return "", nil, fmt.Errorf("chat: message is required")
	}
	category := req.TaskCategory
	if category == "" {
		category = defaultCategory
	}

	sessionID := req.SessionID
	if sessionID == "" {
		id, err := s.log.Create(ctx, "")
		if err != nil {
			return "", nil, err
		}
		sessionID = id
	}

	events, err := s.log.Events(ctx, sessionID)
	if err != nil {
		return sessionID, nil, err
	}
	firstExchange := !hasUserMessage(events)

	if _, err := s.log.Append(ctx, sessionID, session.KindUserMessage, session.UserMessage{
		Text: req.Message, Category: category, ModelHint: req.ModelHint,
	}); err != nil {
		return sessionID, nil, err
	}
	events, err = s.log.Events(ctx, sessionID)
	if err != nil {
		return sessionID, nil, err
	}
	msgs, err := session.LLMContext(events, s.budget)
	if err != nil {
		return sessionID, nil, err
	}

	upstream, err := s.gw.Stream(ctx, gwclient.StreamRequest{
		TaskCategory: category,
		ModelHint:    req.ModelHint,
		System:       systemPrompt,
		Messages:     msgs,
		SessionID:    sessionID,
	})
	if err != nil {
		return sessionID, nil, err
	}

	out := make(chan stream.StreamEvent)
	go s.relay(ctx, sessionID, req.Message, category, firstExchange, upstream, out)
	return sessionID, out, nil
}

// relay forwards events to the client while accumulating the turn,
// then persists it. Persistence uses a cancel-detached context: a
// client disconnect or brain shutdown mid-turn must still leave a
// durable pending_state (the kill-test contract).
func (s *Service) relay(ctx context.Context, sessionID, userText, category string, firstExchange bool, upstream <-chan stream.StreamEvent, out chan<- stream.StreamEvent) {
	var text, reasoning strings.Builder
	var meta *stream.Meta
	sawDone := false
	flushed := 0

	// flushPending checkpoints the partial answer DURING the stream so
	// even a SIGKILL mid-turn loses at most one flush interval — the
	// projection splices the last pending in, and a completed turn
	// supersedes every checkpoint (the kill-test contract).
	flushPending := func() {
		if text.Len() == flushed || text.Len() == 0 {
			return
		}
		wctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
		defer cancel()
		if _, err := s.log.Append(wctx, sessionID, session.KindPendingState, session.PendingState{Partial: text.String()}); err != nil {
			s.logger.Warn("flush pending state", "session_id", sessionID, "error", err)
			return
		}
		flushed = text.Len()
	}

	drainAndPersist := func() {
		// Consume whatever the upstream still yields (it shares the
		// request ctx, so it winds down quickly), free the client,
		// then persist the final state.
		for ev := range upstream {
			switch ev.Type {
			case stream.EventChunk:
				text.WriteString(ev.Text)
			case stream.EventDone:
				sawDone = true
				meta = ev.Meta
			}
		}
		close(out)
		s.persistTurn(sessionID, userText, category, firstExchange, text.String(), reasoning.String(), meta, sawDone, flushed)
	}

	ticker := time.NewTicker(s.flushEvery)
	defer ticker.Stop()

	for {
		select {
		case ev, ok := <-upstream:
			if !ok {
				drainAndPersist()
				return
			}
			switch ev.Type {
			case stream.EventChunk:
				text.WriteString(ev.Text)
			case stream.EventReasoningChunk:
				reasoning.WriteString(ev.Text)
			case stream.EventDone:
				sawDone = true
				meta = ev.Meta
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				flushPending()
				drainAndPersist()
				return
			}
		case <-ticker.C:
			flushPending()
		case <-ctx.Done():
			flushPending()
			drainAndPersist()
			return
		}
	}
}

func (s *Service) persistTurn(sessionID, userText, category string, firstExchange bool, text, reasoning string, meta *stream.Meta, sawDone bool, flushed int) {
	if !sawDone {
		// Abnormal end: keep the partial durable; the projection
		// splices it into the next request. Skip when the periodic
		// flush already checkpointed this exact content.
		if text != "" && len(text) != flushed {
			ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
			defer cancel()
			if _, err := s.log.Append(ctx, sessionID, session.KindPendingState, session.PendingState{Partial: text}); err != nil {
				s.logger.Error("persist pending state", "session_id", sessionID, "error", err)
			}
		}
		return
	}

	var turn session.AssistantTurn
	turn.LLM.Message = text
	if reasoning != "" {
		turn.UI.Blocks = append(turn.UI.Blocks, session.UIBlock{Type: "reasoning", Text: reasoning})
	}
	turn.UI.Blocks = append(turn.UI.Blocks, session.UIBlock{Type: "text", Text: text})
	if meta != nil {
		turn.Provider, turn.Model, turn.LedgerID = meta.Provider, meta.Model, meta.LedgerID
	}
	if s.distill != nil && text != "" {
		dctx, cancel := context.WithTimeout(context.Background(), distillBudget)
		turn.LLM.TurnMemory = s.distill(dctx, sessionID, "user: "+userText+"\n\nassistant: "+text)
		cancel()
	}

	wctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	if _, err := s.log.Append(wctx, sessionID, session.KindAssistantTurn, turn); err != nil {
		s.logger.Error("persist assistant turn", "session_id", sessionID, "error", err)
		return
	}
	if err := s.log.SetLastCategory(wctx, sessionID, category); err != nil {
		s.logger.Warn("persist last category", "session_id", sessionID, "error", err)
	}
	if s.compactor != nil {
		cctx, cancel := context.WithTimeout(context.Background(), compactBudget)
		if err := s.compactor.MaybeCompact(cctx, sessionID); err != nil {
			s.logger.Error("compaction", "session_id", sessionID, "error", err)
		}
		cancel()
	}
	if firstExchange {
		s.autoTitle(sessionID, userText, text)
	}
}

// autoTitle names a session after its first exchange via a mini call;
// best-effort and never clobbers a user-chosen title.
func (s *Service) autoTitle(sessionID, userText, reply string) {
	ctx, cancel := context.WithTimeout(context.Background(), titleTimeout)
	defer cancel()

	const titleSystem = `Produce a title for this conversation: at most 6 words, plain text, no quotes, no trailing punctuation. Reply with only the title.`
	input := userText
	if len(reply) > 200 {
		reply = reply[:200]
	}
	input += "\n\n" + reply

	events, err := s.gw.Stream(ctx, gwclient.StreamRequest{
		TaskCategory: "mini",
		System:       titleSystem,
		Messages:     []provider.Message{{Role: "user", Content: input}},
		MaxTokens:    30,
		SessionID:    sessionID,
	})
	if err != nil {
		s.logger.Warn("auto-title", "session_id", sessionID, "error", err)
		return
	}
	var b strings.Builder
	for ev := range events {
		if ev.Type == stream.EventChunk {
			b.WriteString(ev.Text)
		}
	}
	title := strings.TrimSpace(strings.Trim(strings.TrimSpace(b.String()), `"'`))
	if title == "" {
		return
	}
	if len(title) > 80 {
		title = title[:80]
	}
	if err := s.log.SetTitleIfEmpty(ctx, sessionID, title); err != nil {
		s.logger.Warn("auto-title save", "session_id", sessionID, "error", err)
	}
}

func hasUserMessage(events []session.Event) bool {
	for _, ev := range events {
		if ev.Kind == session.KindUserMessage {
			return true
		}
	}
	return false
}
