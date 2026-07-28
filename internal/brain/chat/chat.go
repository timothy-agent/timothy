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

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

const (
	// autoAgentName is the request sentinel meaning "pick an agent for
	// me" (D-034 follow-up): the composer's "Auto" choice, resolved
	// through candidates+classify before the normal agent lookup.
	autoAgentName = "auto"
	// classifyRoute serves the auto-dispatch classification call —
	// same cheap side-call route distillation and extraction still use.
	// "local" is a real, always-provisioned fixed route
	// (migrations/0022_local_route.sql); "mini" was never seeded by
	// any migration and every call on it failed with no_route. autoTitle
	// uses defaultRoute instead — a session's name deserves the same
	// model quality as the conversation it's naming, not the cheapest
	// available one.
	classifyRoute = "local"
	// defaultRoute serves plain chat turns when the caller picks
	// nothing; the web UI exposes a per-message picker.
	defaultRoute = "default"
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
	SetLastRoute(ctx context.Context, id, route, agent string) error
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
	memory      MemoryExtract   // nil: long-term memory off
	recall      MemoryRetrieve  // nil: no memory injection
	agents      AgentResolver   // nil: zero-value agent (everything allowed)
	candidates  AgentCandidates // nil: auto-dispatch falls back to default
	classify    agents.Classify // nil: auto-dispatch falls back to default
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

// SetAutoDispatch wires auto agent dispatch (D-034 follow-up): a
// request naming the autoAgentName sentinel resolves through candidates
// and classify instead of the named agent. Optional — nil candidates
// or classify makes the sentinel resolve to the default agent, same as
// an empty name.
func (s *Service) SetAutoDispatch(candidates AgentCandidates, classify agents.Classify) {
	s.candidates, s.classify = candidates, classify
}

// New builds the service. packs are the loaded skill definitions: their
// one-line index goes into the system prompt, and their bodies back
// skill_hint (nil/empty = no skills). budget resolves the projected
// context cap per turn (a runtime setting, not a constant); skillAllow
// gates packs per turn, nil allows all. The assembled system prompt
// only changes when a setting does, so provider prompt caches (D-018)
// stay warm in the steady state.
// AgentResolver returns the profile serving a named agent; empty name
// resolves the default. False = unknown (non-empty) name.
type AgentResolver func(ctx context.Context, name string) (agents.Agent, bool)

// AgentCandidates lists the enabled agents auto-dispatch (D-034
// follow-up) chooses among; nil or empty means dispatch always falls
// back to the default agent.
type AgentCandidates func(ctx context.Context) []agents.Agent

func New(gw Gateway, log SessionLog, distill Distill, compactor Compactor, budget func(context.Context) int, packs []skills.Skill, skillAllow func(context.Context, string) bool, resolver AgentResolver, logger *slog.Logger) *Service {
	logger.Info("chat service ready", "system_prompt_version", systemPromptVersion)
	bodies := make(map[string]string, len(packs))
	for _, p := range packs {
		bodies[p.Name] = p.Body
	}
	return &Service{
		gw: gw, log: log, distill: distill, compactor: compactor, budget: budget,
		packs:       packs,
		skillAllow:  skillAllow,
		agents:      resolver,
		skillBodies: bodies,
		flushEvery:  2 * time.Second, logger: logger,
	}
}

// dispatchAgent resolves the "auto" sentinel to a real agent name via
// agents.Dispatch. classify is built here (not injected verbatim as
// Classify) so it always drains through this service's own Gateway —
// a fresh call, not a lingering handle from setup time.
func (s *Service) dispatchAgent(ctx context.Context, message string) string {
	if s.candidates == nil || s.classify == nil {
		return ""
	}
	candidates := s.candidates(ctx)
	return agents.Dispatch(ctx, s.classify, message, candidates, "")
}

// ClassifyOverGateway builds an agents.Classify that asks classifyRoute
// for a one-shot text reply — the same cheap side-call shape as
// auto-title, distillation, and extraction use. Exported so main can
// wire it via SetAutoDispatch using the raw gateway client, bypassing
// the tool loop that only engages for Purpose=="chat".
func ClassifyOverGateway(gw Gateway) agents.Classify {
	return func(ctx context.Context, prompt string) (string, error) {
		events, err := gw.Stream(ctx, gwclient.StreamRequest{
			Route:     classifyRoute,
			Purpose:   "agent_dispatch",
			System:    "Answer with only what is requested — no prose, no explanation.",
			Messages:  []provider.Message{{Role: "user", Content: prompt}},
			MaxTokens: 20,
		})
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for ev := range events {
			if ev.Type == stream.EventChunk {
				b.WriteString(ev.Text)
			}
		}
		return b.String(), nil
	}
}

// allowedPacks filters the loaded packs through the global runtime
// allowlist AND the serving agent's own skill list (empty = all).
func (s *Service) allowedPacks(ctx context.Context, profile agents.Agent) []skills.Skill {
	out := make([]skills.Skill, 0, len(s.packs))
	for _, p := range s.packs {
		if s.skillAllow != nil && !s.skillAllow(ctx, p.Name) {
			continue
		}
		if !profileAllows(profile.Skills, p.Name) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// profileAllows checks one agent allowlist: empty admits everything.
func profileAllows(allow []string, name string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, n := range allow {
		if n == name {
			return true
		}
	}
	return false
}

// Request is one chat turn.
type Request struct {
	SessionID    string `json:"session_id,omitempty"`
	Message      string `json:"message"`
	// Agent names who serves this turn; empty = the default agent.
	Agent string `json:"agent,omitempty"`
	Route string `json:"route,omitempty"`
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
	agentName := req.Agent
	if agentName == autoAgentName {
		agentName = s.dispatchAgent(ctx, req.Message)
	}
	profile := agents.Agent{Memory: true}
	if s.agents != nil {
		var known bool
		profile, known = s.agents(ctx, agentName)
		if !known {
			return "", nil, fmt.Errorf("chat: %w: unknown agent %q", ErrBadRequest, agentName)
		}
	}
	var skillBody string
	if req.SkillHint != "" {
		body, ok := s.skillBodies[req.SkillHint]
		if !ok || (s.skillAllow != nil && !s.skillAllow(ctx, req.SkillHint)) ||
			!profileAllows(profile.Skills, req.SkillHint) {
			return "", nil, fmt.Errorf("chat: %w: unknown skill %q", ErrBadRequest, req.SkillHint)
		}
		skillBody = body
	}
	// Routing precedence: explicit request override, then the agent's
	// route, then the default chain.
	route := req.Route
	if route == "" {
		route = profile.Route
	}
	if route == "" {
		route = defaultRoute
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
	// needsTitle, not "is this literally message #1": a session whose
	// earlier turns all failed (chain exhausted, a dropped stream) has
	// never had a live shot at autoTitle, since persistTurn only calls
	// it on a completed turn. Keying off "no PRIOR turn ever completed"
	// instead of "no PRIOR message exists" makes titling retry on every
	// later message too, until one finally succeeds.
	needsTitle := !hasCompletedTurn(events)

	if _, err := s.log.Append(ctx, sessionID, session.KindUserMessage, session.UserMessage{
		Text: req.Message, Route: route, Agent: profile.Name, ModelHint: req.ModelHint,
	}); err != nil {
		return sessionID, nil, err
	}
	return s.runTurn(ctx, sessionID, req.Message, req.ModelHint, req.SkillHint, skillBody, route, profile, needsTitle)
}

// ErrNoRetryableTurn marks a Retry call whose session isn't in a
// retryable state — its last event isn't a user message left dangling
// by a failed attempt (persistTurn only completes a turn on sawDone).
var ErrNoRetryableTurn = errors.New("no retryable turn")

// Retry re-runs generation for a session's last turn WITHOUT persisting
// a second user_message: Chat unconditionally appends before streaming
// (line above), so a failed attempt already leaves that message durable
// with no assistant_turn after it. Retry reuses it verbatim — same
// route/agent/model_hint the original request resolved to, since those
// live on the persisted UserMessage, not the transient Request. A
// skill_hint is NOT persisted (it's a rare, deliberate one-off pick),
// so a retried turn never re-loads one.
func (s *Service) Retry(ctx context.Context, sessionID string) (string, <-chan stream.StreamEvent, error) {
	events, err := s.log.Events(ctx, sessionID)
	if err != nil {
		return sessionID, nil, err
	}
	last, ok := lastUserMessage(events)
	if !ok {
		return sessionID, nil, fmt.Errorf("chat: %w: session has no retryable turn", ErrNoRetryableTurn)
	}
	needsTitle := !hasCompletedTurn(events)

	profile := agents.Agent{Memory: true}
	if s.agents != nil {
		var known bool
		profile, known = s.agents(ctx, last.Agent)
		if !known {
			return sessionID, nil, fmt.Errorf("chat: %w: unknown agent %q", ErrBadRequest, last.Agent)
		}
	}
	route := last.Route
	if route == "" {
		route = defaultRoute
	}
	return s.runTurn(ctx, sessionID, last.Text, last.ModelHint, "", "", route, profile, needsTitle)
}

// runTurn is the shared tail of Chat and Retry: compact, project
// context, assemble the system prompt, stream, and relay. The caller
// has already ensured exactly the right user_message sits durable at
// the end of the log — this never appends one itself.
func (s *Service) runTurn(ctx context.Context, sessionID, userText, modelHint, skillHint, skillBody, route string, profile agents.Agent, needsTitle bool) (string, <-chan stream.StreamEvent, error) {
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
	events, err := s.log.Events(ctx, sessionID)
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
	system := assembleSystem(skills.Index(s.allowedPacks(ctx, profile)), time.Now())
	// The agent overlay is stable for a given agent, so it sits ahead
	// of the per-turn tail and stays inside the cacheable prefix.
	if profile.PromptOverlay != "" {
		system += "\n\n# Agent: " + profile.Name + "\n\n" + profile.PromptOverlay
	}
	if skillBody != "" {
		system += "\n\n# Skill: " + skillHint + "\n\n" + skillBody
	}
	if s.recall != nil && profile.Memory {
		if block := s.recall(ctx, sessionID, userText); block != "" {
			system += "\n\n" + block
		}
	}

	upstream, err := s.gw.Stream(ctx, gwclient.StreamRequest{
		Route:     route,
		Agent:     profile.Name,
		ToolAllow: profile.Tools,
		Purpose:   "chat",
		ModelHint: modelHint,
		System:    system,
		Messages:  msgs,
		SessionID: sessionID,
	})
	if err != nil {
		return sessionID, nil, err
	}

	out := make(chan stream.StreamEvent)
	go s.relay(ctx, sessionID, userText, route, profile, needsTitle, upstream, out)
	return sessionID, out, nil
}

// relay forwards events to the client while accumulating the turn,
// then persists it. Persistence uses a cancel-detached context: a
// client disconnect or brain shutdown mid-turn must still leave a
// durable pending_state (the kill-test contract).
func (s *Service) relay(ctx context.Context, sessionID, userText, route string, profile agents.Agent, needsTitle bool, upstream <-chan stream.StreamEvent, out chan<- stream.StreamEvent) {
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
		s.persistTurn(sessionID, userText, route, profile, needsTitle, text.String(), reasoning.String(), meta, usage, sawDone, flushed)
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

func (s *Service) persistTurn(sessionID, userText, route string, profile agents.Agent, needsTitle bool, text, reasoning string, meta *stream.Meta, usage *stream.Usage, sawDone bool, flushed int) {
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
	if err := s.log.SetLastRoute(wctx, sessionID, route, profile.Name); err != nil {
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
	if s.memory != nil && profile.Memory {
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
	if needsTitle {
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
		Route:    defaultRoute,
		Purpose:  "title",
		System:   titleSystem,
		Messages: []provider.Message{{Role: "user", Content: input}},
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

// hasCompletedTurn reports whether events carries at least one
// assistant_turn — the only kind persistTurn appends on sawDone.
// Gates auto-title: a session with no completed turn yet has never had
// a live shot at being titled (a turn that only ever failed skips
// autoTitle entirely), so retitling keeps being attempted on every
// later message or retry until one finally succeeds.
func hasCompletedTurn(events []session.Event) bool {
	for _, ev := range events {
		if ev.Kind == session.KindAssistantTurn {
			return true
		}
	}
	return false
}

// lastUserMessage returns the log's last event when — and only when —
// it is a user_message: the signature of a turn that never completed
// (persistTurn only appends assistant_turn on sawDone). Any other
// trailing kind means there's nothing dangling to retry.
func lastUserMessage(events []session.Event) (session.UserMessage, bool) {
	if len(events) == 0 || events[len(events)-1].Kind != session.KindUserMessage {
		return session.UserMessage{}, false
	}
	var msg session.UserMessage
	if err := json.Unmarshal(events[len(events)-1].Payload, &msg); err != nil {
		return session.UserMessage{}, false
	}
	return msg, true
}
