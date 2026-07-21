// Package chat orchestrates one conversation turn: project the
// session's event log into context, stream through the gateway,
// persist the turn as events (with distilled residue), and keep the
// projection under budget. State lives in the log — a restart loses
// nothing (D-006).
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
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
	// Compaction runs an extraction round trip AND a summarize on slow
	// reasoning providers; a tight budget starves the summarize and
	// compaction never converges. Post-turn passes are off the user's
	// clock; the rare pre-send pass accepts the latency.
	compactBudget = 150 * time.Second
	titleTimeout  = 15 * time.Second
)

// ErrBadRequest marks caller mistakes (empty message) so the API can
// answer 400 instead of blaming the gateway.
var ErrBadRequest = errors.New("bad request")

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

// MemoryExtract posts one turn's text to memoryd for long-term memory
// extraction. Fire-and-forget from chat's view: chat invokes it on a
// goroutine, the wrapper owns timeout and error logging, and no
// failure may touch the user-facing turn.
type MemoryExtract func(ctx context.Context, sessionID string, seq int64, text string)

// MemoryRetrieve returns the rendered long-term memory block for a
// user message, or "" for nothing relevant. The wrapper owns timeout
// and error handling; a failure returns "" — a turn without memories
// beats no turn.
type MemoryRetrieve func(ctx context.Context, sessionID, query string) string

// Service orchestrates turns against the event store.
type Service struct {
	gw          Gateway
	log         SessionLog
	distill     Distill
	compactor   Compactor
	memory      MemoryExtract  // nil: long-term memory off
	recall      MemoryRetrieve // nil: no memory injection
	budget      func(context.Context) int
	packs       []skills.Skill
	skillAllow  func(context.Context, string) bool // nil: all packs allowed
	skillBodies map[string]string                  // name -> full pack body, for skill_hint
	flushEvery  time.Duration                      // pending-state flush cadence mid-stream
	logger      *slog.Logger
}

// SetMemoryExtract wires the memoryd hook. Optional — nil leaves
// long-term memory off.
func (s *Service) SetMemoryExtract(fn MemoryExtract) { s.memory = fn }

// SetMemoryRetrieve wires per-turn memory recall. Optional.
func (s *Service) SetMemoryRetrieve(fn MemoryRetrieve) { s.recall = fn }

// New builds the service. packs are the loaded skill definitions: their
// one-line index goes into the system prompt, and their bodies back
// skill_hint (nil/empty = no skills). budget resolves the projected
// context cap per turn (a runtime setting, not a constant); skillAllow
// gates packs per turn, nil allows all. The assembled system prompt
// only changes when a setting does, so provider prompt caches (D-018)
// stay warm in the steady state.
func New(gw Gateway, log SessionLog, distill Distill, compactor Compactor, budget func(context.Context) int, packs []skills.Skill, skillAllow func(context.Context, string) bool, logger *slog.Logger) *Service {
	logger.Info("chat service ready", "system_prompt_version", systemPromptVersion)
	bodies := make(map[string]string, len(packs))
	for _, p := range packs {
		bodies[p.Name] = p.Body
	}
	return &Service{
		gw: gw, log: log, distill: distill, compactor: compactor, budget: budget,
		packs:       packs,
		skillAllow:  skillAllow,
		skillBodies: bodies,
		flushEvery:  2 * time.Second, logger: logger,
	}
}

// allowedPacks filters the loaded packs through the runtime allowlist.
func (s *Service) allowedPacks(ctx context.Context) []skills.Skill {
	if s.skillAllow == nil {
		return s.packs
	}
	out := make([]skills.Skill, 0, len(s.packs))
	for _, p := range s.packs {
		if s.skillAllow(ctx, p.Name) {
			out = append(out, p)
		}
	}
	return out
}

// Request is one chat turn.
type Request struct {
	SessionID    string `json:"session_id,omitempty"`
	Message      string `json:"message"`
	TaskCategory string `json:"task_category,omitempty"`
	ModelHint    string `json:"model_hint,omitempty"`
	// SkillHint names a skill pack to force-load for this turn — set
	// when the user picked one explicitly (a UI chip, not parsed
	// text). Unlike load_skill, this is not a choice the model can
	// skip: the pack's body is in the system prompt before the first
	// token streams.
	SkillHint string `json:"skill_hint,omitempty"`
}

// Chat streams one turn. The user message is durably appended before
// the provider is called; the assistant turn (or a pending_state on
// abnormal end) is appended when the stream finishes. The returned
// channel follows the stream package's terminal contract.
func (s *Service) Chat(ctx context.Context, req Request) (string, <-chan stream.StreamEvent, error) {
	if strings.TrimSpace(req.Message) == "" {
		return "", nil, fmt.Errorf("chat: %w: message is required", ErrBadRequest)
	}
	var skillBody string
	if req.SkillHint != "" {
		body, ok := s.skillBodies[req.SkillHint]
		if !ok || (s.skillAllow != nil && !s.skillAllow(ctx, req.SkillHint)) {
			return "", nil, fmt.Errorf("chat: %w: unknown skill %q", ErrBadRequest, req.SkillHint)
		}
		skillBody = body
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
	// Pre-send guarantee: the context actually sent to the provider
	// stays under budget even on the turn that crosses it. The
	// post-turn pass below keeps sessions compacted ahead of time, so
	// this is a cheap no-op except on the crossing turn; best-effort —
	// an oversized context beats no answer.
	if s.compactor != nil {
		cctx, cancel := context.WithTimeout(ctx, compactBudget)
		if err := s.compactor.MaybeCompact(cctx, sessionID); err != nil {
			s.logger.Warn("pre-send compaction", "session_id", sessionID, "error", err)
		}
		cancel()
	}
	events, err = s.log.Events(ctx, sessionID)
	if err != nil {
		return sessionID, nil, err
	}
	msgs, err := session.LLMContext(events, s.budget(ctx))
	if err != nil {
		return sessionID, nil, err
	}

	// Retrieved memory and a hinted skill both ride the system
	// prompt's TAIL: the stable prefix stays byte-identical for
	// provider prompt caches (D-018) while the per-turn additions vary
	// after it. The memory block is fenced DATA, never instructions
	// (D-011 poisoning defense); the skill body is instructions the
	// user explicitly selected, deterministically loaded rather than
	// left to the model's load_skill judgment.
	system := assembleSystem(skills.Index(s.allowedPacks(ctx)))
	if skillBody != "" {
		system += "\n\n# Skill: " + req.SkillHint + "\n\n" + skillBody
	}
	if s.recall != nil {
		if block := s.recall(ctx, sessionID, req.Message); block != "" {
			system += "\n\n" + block
		}
	}

	upstream, err := s.gw.Stream(ctx, gwclient.StreamRequest{
		TaskCategory: category,
		Purpose:      "chat",
		ModelHint:    req.ModelHint,
		System:       system,
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
	var usage *stream.Usage
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
			case stream.EventUsage:
				usage = ev.Usage
			case stream.EventDone:
				sawDone = true
				meta = ev.Meta
			}
		}
		close(out)
		s.persistTurn(sessionID, userText, category, firstExchange, text.String(), reasoning.String(), meta, usage, sawDone, flushed)
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
			case stream.EventUsage:
				usage = ev.Usage
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

func (s *Service) persistTurn(sessionID, userText, category string, firstExchange bool, text, reasoning string, meta *stream.Meta, usage *stream.Usage, sawDone bool, flushed int) {
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

	// Models occasionally restate their entire answer after a late
	// tool call; the loop concatenates every step's text, so the
	// restatement lands as a verbatim duplicate tail. Collapse it
	// before the turn becomes durable.
	text = collapseRepeatedTail(text)
	reasoning = collapseRepeatedTail(reasoning)

	var turn session.AssistantTurn
	turn.LLM.Message = text
	if reasoning != "" {
		turn.UI.Blocks = append(turn.UI.Blocks, session.UIBlock{Type: "reasoning", Text: reasoning})
	}
	// A turn whose answer landed entirely in reasoning has no text;
	// an empty text block serializes without its text key (omitempty)
	// and renders as a literal "undefined" in older clients.
	if text != "" {
		turn.UI.Blocks = append(turn.UI.Blocks, session.UIBlock{Type: "text", Text: text})
	}
	if meta != nil {
		turn.Provider, turn.Model, turn.LedgerID = meta.Provider, meta.Model, meta.LedgerID
	}
	turn.Usage = usage

	// The assistant turn must be durable the moment the stream ends: a
	// follow-up message can arrive within seconds, and its projection
	// must see this turn completed (not a phantom interruption from a
	// stale checkpoint). Distillation is an LLM call — it lands later
	// as its own turn_memory event, never on this write's clock.
	wctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	turnSeq, err := s.log.Append(wctx, sessionID, session.KindAssistantTurn, turn)
	if err != nil {
		s.logger.Error("persist assistant turn", "session_id", sessionID, "error", err)
		return
	}
	if err := s.log.SetLastCategory(wctx, sessionID, category); err != nil {
		s.logger.Warn("persist last category", "session_id", sessionID, "error", err)
	}

	var tm *session.TurnMemory
	if s.distill != nil && text != "" {
		dctx, dcancel := context.WithTimeout(context.Background(), distillBudget)
		tm = s.distill(dctx, sessionID, "user: "+userText+"\n\nassistant: "+text)
		dcancel()
		if tm != nil && (len(tm.FilesChanged) > 0 || len(tm.Failures) > 0 || len(tm.KeyFindings) > 0) {
			mctx, mcancel := context.WithTimeout(context.Background(), persistTimeout)
			if _, err := s.log.Append(mctx, sessionID, session.KindTurnMemory, session.TurnMemoryEvent{
				TurnSeq: turnSeq, TurnMemory: *tm,
			}); err != nil {
				s.logger.Warn("persist turn memory", "session_id", sessionID, "error", err)
			}
			mcancel()
		}
	}

	// Long-term memory extraction rides the same residue (D-007): the
	// user's words plus the distilled turn, never the raw trace. It
	// runs on every COMPLETED turn even when the assistant produced no
	// text (some providers end tool turns without a message) — the
	// user's words alone can carry facts. Detached context — the turn
	// is already over.
	if s.memory != nil {
		mtext := "user: " + userText
		if tm != nil {
			if residue, err := json.Marshal(tm); err == nil {
				mtext += "\n\nturn residue: " + string(residue)
			}
		} else if text != "" {
			mtext += "\n\nassistant: " + text
		}
		go s.memory(context.Background(), sessionID, turnSeq, mtext)
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
	input := userText + "\n\n" + truncateRunes(reply, 200)

	events, err := s.gw.Stream(ctx, gwclient.StreamRequest{
		TaskCategory: "mini",
		Purpose:      "title",
		System:       titleSystem,
		Messages:     []provider.Message{{Role: "user", Content: input}},
		// Reasoning models spend hundreds of tokens thinking before
		// the first answer token; a tight cap truncates the stream
		// mid-reasoning and yields an empty title.
		MaxTokens: 1000,
		SessionID: sessionID,
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
		s.logger.Warn("auto-title returned no text", "session_id", sessionID)
		return
	}
	title = truncateRunes(title, 80)
	if err := s.log.SetTitleIfEmpty(ctx, sessionID, title); err != nil {
		s.logger.Warn("auto-title save", "session_id", sessionID, "error", err)
	}
}

// truncateRunes cuts on rune boundaries: byte slicing can split a
// UTF-8 sequence and Postgres rejects invalid text.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// collapseRepeatedTail strips a verbatim duplicated tail: when the
// text ends with a block that is an exact copy of what immediately
// precedes it (whitespace between copies aside), one copy is dropped.
// The 40-char floor keeps legitimately repeated short phrases intact;
// scanning longest-first collapses the whole restated answer, not a
// fragment of it.
func collapseRepeatedTail(s string) string {
	const minRepeat = 40
	t := strings.TrimRight(s, " \t\n")
	n := len(t)
	for l := n / 2; l >= minRepeat; l-- {
		tail := t[n-l:]
		head := strings.TrimRight(t[:n-l], " \t\n")
		if strings.HasSuffix(head, tail) {
			return collapseRepeatedTail(head)
		}
	}
	if n == len(s) {
		return s
	}
	return t
}

func hasUserMessage(events []session.Event) bool {
	for _, ev := range events {
		if ev.Kind == session.KindUserMessage {
			return true
		}
	}
	return false
}
