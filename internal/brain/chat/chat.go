// Package chat orchestrates one conversation turn: project the
// session's event log into context, stream through the gateway,
// persist the turn as events (with distilled residue), and keep the
// projection under budget. State lives in the log — a restart loses
// nothing (D-006).
package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
)

const (
	// autoAgentName is the request sentinel meaning "pick an agent for
	// me" (D-034 follow-up): the composer's "Auto" choice, resolved
	// through candidates+classify before the normal agent lookup.
	autoAgentName = "auto"
	// turnTimeout ceils a detached turn's lifetime (see Chat/Retry's
	// turnCtx): must exceed loop's permissionTimeout (10m) so a parked
	// permission ask can't be killed by the ceiling out from under it.
	turnTimeout = 30 * time.Minute
	// Each persistence stage gets its OWN deadline: LLM-backed stages
	// (distill, compaction) must never eat the database writes' clock.
	persistTimeout = 10 * time.Second
	distillBudget  = 190 * time.Second // two 90s attempts + slack
	// Compaction runs an extraction round trip AND a summarize on slow
	// reasoning providers; a tight budget starves the summarize and
	// compaction never converges. Post-turn passes are off the user's
	// clock; the rare pre-send pass accepts the latency.
	compactBudget = 150 * time.Second
	titleTimeout  = 15 * time.Second
	// approvalGrantTTL matches missions/driver.go's missionGrantTTL and
	// loop's own sessionGrantTTL (12h) — a chat session idle longer than
	// that just re-seeds the grant on its next turn, same degrade as a
	// mission outliving its grant.
	approvalGrantTTL = 12 * time.Hour
)

// ErrBadRequest marks caller mistakes (empty message) so the API can
// answer 400 instead of blaming the gateway.
var ErrBadRequest = errors.New("bad request")

// Gateway is the slice of the gateway client chat needs. RouteForRole
// resolves which route currently serves one of the 4 roles Timothy
// requires to work (D-049) — chat/turnmemory/compactor call it instead
// of hardcoding a route name.
type Gateway interface {
	Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error)
	RouteForRole(ctx context.Context, role string) (string, bool, error)
}

// SessionLog is the slice of the session store chat needs; tests fake
// it, session.Store satisfies it.
type SessionLog interface {
	Create(ctx context.Context, title string) (string, error)
	Events(ctx context.Context, sessionID string) ([]session.Event, error)
	Append(ctx context.Context, sessionID, kind string, payload any) (int64, error)
	SetTitleIfEmpty(ctx context.Context, id, title string) error
	SetLastRoute(ctx context.Context, id, route, agent string) error
	Knowledge(ctx context.Context, id string) ([]string, error)
	AddKnowledge(ctx context.Context, id string, names []string) error
}

// Distill extracts turn residue; loop.DistillTurn curried with the
// gateway in main, stubbed in tests. May return nil. route is "" for
// the normal side-call route, or the sensitive route pin when the turn
// executed a sensitive tool — same convention as MemoryExtract's route.
type Distill func(ctx context.Context, sessionID, turnText, route string) *session.TurnMemory

// Compactor keeps a session's projection under budget.
type Compactor interface {
	MaybeCompact(ctx context.Context, sessionID string) error
}

// MemoryExtract posts one turn's text to memoryd for long-term memory
// extraction. Fire-and-forget from chat's view: chat invokes it on a
// goroutine, the wrapper owns timeout and error logging, and no
// failure may touch the user-facing turn. route is "" for the normal
// side-call route, or the sensitive route pin when the turn executed a
// sensitive tool (see Service.sensitive).
type MemoryExtract func(ctx context.Context, sessionID string, seq int64, text string, route string)

// MemoryRetrieve returns the rendered long-term memory block for a
// user message, or "" for nothing relevant. The wrapper owns timeout
// and error handling; a failure returns "" — a turn without memories
// beats no turn.
type MemoryRetrieve func(ctx context.Context, sessionID, query string) string

// AttachmentStore is the slice of *attachments.Store chat needs: Get
// validates a ref exists (400 before any event append), Open resolves
// its bytes to base64 at request-build time. Nil disables attachments
// — a Request naming any Attachments id gets ErrBadRequest.
type AttachmentStore interface {
	Get(ctx context.Context, id string) (attachments.Attachment, error)
	Open(ctx context.Context, id string) (io.ReadCloser, attachments.Attachment, error)
}

// Granter is the narrow slice of *tools.Permissions chat needs to seed
// standing grants for a serving agent's ApprovalAllowlist — the same
// shape missions/driver.go's sessionGranter uses against the same
// session_grants table, so a grant recorded here is visible to (and
// glob-matched by) the exact same Permissions.Resolve chain a mission
// goes through.
type Granter interface {
	Grant(ctx context.Context, sessionID, tool, pattern string, ttl time.Duration) error
}

// Service orchestrates turns against the event store.
type Service struct {
	gw             Gateway
	log            SessionLog
	distill        Distill
	compactor      Compactor
	memory         MemoryExtract   // nil: long-term memory off
	recall         MemoryRetrieve  // nil: no memory injection
	agents         AgentResolver   // nil: zero-value agent (no skills/tools but retrieve_output, default route, memory on)
	candidates     AgentCandidates // nil: auto-dispatch falls back to default
	classify       agents.Classify // nil: auto-dispatch falls back to default
	budget         func(context.Context) int
	packs          []skills.Skill
	skillAllow     func(context.Context, string) bool // nil: all packs allowed
	skillBodies    map[string]string                  // name -> full pack body, for skill_hint
	flushEvery     time.Duration                      // pending-state flush cadence mid-stream
	turnTimeout    time.Duration                      // detached-turn ceiling; defaults to the turnTimeout const, overridable in tests
	sensitive      *session.SensitiveTools            // nil: no sensitive-tool route pin for side-calls
	attachments    AttachmentStore                    // nil: attachments disabled (ATTACHMENTS_DIR unset)
	markitdownURL  string                             // "": pdf attachments disabled (MARKITDOWN_URL unset)
	markitdownHTTP *http.Client                       // shared client for the markitdown sidecar call
	kbSearch       KBSearch                           // nil: kb_search never offered, regardless of agent config
	kbRead         KBRead                             // nil: kb_read never offered, regardless of agent config
	logger         *slog.Logger

	grants Granter // nil: chat never seeds standing grants (today's behavior)
	// seeded remembers which (session, agent) pairs already had their
	// ApprovalAllowlist granted this process's lifetime, so a long chat
	// session doesn't re-INSERT the same grant row on every turn.
	// Process-local by design (same tradeoff as driver.go's gatekeepers
	// map): a restart just re-seeds once more, a harmless duplicate row,
	// never a correctness problem, since matchGrant only cares whether
	// an unexpired matching row exists.
	seededMu sync.Mutex
	seeded   map[string]bool

	// turns is the live-turn broadcaster registry (broadcast.go): a
	// session ID present here means that session has a turn in flight,
	// full stop. There is no separate turn_active bool anywhere — the
	// GET /v1/sessions/{id} handler and the live-reattach endpoint both
	// read this same map through TurnActive/Subscribe, so the flag and
	// the broadcaster can never disagree about whether a turn is
	// running (no prior busy-state registry existed to reuse; this is
	// the only one).
	turnsMu sync.Mutex
	turns   map[string]*turnBroadcaster

	publishSession    func(sessionID string) // nil: no session-signal push (today's default)
	publishPermission func(sessionID string) // nil: no permission-signal push (today's default)
}

// roleRoute resolves the route currently bound to one of the 4 roles
// Timothy requires to work (D-049): "default" or "vision". Returns ""
// on any failure (role unbound, gateway unreachable) — callers must
// never hard-fail a turn over this; an empty route falls through to
// the gateway's own no_route error same as an unconfigured route
// always has.
func (s *Service) roleRoute(ctx context.Context, role string) string {
	name, ok, err := s.gw.RouteForRole(ctx, role)
	if err != nil || !ok {
		return ""
	}
	return name
}

// SetSessionHub wires the "session" signal push: fires once a turn's
// terminal is durable (see persistTurn), same after-commit ordering
// missions.Store/Notifier already use for their own hub.Publish calls.
// A plain func rather than an interface on *missions.Hub: chat sits
// lower than missions in the import graph (missions already reaches
// into session-shaped types; the reverse would cycle), so main wires
// this the same way it wires every other optional side-effect hook
// (SetMemoryExtract, SetApprovalGrants, ...). Nil (today's default,
// and every test) makes this a no-op.
func (s *Service) SetSessionHub(publish func(sessionID string)) {
	s.publishSession = publish
}

// SetPermissionHub wires the "permission" signal push: fires once a
// permission_request or permission_resolved event is durable (see
// notePermission), same pattern and same nil-safe default as
// SetSessionHub above — main wires both to the same missions.Hub, just
// a different Signal.Kind, so the web's one global /v1/events stream
// carries both without a second transport.
func (s *Service) SetPermissionHub(publish func(sessionID string)) {
	s.publishPermission = publish
}

// SetMemoryExtract wires the memoryd hook. Optional — nil leaves
// long-term memory off.
func (s *Service) SetMemoryExtract(fn MemoryExtract) { s.memory = fn }

// SetMemoryRetrieve wires per-turn memory recall. Optional.
func (s *Service) SetMemoryRetrieve(fn MemoryRetrieve) { s.recall = fn }

// SetSensitiveTools wires the sensitive-tool route pin for side-calls
// (memory extraction): a turn that executed a matching tool sends its
// extraction on t.Route instead of memoryd's own default. Optional —
// nil leaves every turn's side-calls on today's behavior.
func (s *Service) SetSensitiveTools(t *session.SensitiveTools) { s.sensitive = t }

// SetAttachments wires the attachment store (D-045). Optional — nil
// (ATTACHMENTS_DIR unset) rejects any Request naming Attachments ids.
func (s *Service) SetAttachments(store AttachmentStore) { s.attachments = store }

// SetMarkitdown wires the markitdown sidecar's base URL for PDF
// attachment conversion. Optional — empty (MARKITDOWN_URL unset)
// rejects any PDF attachment ref with ErrBadRequest.
func (s *Service) SetMarkitdown(url string) {
	s.markitdownURL = url
	if s.markitdownHTTP == nil {
		s.markitdownHTTP = &http.Client{}
	}
}

// KBSearch runs one knowledge-base search scoped to collectionNames —
// main curries memclient.Client.KBSearch in; collectionNames travels
// on every call, never bound once, since the same func serves every
// agent and each turn's collections are the SERVING agent's own
// Knowledge list (D-060: enforced here in Go, never a prompt).
type KBSearch func(ctx context.Context, query string, collectionNames []string, mode string, k int) ([]builtin.KBSearchHit, error)

// SetKBSearch wires the kb_search tool's backing search call. Optional
// — nil means kb_search is never offered on any turn, regardless of an
// agent's Knowledge list (same "the dependency's absence turns the
// feature off entirely" contract as SetMemoryRetrieve/SetAttachments).
func (s *Service) SetKBSearch(fn KBSearch) { s.kbSearch = fn }

// kbSearchTool builds this turn's kb_search ExtraTool bound to
// collections (the serving agent's Knowledge unioned with the
// session's own pinned list), or nil when kb_search must not be
// offered: no backing search call wired, or collections is empty
// (opt-in-only, same contract as Skills/Tools — this is the "exclude
// from the turn" choice, matching load_skill's precedent of leaving a
// tool off the offered surface entirely rather than having it answer
// with an error).
func (s *Service) kbSearchTool(collections []string) *tools.Tool {
	if s.kbSearch == nil || len(collections) == 0 {
		return nil
	}
	collections = slices.Clone(collections)
	return builtin.KBSearch(func(ctx context.Context, query, mode string, k int) ([]builtin.KBSearchHit, error) {
		return s.kbSearch(ctx, query, collections, mode, k)
	})
}

// KBRead loads one knowledge-base document scoped to collectionNames —
// same contract as KBSearch: collectionNames travels on every call and
// is the serving agent's own Knowledge list.
type KBRead func(ctx context.Context, documentID string, collectionNames []string) (builtin.KBDocument, error)

// SetKBRead wires the kb_read tool's backing lookup. Optional — same
// nil contract as SetKBSearch.
func (s *Service) SetKBRead(fn KBRead) { s.kbRead = fn }

// kbReadTool builds this turn's kb_read ExtraTool, gated exactly like
// kbSearchTool: no backend or empty collections means the tool is not
// offered.
func (s *Service) kbReadTool(collections []string) *tools.Tool {
	if s.kbRead == nil || len(collections) == 0 {
		return nil
	}
	collections = slices.Clone(collections)
	return builtin.KBRead(func(ctx context.Context, documentID string) (builtin.KBDocument, error) {
		return s.kbRead(ctx, documentID, collections)
	})
}

// SetAutoDispatch wires auto agent dispatch (D-034 follow-up): a
// request naming the autoAgentName sentinel resolves through candidates
// and classify instead of the named agent. Optional — nil candidates
// or classify makes the sentinel resolve to the default agent, same as
// an empty name.
func (s *Service) SetAutoDispatch(candidates AgentCandidates, classify agents.Classify) {
	s.candidates, s.classify = candidates, classify
}

// SetApprovalGrants wires the standing-grant seeder: once wired, a
// turn served by an agent with a non-empty ApprovalAllowlist gets
// those tools granted for its session before the turn runs, extending
// the user's Settings-authored per-agent consent to interactive chat.
// Optional — nil (today's default) leaves every allowlisted tool
// asking on its first chat call, exactly like before this existed.
//
// Safety framing (D-010 chain, see tools/permissions.go): this only
// ever ADDS a session_grants row that Permissions.Resolve already knew
// how to consult — it does not change the chain itself. Destructive-
// classified shell commands still hard-force DecisionAsk before any
// grant (session or allowlist-seeded) is even looked at, so an
// allowlisted destructive command parks exactly as it would for a
// mission. The allowlist is user-authored config (Settings' per-agent
// editor), not a model choice — seeding it as grants widens nothing
// beyond what the user already consented to for that agent.
func (s *Service) SetApprovalGrants(g Granter) {
	s.grants = g
	if s.seeded == nil {
		s.seeded = map[string]bool{}
	}
}

// seedApprovalGrants grants every tool in profile's ApprovalAllowlist
// for sessionID, once per (session, agent) — an agent switch mid-
// session (a new profile.ID) seeds again, since the key includes it.
// Best-effort and fire-and-forget on error, same convention as
// missions/driver.go's grantSessionDefaults: a failed grant just means
// this turn's allowlisted tools ask once more, never a broken turn.
func (s *Service) seedApprovalGrants(ctx context.Context, sessionID string, profile agents.Agent) {
	if s.grants == nil || len(profile.ApprovalAllowlist) == 0 {
		return
	}
	key := sessionID + "\x00" + profile.ID
	s.seededMu.Lock()
	if s.seeded[key] {
		s.seededMu.Unlock()
		return
	}
	s.seeded[key] = true
	s.seededMu.Unlock()

	for _, tool := range profile.ApprovalAllowlist {
		if err := s.grants.Grant(ctx, sessionID, tool, "*", approvalGrantTTL); err != nil {
			s.logger.Warn("chat: approval allowlist grant failed", "session_id", sessionID, "tool", tool, "error", err)
		}
	}
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
		flushEvery:  2 * time.Second, turnTimeout: turnTimeout, logger: logger,
	}
}

// SetTurnTimeout overrides the detached-turn ceiling (startup-only,
// same discipline as the loop's SetOffloadThreshold: called from
// main.go wiring before the service takes traffic, never after). The
// ceiling must exceed the loop's permission park timeout (10m) or a
// parked ask could never be answered in time; values at or below it
// are rejected so a bad env value degrades to the default rather than
// a broken permission flow.
func (s *Service) SetTurnTimeout(d time.Duration) error {
	if d <= 10*time.Minute {
		return fmt.Errorf("chat: turn timeout %s must exceed the 10m permission park timeout", d)
	}
	s.turnTimeout = d
	return nil
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

// ClassifyOverGateway builds an agents.Classify that asks the
// "summarize" role's route for a one-shot text reply — the same cheap
// side-call shape distillation and extraction already ride, resolved
// dynamically by role (D-049) rather than a hardcoded route name:
// a fixed name like "local" 502s with no_route on every install that
// never seeded a route by that exact name. "summarize" over "default"
// because the classification prompt carries the same message content
// distillation/extraction already send there — the old "local for
// privacy" rationale doesn't add anything a cheap cloud role doesn't
// already get. When the role is unbound (or the gateway lookup fails),
// this returns an error without calling Stream at all; agents.Dispatch
// already treats any classify error as "fall back to the default
// agent", so an unbound role just disables auto-dispatch rather than
// breaking the turn. Exported so main can wire it via SetAutoDispatch
// using the raw gateway client, bypassing the tool loop that only
// engages for Purpose=="chat".
func ClassifyOverGateway(gw Gateway) agents.Classify {
	return func(ctx context.Context, prompt string) (string, error) {
		route, ok, err := gw.RouteForRole(ctx, "summarize")
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("chat: classify: summarize role is unbound")
		}
		events, err := gw.Stream(ctx, gwclient.StreamRequest{
			Route:    route,
			Purpose:  "agent_dispatch",
			System:   "Answer with only what is requested — no prose, no explanation.",
			Messages: []provider.Message{{Role: "user", Content: prompt}},
			// MaxTokens includes a thinking model's reasoning tokens; 512
			// leaves room to think before the one-word answer.
			MaxTokens: 512,
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

// titleChatterPrefixes are stock openers a model reaches for when it
// answers or comments on the request instead of naming it — rejected
// case-insensitively, but only as whole leading words: "Okta setup"
// must not trip on "ok".
var titleChatterPrefixes = []string{
	"i'll", "i will", "i can", "i would", "sure", "okay", "ok",
	"let me", "here is", "here's", "certainly", "of course",
}

// validTitle rejects anything that isn't a short, plain name: empty,
// over 8 words, over 60 runes, carrying a newline/backtick/code fence,
// or opening with a chatter prefix — the shape of a model answering or
// commenting on the request instead of naming it.
func validTitle(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 60 {
		return false
	}
	if strings.ContainsAny(s, "\n`") {
		return false
	}
	if len(strings.Fields(s)) > 8 {
		return false
	}
	lower := strings.ToLower(s)
	for _, p := range titleChatterPrefixes {
		if lower == p || (strings.HasPrefix(lower, p) && !isWordChar(lower[len(p)])) {
			return false
		}
	}
	return true
}

// isWordChar reports whether b continues a word — used to bound a
// chatter-prefix match at a word boundary.
func isWordChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '\''
}

// TitleOverGateway mirrors autoTitle's mechanism (same system-prompt/
// MaxTokens-headroom/rune-safe-truncation shape) as a standalone
// one-shot call — for callers outside chat (missions naming a mission
// from its goal) that want a short display name generated the same
// way a session's title is, without the session/reply/sensitive-route
// ceremony a live chat turn carries. Titles are summarize-class work
// (D-049): routed by the "summarize" role, falling back to "default"
// when unbound, rather than burning the default chain's big model. A
// rejected reply (validTitle) retries once with the same params;
// still-invalid or any gateway failure returns "" (never an error) —
// best-effort, exactly like autoTitle's own logged-and-dropped failure
// path; the caller decides what "no name yet" means for its own
// fallback rendering. Every failure path logs through log (message
// prefix "title") so a silently-unnamed mission is diagnosable — log
// must be non-nil, same as Service's own s.logger.
func TitleOverGateway(gw Gateway, log *slog.Logger) func(ctx context.Context, input string) string {
	return func(ctx context.Context, input string) string {
		ctx, cancel := context.WithTimeout(ctx, titleTimeout)
		defer cancel()

		const titleSystem = `Produce a title for this conversation: at most 6 words, plain text, no quotes, no trailing punctuation. Do not perform, answer, or comment on the request — output only a short name for it. Reply with only the title.`
		route, ok, err := gw.RouteForRole(ctx, "summarize")
		if err != nil || !ok {
			route, ok, err = gw.RouteForRole(ctx, "default")
			if err != nil {
				log.Warn("title: route lookup failed", "error", err)
				return ""
			}
			if !ok {
				log.Warn("title: no route bound for summarize or default")
				return ""
			}
		}
		req := gwclient.StreamRequest{
			Route:    route,
			Purpose:  "title",
			System:   titleSystem,
			Messages: []provider.Message{{Role: "user", Content: input}},
			// Reasoning models spend hundreds of tokens thinking before
			// the first answer token; a tight cap truncates the stream
			// mid-reasoning and yields an empty title.
			MaxTokens: 1000,
		}
		for attempt := 0; attempt < 2; attempt++ {
			events, err := gw.Stream(ctx, req)
			if err != nil {
				log.Warn("title: stream failed", "error", err)
				return ""
			}
			var b strings.Builder
			var streamErr *stream.StreamError
			for ev := range events {
				switch ev.Type {
				case stream.EventChunk:
					b.WriteString(ev.Text)
				case stream.EventError:
					streamErr = ev.Err
				}
			}
			title := strings.TrimSpace(strings.Trim(strings.TrimSpace(b.String()), `"'`))
			title, _, _ = strings.Cut(title, "\n")
			title = strings.TrimSpace(title)
			if validTitle(title) {
				return truncateRunes(title, 80)
			}
			if title != "" {
				log.Warn("title: rejected by validTitle", "title", truncateRunes(title, 80))
			} else if streamErr != nil {
				log.Warn("title: stream error event", "code", streamErr.Code, "message", streamErr.Message)
			}
		}
		return ""
	}
}

// collectionClassifyDocRunes caps how much document text rides into the
// classify prompt — the model only needs the gist to pick a topic, not
// the full document (which can run to markitdown.TruncateMarkdown's own
// 128KB cap).
const collectionClassifyDocRunes = 4000

// CollectionChoice is ClassifyCollectionOverGateway's result: either an
// existing collection to file into (ExistingID) or a new one to create
// first (NewName/NewDesc). Exactly one of the two is ever set.
type CollectionChoice struct {
	ExistingID string
	NewName    string
	NewDesc    string
}

// unsortedCollectionName is the fallback CollectionChoice when
// classification can't be trusted (gateway error, empty/malformed
// reply) — a document must land somewhere, so it lands in a generic
// catch-all rather than blocking ingest on a model failure.
const unsortedCollectionName = "Unsorted"

// ClassifyCollectionOverGateway mirrors TitleOverGateway's mechanism
// (same route resolution, same Stream-and-drain, same never-errors
// contract) for a different one-shot job: given a document's title/text
// and the existing knowledge-base collections, pick the best matching
// collection or propose a new one. Free-text protocol, not JSON — this
// codebase has no precedent for parsing prose-as-JSON from a model
// reply (every json.Unmarshal on model output elsewhere unmarshals
// provider-structured tool-call arguments, never free text), so the
// model is asked for exactly one line: either a bare collection ID from
// the list, or "NEW: <name> | <description>". Any reply that doesn't
// parse cleanly (gateway error, empty stream, malformed line) falls
// back to unsortedCollectionName — the one guaranteed outcome, since
// auto-classify ingest has no path to ask the user instead.
func ClassifyCollectionOverGateway(gw Gateway, log *slog.Logger) func(ctx context.Context, docTitle, docText string, collections []kb.Collection) CollectionChoice {
	return func(ctx context.Context, docTitle, docText string, collections []kb.Collection) CollectionChoice {
		ctx, cancel := context.WithTimeout(ctx, titleTimeout)
		defer cancel()

		fallback := CollectionChoice{NewName: unsortedCollectionName}

		var list strings.Builder
		if len(collections) == 0 {
			list.WriteString("No collections exist yet.")
		} else {
			for _, c := range collections {
				fmt.Fprintf(&list, "%s: %s — %s\n", c.ID, c.Name, c.Description)
			}
		}

		const classifySystem = `You file documents into a knowledge base. Given a document and a list of existing collections, reply with exactly one line: either the bare id of the best-matching existing collection, or "NEW: <name> | <description>" to propose a new collection when nothing fits. Prefer an existing collection whenever the document broadly belongs to its topic — an imperfect match beats a new collection. A new collection's name must be a SHORT GENERIC TOPIC CATEGORY of 1-3 words (like "AI Agents", "Scalability", "Code Review", "CVs") that many future documents could belong to — never a title describing this specific document. The description should state the topic's scope broadly. No other text.`
		route, ok, err := gw.RouteForRole(ctx, "summarize")
		if err != nil || !ok {
			route, ok, err = gw.RouteForRole(ctx, "default")
			if err != nil {
				log.Warn("classify collection: route lookup failed", "error", err)
				return fallback
			}
			if !ok {
				log.Warn("classify collection: no route bound for summarize or default")
				return fallback
			}
		}

		docText = truncateRunes(docText, collectionClassifyDocRunes)
		user := fmt.Sprintf("Existing collections:\n%s\nDocument title: %s\n\n%s", list.String(), docTitle, docText)

		events, err := gw.Stream(ctx, gwclient.StreamRequest{
			Route:     route,
			Purpose:   "kb_classify",
			System:    classifySystem,
			Messages:  []provider.Message{{Role: "user", Content: user}},
			MaxTokens: 1000,
		})
		if err != nil {
			log.Warn("classify collection: stream failed", "error", err)
			return fallback
		}
		var b strings.Builder
		var streamErr *stream.StreamError
		for ev := range events {
			switch ev.Type {
			case stream.EventChunk:
				b.WriteString(ev.Text)
			case stream.EventError:
				streamErr = ev.Err
			}
		}
		reply, _, _ := strings.Cut(strings.TrimSpace(b.String()), "\n")
		reply = strings.TrimSpace(reply)
		if reply == "" {
			if streamErr != nil {
				log.Warn("classify collection: stream error event", "code", streamErr.Code, "message", streamErr.Message)
			}
			return fallback
		}
		for _, c := range collections {
			if reply == c.ID {
				return CollectionChoice{ExistingID: c.ID}
			}
		}
		if rest, ok := strings.CutPrefix(reply, "NEW:"); ok {
			name, desc, _ := strings.Cut(rest, "|")
			name = strings.TrimSpace(name)
			desc = strings.TrimSpace(desc)
			if name != "" {
				return CollectionChoice{NewName: truncateRunes(name, 80), NewDesc: truncateRunes(desc, 300)}
			}
		}
		log.Warn("classify collection: reply matched neither an existing id nor NEW:", "reply", truncateRunes(reply, 80))
		return fallback
	}
}

// allowedPacks filters the loaded packs through the global runtime
// allowlist AND the serving agent's own skill list (empty = none: an
// agent must opt into skills explicitly).
func (s *Service) allowedPacks(ctx context.Context, profile agents.Agent) []skills.Skill {
	out := make([]skills.Skill, 0, len(s.packs))
	for _, p := range s.packs {
		if s.skillAllow != nil && !s.skillAllow(ctx, p.Name) {
			continue
		}
		if !profileAllowsSkill(profile.Skills, p.Name) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// profileAllowsSkill checks one agent's skills allowlist: empty denies
// every pack (an agent must opt into skills explicitly).
func profileAllowsSkill(allow []string, name string) bool {
	for _, n := range allow {
		if n == name {
			return true
		}
	}
	return false
}

// retrieveOutputTool and loadSkillTool are the builtin tools' exact
// registered names (internal/brain/tools/builtin/retrieve.go,
// internal/brain/skills/tool.go) — not imported as constants to avoid
// pulling chat into the builtin package's dependency graph for two
// literal strings, same convention as the sensitive-tool suffixes in
// cmd/brain/main.go.
const (
	retrieveOutputTool = "retrieve_output"
	loadSkillTool      = "load_skill"
)

// resolveToolAllow builds the tool allowlist actually sent to the
// loop from the serving agent's config: empty means no tools (an
// agent must opt into tools explicitly), the same flip as skills.
// Two exemptions, independent of the agent's own list:
//   - retrieve_output always stays available — it is how the model
//     reads back its own offloaded tool results (D-019); filtering it
//     out would silently strand any result too big to inline.
//   - load_skill follows the SKILLS allowlist, not the tools one: it
//     is present only when the agent has at least one skill to load
//     (an agent with none has nothing load_skill could load, and
//     omitting it saves its schema's tokens on every skill-less turn).
//
// Both exemptions are enforced whether or not the agent authored a
// tool list — an agent that picks tools in the UI should not silently
// lose offload retrieval or skill loading by not knowing the infra
// names. profile.Tools itself is never mutated.
func resolveToolAllow(profile agents.Agent) []string {
	allow := profile.Tools
	if !slices.Contains(allow, retrieveOutputTool) {
		allow = append(slices.Clone(allow), retrieveOutputTool)
	}
	if len(profile.Skills) > 0 && !slices.Contains(allow, loadSkillTool) {
		allow = append(slices.Clone(allow), loadSkillTool)
	}
	return allow
}

// Request is one chat turn.
type Request struct {
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message"`
	// Agent names who serves this turn; empty = the default agent.
	Agent     string `json:"agent,omitempty"`
	Route     string `json:"route,omitempty"`
	ModelHint string `json:"model_hint,omitempty"`
	// SkillHint names a skill pack to force-load for this turn — set
	// when the user picked one explicitly (a UI chip, not parsed
	// text). Unlike load_skill, this is not a choice the model can
	// skip: the pack's body is in the system prompt before the first
	// token streams.
	SkillHint string `json:"skill_hint,omitempty"`
	// Attachments are attachment ids (internal/brain/attachments)
	// the user attached to this message — refs only, resolved into
	// base64 at request-build time (D-045).
	Attachments []string `json:"attachments,omitempty"`
	// Knowledge names kb collections the user pinned to this turn's
	// session (composer # mentions) — unioned into the session's
	// stored knowledge list before the turn runs.
	Knowledge []string `json:"knowledge,omitempty"`
}

// Chat streams one turn. The user message is durably appended before
// the provider is called; the assistant turn (or a pending_state on
// abnormal end) is appended when the stream finishes. The returned
// channel follows the stream package's terminal contract.
func (s *Service) Chat(ctx context.Context, req Request) (string, <-chan stream.StreamEvent, error) {
	if strings.TrimSpace(req.Message) == "" && len(req.Attachments) == 0 {
		return "", nil, fmt.Errorf("chat: %w: message or an attachment is required", ErrBadRequest)
	}
	images, documents, err := s.validateAttachments(ctx, req.Attachments)
	if err != nil {
		return "", nil, err
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
			!profileAllowsSkill(profile.Skills, req.SkillHint) {
			return "", nil, fmt.Errorf("chat: %w: unknown skill %q", ErrBadRequest, req.SkillHint)
		}
		skillBody = body
	}
	// Routing precedence: explicit request override, then images auto-flip
	// to the vision route, then the agent's route, then the default chain
	// (sensitive-pin below still overrides all of this).
	route := req.Route
	// Only images flip to the vision route — documents are converted to
	// plain markdown text, not something the model needs vision
	// capability to read.
	if route == "" && len(images) > 0 {
		// D-046: the gateway-side safety net (internal/gateway/api/api.go's
		// handleStream) falls back to the default role's route when the
		// vision role resolves to a missing/disabled/empty chain, so
		// installs that never bound the vision role still work; an
		// existing vision route wins outright since its cheapest-first
		// chain is exactly the point of this feature.
		route = s.roleRoute(ctx, "vision")
	}
	if route == "" {
		route = profile.Route
	}
	if route == "" {
		route = s.roleRoute(ctx, "default")
	}

	sessionID := req.SessionID
	if sessionID == "" {
		id, err := s.log.Create(ctx, "")
		if err != nil {
			return "", nil, err
		}
		sessionID = id
	}
	if len(req.Knowledge) > 0 {
		if err := s.log.AddKnowledge(ctx, sessionID, req.Knowledge); err != nil {
			return sessionID, nil, err
		}
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
	modelHint := req.ModelHint
	route, modelHint = s.pinSensitiveRoute(ctx, events, route, modelHint)

	// Exclusivity must be won BEFORE the first event append of the turn
	// (D-042): a loser must never persist even the user_message, or two
	// concurrent requests interleave writes on the same append-only log.
	bc, err := s.turnBegin(sessionID)
	if err != nil {
		return sessionID, nil, err
	}

	// The turn's lifetime belongs to the session, not the HTTP request
	// that started it: a navigation/reload aborts ctx, but that must
	// not kill the loop mid-tool-call. turnCtx drops ctx's cancellation
	// (WithoutCancel) while keeping its values (auth, trace); StopTurn
	// is the only thing allowed to cancel it from here on (see
	// broadcast.go's setCancel). The user_message append below also
	// rides turnCtx, not ctx: once the slot is won, the message must
	// land even if the client vanishes that same instant.
	turnCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.turnTimeout)
	bc.setCancel(cancel)

	if _, err := s.log.Append(turnCtx, sessionID, session.KindUserMessage, session.UserMessage{
		Text: req.Message, Route: route, Agent: profile.Name, ModelHint: modelHint, Images: images, Documents: documents,
	}); err != nil {
		s.turnDone(sessionID)
		return sessionID, nil, err
	}
	return s.runTurn(turnCtx, ctx, sessionID, req.Message, modelHint, req.SkillHint, skillBody, route, profile, needsTitle, bc)
}

// resolveImages fills each message's Images from its ImageRefs
// in-place, reading each attachment's bytes and base64-encoding them.
// The only call site (runTurn, immediately before the gateway request
// is built) — nothing else in the turn's lifecycle ever needs bytes
// (D-045). A no-op when nothing carries refs (the common case). Once
// resolved, ImageRefs is cleared: it never crosses the gwclient wire
// anyway (json:"-"), but clearing avoids holding two views of the same
// data past the point either is needed.
func (s *Service) resolveImages(ctx context.Context, msgs []provider.Message) error {
	for i := range msgs {
		if len(msgs[i].ImageRefs) == 0 {
			continue
		}
		if s.attachments == nil {
			return fmt.Errorf("chat: resolve images: attachments are not enabled")
		}
		images := make([]provider.ImageData, 0, len(msgs[i].ImageRefs))
		for _, ref := range msgs[i].ImageRefs {
			data, err := s.readAttachment(ctx, ref.ID)
			if err != nil {
				return fmt.Errorf("chat: resolve image %q: %w", ref.ID, err)
			}
			images = append(images, provider.ImageData{MediaType: ref.Mime, Data: data})
		}
		msgs[i].Images = images
		msgs[i].ImageRefs = nil
	}
	return nil
}

// readAttachment opens and base64-encodes one attachment's bytes.
func (s *Service) readAttachment(ctx context.Context, id string) (string, error) {
	r, _, err := s.attachments.Open(ctx, id)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// validateAttachments checks every id exists in the attachment store
// BEFORE the turn's exclusivity is won or anything is appended (D-042:
// store lookups are read-only and safe before turnBegin, but a bad ref
// must 400 before even the user_message persists). Empty ids returns
// nil, nil, nil without touching the store — attachments stay off when
// unconfigured, and a message with none never needs it wired.
//
// PDFs are converted to markdown here, once, at send time (not lazily
// per turn like images): re-converting on every turn would re-call the
// markitdown sidecar every turn, and any output drift would rewrite an
// earlier projected message — breaking LLMContext's prefix stability
// that provider prompt caches depend on. A conversion failure is the
// sidecar's fault, not the client's, so it returns a plain wrapped
// error rather than ErrBadRequest.
func (s *Service) validateAttachments(ctx context.Context, ids []string) ([]session.ImageRef, []session.DocumentRef, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	if s.attachments == nil {
		return nil, nil, fmt.Errorf("chat: %w: attachments are not enabled", ErrBadRequest)
	}
	var images []session.ImageRef
	var documents []session.DocumentRef
	for _, id := range ids {
		att, err := s.attachments.Get(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("chat: %w: attachment %q: %v", ErrBadRequest, id, err)
		}
		switch {
		case strings.HasPrefix(att.Mime, "image/"):
			images = append(images, session.ImageRef{ID: att.ID, Mime: att.Mime})
		case att.Mime == "application/pdf":
			if s.markitdownURL == "" {
				return nil, nil, fmt.Errorf("chat: %w: pdf attachments require the markitdown sidecar (MARKITDOWN_URL)", ErrBadRequest)
			}
			r, _, err := s.attachments.Open(ctx, att.ID)
			if err != nil {
				return nil, nil, fmt.Errorf("chat: %w: attachment %q: %v", ErrBadRequest, id, err)
			}
			raw, err := io.ReadAll(r)
			_ = r.Close()
			if err != nil {
				return nil, nil, fmt.Errorf("chat: read attachment %q: %w", id, err)
			}
			md, err := markitdown.Convert(ctx, s.markitdownHTTP, s.markitdownURL, att.ID+".pdf", att.Mime, raw)
			if err != nil {
				return nil, nil, fmt.Errorf("chat: convert attachment %q: %w", id, err)
			}
			documents = append(documents, session.DocumentRef{ID: att.ID, Mime: att.Mime, Markdown: markitdown.TruncateMarkdown(md)})
		default:
			return nil, nil, fmt.Errorf("chat: %w: attachment %q: unsupported mime %q", ErrBadRequest, id, att.Mime)
		}
	}
	return images, documents, nil
}

// pinSensitiveRoute promotes the session-wide privacy floor above the
// turn-scoped one (SetForceRoute/turnSensitive): once ANY prior turn in
// the session has executed a sensitive tool, the email/etc. content it
// pulled into context is still there on every LATER turn even though
// this turn ran no sensitive tool itself — so every subsequent turn
// must start on the sensitive route, not just the turn that triggered
// it. Overrides the route picker, the agent's own route, and the
// default alike (same "safety floor beats user choice" rule the in-turn
// pin already enforces), and drops modelHint for the same reason
// SetForceRoute does: a hint outranks the route at the gateway's
// Resolve, so a surviving hint would let the model bypass the pinned
// route's chain entirely. A no-op whenever sensitive-tool routing is
// unconfigured (s.sensitive nil or its Route resolves empty) — current
// behavior everywhere until an operator sets sensitive_tool_route.
func (s *Service) pinSensitiveRoute(ctx context.Context, events []session.Event, route, modelHint string) (string, string) {
	if s.sensitive == nil || s.sensitive.Route == nil {
		return route, modelHint
	}
	if !s.sensitive.SessionSensitive(ctx, events) {
		return route, modelHint
	}
	if forced := s.sensitive.Route(ctx); forced != "" {
		return forced, ""
	}
	return route, modelHint
}

// ErrNoRetryableTurn marks a Retry call whose session isn't in a
// retryable state — its last user_message already has a completed
// (assistant_turn) turn after it, or there's no user_message at all.
var ErrNoRetryableTurn = errors.New("no retryable turn")

// Retry re-runs generation for a session's last turn WITHOUT persisting
// a second user_message: Chat unconditionally appends before streaming
// (line above), so a failed attempt already leaves that message durable
// with no assistant_turn after it. A dead attempt (brain restart mid-turn)
// can also leave trailing tool_execution/pending_state/permission events
// after that message — lastUserMessage looks past those; only a
// completed turn blocks retry. Retry reuses the message verbatim — same
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
		route = s.roleRoute(ctx, "default")
	}
	modelHint := last.ModelHint
	route, modelHint = s.pinSensitiveRoute(ctx, events, route, modelHint)

	// Same exclusivity acquisition as Chat (D-042): Retry appends no new
	// user_message of its own, but it must still win the slot before
	// runTurn can touch the log — a losing Retry racing a Chat (or
	// another Retry) must not interleave events with the winner's turn.
	bc, err := s.turnBegin(sessionID)
	if err != nil {
		return sessionID, nil, err
	}
	// Detached turn context — same reasoning as Chat's (see there).
	turnCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.turnTimeout)
	bc.setCancel(cancel)
	return s.runTurn(turnCtx, ctx, sessionID, last.Text, modelHint, "", "", route, profile, needsTitle, bc)
}

// runTurn is the shared tail of Chat and Retry: compact, project
// context, assemble the system prompt, stream, and relay. The caller
// has already ensured exactly the right user_message sits durable at
// the end of the log — this never appends one itself. bc is this
// turn's broadcaster, already registered by the caller's turnBegin
// (D-042) — runTurn must free it (turnDone) on every path that returns
// before the relay goroutine takes ownership of that responsibility,
// so a setup failure here can never wedge the session's slot.
//
// turnCtx is the detached, session-owned context (survives the client
// disconnecting) that everything in this function — and the whole tool
// loop underneath gw.Stream — runs on. reqCtx is the original HTTP
// request's context; it is only handed to relay, which uses it solely
// to decide whether the ORIGINAL client is still there to stream to
// (see relay's doc comment).
func (s *Service) runTurn(turnCtx, reqCtx context.Context, sessionID, userText, modelHint, skillHint, skillBody, route string, profile agents.Agent, needsTitle bool, bc *turnBroadcaster) (string, <-chan stream.StreamEvent, error) {
	// start marks the turn's wall-clock beginning for the stats line's
	// duration: here, not Chat/Retry's entry, so a retried turn's
	// duration covers only the retried run — the dead attempt's gap
	// never counts (runTurn is the shared tail both call fresh).
	start := time.Now()
	s.seedApprovalGrants(turnCtx, sessionID, profile)
	// Pre-send guarantee: the context actually sent to the provider
	// stays under budget even on the turn that crosses it. The
	// post-turn pass below keeps sessions compacted ahead of time, so
	// this is a cheap no-op except on the crossing turn; best-effort —
	// an oversized context beats no answer.
	if s.compactor != nil {
		cctx, cancel := context.WithTimeout(turnCtx, compactBudget)
		if err := s.compactor.MaybeCompact(cctx, sessionID); err != nil {
			s.logger.Warn("pre-send compaction", "session_id", sessionID, "error", err)
		}
		cancel()
	}
	events, err := s.log.Events(turnCtx, sessionID)
	if err != nil {
		s.turnDone(sessionID)
		return sessionID, nil, err
	}
	msgs, err := session.LLMContext(events, s.budget(turnCtx))
	if err != nil {
		s.turnDone(sessionID)
		return sessionID, nil, err
	}
	// Resolve image refs into base64 ONLY here, right before the
	// gateway call: this is the sole point in the whole turn where
	// attachment bytes exist in memory at all (D-045). ImageRefs never
	// crosses the wire (json:"-"); Images (the resolved payload) does.
	if err := s.resolveImages(turnCtx, msgs); err != nil {
		s.turnDone(sessionID)
		return sessionID, nil, err
	}
	// sessionSensitive reuses this same fetch: a session pinned by a
	// PRIOR turn still carries that turn's sensitive content in the
	// context just projected above, even when THIS turn runs no
	// sensitive tool itself — so persistTurn's side-calls (distill,
	// extraction) must be pinned too, same as turnSensitive.
	sessionSensitive := s.sensitive.SessionSensitive(turnCtx, events)

	// Retrieved memory and a hinted skill both ride the system
	// prompt's TAIL: the stable prefix stays byte-identical for
	// provider prompt caches (D-018) while the per-turn additions vary
	// after it. The memory block is fenced DATA, never instructions
	// (D-011 poisoning defense); the skill body is instructions the
	// user explicitly selected, deterministically loaded rather than
	// left to the model's load_skill judgment.
	system := assembleSystem(skills.Index(s.allowedPacks(turnCtx, profile)), time.Now())
	// The agent overlay is stable for a given agent, so it sits ahead
	// of the per-turn tail and stays inside the cacheable prefix.
	if profile.PromptOverlay != "" {
		system += "\n\n# Agent: " + profile.Name + "\n\n" + profile.PromptOverlay
	}
	if skillBody != "" {
		system += "\n\n# Skill: " + skillHint + "\n\n" + skillBody
	}
	if s.recall != nil && profile.Memory {
		if block := s.recall(turnCtx, sessionID, userText); block != "" {
			system += "\n\n" + block
		}
	}

	// Collections offered this turn: the serving agent's own Knowledge
	// list unioned with the session's pinned list (composer # mentions).
	// The session lookup is best-effort — a failure must not kill the
	// turn, it just falls back to the agent's list alone. Fetched here
	// (not just before extraTools) so the same result also backs the
	// pinned-knowledge system prompt block below.
	collections := slices.Clone(profile.Knowledge)
	sk, skErr := s.log.Knowledge(turnCtx, sessionID)
	if skErr != nil {
		s.logger.Warn("session knowledge lookup", "session_id", sessionID, "error", skErr)
	} else {
		for _, name := range sk {
			if !slices.Contains(collections, name) {
				collections = append(collections, name)
			}
		}
	}

	// A pinned collection is an explicit user signal to search it, not
	// just a passive tool grant — but only when kb_search will actually
	// be offered (skErr nil, s.kbSearch wired, union non-empty): a
	// pinned name with no backend must never promise a tool that isn't
	// there.
	if skErr == nil && len(sk) > 0 && s.kbSearch != nil && len(collections) > 0 {
		system += "\n\n# Pinned knowledge\n\nThe user pinned these knowledge collections to this session: " + strings.Join(sk, ", ") + ". This is an explicit signal: when a question could plausibly be answered by their content, call kb_search first and ground the answer in what it returns, rather than answering from general knowledge alone."
	}

	var extraTools []*tools.Tool
	if t := s.kbSearchTool(collections); t != nil {
		extraTools = append(extraTools, t)
	}
	if t := s.kbReadTool(collections); t != nil {
		extraTools = append(extraTools, t)
	}
	upstream, err := s.gw.Stream(turnCtx, gwclient.StreamRequest{
		Route:      route,
		Agent:      profile.Name,
		ToolAllow:  resolveToolAllow(profile),
		ExtraTools: extraTools,
		Purpose:    "chat",
		ModelHint:  modelHint,
		System:     system,
		Messages:   msgs,
		SessionID:  sessionID,
	})
	if err != nil {
		s.turnDone(sessionID)
		return sessionID, nil, err
	}

	// bc was registered by the caller's turnBegin before the turn's
	// first event append (D-042) — already live the instant the turn
	// started, not just from here on. From this point, relay's own
	// defer-equivalent (drainAndPersist, on every exit path) owns
	// freeing it via turnDone.
	out := make(chan stream.StreamEvent)
	go s.relay(reqCtx, sessionID, userText, route, profile, needsTitle, sessionSensitive, start, bc, upstream, out)
	return sessionID, out, nil
}

// relay forwards events to the client while accumulating the turn,
// then persists it. Persistence uses a cancel-detached context: a
// client disconnect or brain shutdown mid-turn must still leave a
// durable pending_state (the kill-test contract). sessionSensitive
// carries forward whether a PRIOR turn in this session already pinned
// it (see runTurn) — persistTurn ORs it with this turn's own
// turnSensitive flip so side-calls stay pinned on every later turn too.
// start is runTurn's wall-clock beginning, stamped onto the terminal
// done event's Meta (and passed to persistTurn) so live and replayed
// stats show the same duration. bc is this turn's broadcaster
// (registered by runTurn before this goroutine started): every event
// this relay sees — including ones the ORIGINAL client never gets
// because it already disconnected (drainAndPersist) — still reaches
// bc, since a GET /live subscriber may be attached independently of
// the POST that started the turn.
//
// reqCtx is the ORIGINAL request's context, not the turn's — upstream
// is already bound to the detached turnCtx (runTurn's gw.Stream call),
// so it keeps producing long after reqCtx dies. reqCtx here gates only
// whether THIS client is still around to receive forwarded events: the
// moment it's done, the select's <-reqCtx.Done() branch stops sending
// to out and falls through to drainAndPersist, which keeps consuming
// the still-live upstream to completion so the turn finishes and
// persists normally, with every event still reaching bc for any /live
// subscriber.
func (s *Service) relay(reqCtx context.Context, sessionID, userText, route string, profile agents.Agent, needsTitle, sessionSensitive bool, start time.Time, bc *turnBroadcaster, upstream <-chan stream.StreamEvent, out chan<- stream.StreamEvent) {
	var text, reasoning strings.Builder
	var meta *stream.Meta
	var usage *stream.Usage
	sawDone := false
	flushed := 0
	// turnSensitive flips once any tool this turn executed matches
	// s.sensitive — memory extraction then rides the sensitive route
	// pin instead of its own default (see persistTurn).
	turnSensitive := false
	// ranTool records whether ANY tool executed this turn (regardless of
	// sensitivity) — persistTurn's empty-response guard (D-044) must not
	// flag a turn whose answer is entirely tool/reasoning traffic with
	// no text.
	ranTool := false
	// failure captures a terminal EventError/EventIncomplete's code and
	// message (D-044): the relay used to just drop these on the floor
	// after streaming them to the client, leaving a turn with nothing
	// persisted. persistTurn appends it as KindTurnFailed when there is
	// no partial text worth keeping instead.
	var failure *session.TurnFailed
	noteToolResult := func(ev stream.StreamEvent) {
		if ev.Type != stream.EventToolResult || ev.ToolResult == nil {
			return
		}
		ranTool = true
		if s.sensitive.Matches(reqCtx, ev.ToolResult.Name) {
			turnSensitive = true
		}
	}
	noteFailure := func(ev stream.StreamEvent) {
		switch ev.Type {
		case stream.EventError:
			if ev.Err != nil {
				failure = &session.TurnFailed{Code: ev.Err.Code, Message: ev.Err.Message}
			}
		case stream.EventIncomplete:
			failure = &session.TurnFailed{Code: "incomplete", Message: ev.Text}
		}
	}

	// notePermission persists an ask (and its resolution) the moment it
	// crosses the relay, not at turn end: a parked turn can sit for the
	// full permissionTimeout, and the whole point of this event is that
	// a session opened while that's happening shows the same prompt the
	// live stream shows. Same detached-timeout, best-effort pattern as
	// flushPending — a failed write here loses the replay prompt, never
	// the turn itself.
	notePermission := func(ev stream.StreamEvent) {
		var kind string
		var payload any
		switch ev.Type {
		case stream.EventPermissionRequest:
			if ev.Permission == nil {
				return
			}
			kind = session.KindPermissionRequest
			payload = session.PermissionRequest{
				ID: ev.Permission.ID, CallID: ev.Permission.CallID, Tool: ev.Permission.Tool,
				Args: ev.Permission.Args, Danger: ev.Permission.Danger, Rationale: ev.Permission.Rationale,
			}
		case stream.EventPermissionResolved:
			if ev.Resolved == nil {
				return
			}
			kind = session.KindPermissionResolved
			payload = session.PermissionResolved{ID: ev.Resolved.ID, Decision: ev.Resolved.Decision}
		default:
			return
		}
		wctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
		defer cancel()
		if _, err := s.log.Append(wctx, sessionID, kind, payload); err != nil {
			s.logger.Warn("persist permission event", "session_id", sessionID, "kind", kind, "error", err)
			return
		}
		if s.publishPermission != nil {
			s.publishPermission(sessionID)
		}
	}

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
		// out closes FIRST, before draining upstream: upstream is bound
		// to turnCtx (session-owned), not reqCtx, so once the client is
		// gone it may still have minutes left to run. Closing out up
		// front frees streamTurn's client-facing goroutine immediately
		// instead of holding it for the remainder of the turn — any
		// further send() calls on a dead ResponseWriter just fail
		// silently. The drain loop below then keeps consuming upstream
		// (publishing every event to bc for /live subscribers) until it
		// closes on its own, however long that takes.
		close(out)
		for ev := range upstream {
			switch ev.Type {
			case stream.EventChunk:
				text.WriteString(ev.Text)
			case stream.EventUsage:
				usage = ev.Usage
			case stream.EventDone:
				sawDone = true
				meta = stampDuration(ev.Meta, start)
				ev.Meta = meta
			}
			noteToolResult(ev)
			noteFailure(ev)
			notePermission(ev)
			bc.publish(ev)
		}
		s.persistTurn(sessionID, userText, route, profile, needsTitle, text.String(), reasoning.String(), meta, usage, sawDone, flushed, turnSensitive || sessionSensitive, failure, ranTool)
		// Terminal persist is now durable: free the broadcaster (closes
		// every live /live subscriber) and push the session signal, in
		// that order — mirrors missions.Store's own "publish only after
		// commit" discipline, so a client that sees turn_active flip
		// false (via the freed entry) or gets a session signal is
		// guaranteed the transcript already reflects the finished turn.
		s.turnDone(sessionID)
		if s.publishSession != nil {
			s.publishSession(sessionID)
		}
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
				meta = stampDuration(ev.Meta, start)
				ev.Meta = meta
			}
			noteToolResult(ev)
			noteFailure(ev)
			notePermission(ev)
			bc.publish(ev)
			select {
			case out <- ev:
			case <-reqCtx.Done():
				flushPending()
				drainAndPersist()
				return
			}
		case <-ticker.C:
			flushPending()
		case <-reqCtx.Done():
			flushPending()
			drainAndPersist()
			return
		}
	}
}

// stampDuration copies base (the gateway-attributed Meta, possibly
// nil when no provider attempt succeeded) and sets DurationMs from
// start — a copy because base may be a shared struct decoded once per
// SSE event, not something relay owns to mutate in place.
func stampDuration(base *stream.Meta, start time.Time) *stream.Meta {
	m := stream.Meta{}
	if base != nil {
		m = *base
	}
	m.DurationMs = time.Since(start).Milliseconds()
	return &m
}

func (s *Service) persistTurn(sessionID, userText, route string, profile agents.Agent, needsTitle bool, text, reasoning string, meta *stream.Meta, usage *stream.Usage, sawDone bool, flushed int, sensitive bool, failure *session.TurnFailed, ranTool bool) {
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
			return
		}
		// No partial worth keeping: a captured terminal error/incomplete
		// (D-044) becomes durable evidence instead of the turn silently
		// vanishing — same detached-context, best-effort append as the
		// pending-state write above. No failure AND no text at all means
		// every upstream producer lost its terminal on the way here
		// (deadline racing the stream cut) — synthesize one rather than
		// append nothing: a turn that ran must never leave zero events.
		// A flushed partial (text non-empty, already checkpointed) keeps
		// today's behavior: the pending state is the evidence.
		if failure == nil && text == "" {
			failure = &session.TurnFailed{
				Code:    "turn_aborted",
				Message: "turn ended with no terminal event (deadline exceeded or stream cut)",
			}
		}
		if failure != nil {
			ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
			defer cancel()
			if _, err := s.log.Append(ctx, sessionID, session.KindTurnFailed, *failure); err != nil {
				s.logger.Error("persist turn failed", "session_id", sessionID, "error", err)
			}
		}
		return
	}

	// A completed turn with no text, no reasoning, and no tool
	// executions is not a success (D-044) — persist evidence instead of
	// a blank assistant_turn. Keyed on all three being empty so a
	// tool/reasoning-only turn (a real, valid shape some providers use)
	// stays untouched.
	if text == "" && reasoning == "" && !ranTool {
		ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
		defer cancel()
		if _, err := s.log.Append(ctx, sessionID, session.KindTurnFailed, session.TurnFailed{
			Code: "empty_response", Message: "the turn completed with no text, reasoning, or tool calls",
		}); err != nil {
			s.logger.Error("persist turn failed", "session_id", sessionID, "error", err)
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
		turn.DurationMs = meta.DurationMs
		turn.Cost, turn.Currency = meta.Cost, meta.Currency
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

	// A turn that executed a sensitive tool itself, OR one served on an
	// already session-pinned route (a PRIOR turn's sensitive content is
	// still in this turn's context — see runTurn's sessionSensitive),
	// pins its side-calls to the same route the loop/pin already forced
	// the turn onto — both distillation and extraction below can see raw
	// sensitive output (e.g. email), so neither may fall back to its own
	// cheap default.
	sensitiveRoute := ""
	if sensitive && s.sensitive != nil && s.sensitive.Route != nil {
		sensitiveRoute = s.sensitive.Route(context.Background())
	}

	var tm *session.TurnMemory
	if s.distill != nil && text != "" {
		dctx, dcancel := context.WithTimeout(context.Background(), distillBudget)
		tm = s.distill(dctx, sessionID, "user: "+userText+"\n\nassistant: "+text, sensitiveRoute)
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
		go s.memory(context.Background(), sessionID, turnSeq, mtext, sensitiveRoute)
	}

	if s.compactor != nil {
		cctx, cancel := context.WithTimeout(context.Background(), compactBudget)
		if err := s.compactor.MaybeCompact(cctx, sessionID); err != nil {
			s.logger.Error("compaction", "session_id", sessionID, "error", err)
		}
		cancel()
	}
	if needsTitle {
		s.autoTitle(sessionID, userText, text, sensitiveRoute)
	}
}

// autoTitle names a session after its first exchange via a mini call;
// best-effort and never clobbers a user-chosen title. sensitiveRoute is
// the same pin persistTurn resolved for distill/extraction above: the
// title prompt's input includes reply (the assistant's own synthesized
// text, truncated), which is exactly the channel a sensitive tool's
// output can be quoted through — the same reasoning distill/extract
// already pin on — so titling rides the identical route pin rather than
// staying on the default role's route whenever this turn (or an
// earlier one this session) is sensitive. An empty sensitiveRoute (the
// common case) falls through to the default role's route exactly as
// before.
func (s *Service) autoTitle(sessionID, userText, reply, sensitiveRoute string) {
	ctx, cancel := context.WithTimeout(context.Background(), titleTimeout)
	defer cancel()

	const titleSystem = `Produce a title for this conversation: at most 6 words, plain text, no quotes, no trailing punctuation. Reply with only the title.`
	input := userText + "\n\n" + truncateRunes(reply, 200)

	route := s.roleRoute(ctx, "default")
	if sensitiveRoute != "" {
		route = sensitiveRoute
	}
	events, err := s.gw.Stream(ctx, gwclient.StreamRequest{
		Route:    route,
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

// lastUserMessage returns the session's last user_message when it has
// no completed turn after it — the signature of a turn that never
// finished (persistTurn only appends assistant_turn on sawDone). A
// brain restart or crash mid-turn can leave trailing tool_execution,
// pending_state, or permission_request/resolved events after that
// message; none of those are turn-terminal, so they must not block a
// retry. LLMContext already treats them as inert for anything but the
// newest pending_state (spliced in as an interrupted assistant message
// only if still live), so replaying them costs nothing — the retried
// turn's context is exactly what the dead attempt's would have been.
func lastUserMessage(events []session.Event) (session.UserMessage, bool) {
	var lastMsg *session.Event
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Kind {
		case session.KindAssistantTurn:
			return session.UserMessage{}, false // turn after it completed
		case session.KindUserMessage:
			lastMsg = &events[i]
		}
		if lastMsg != nil {
			break
		}
	}
	if lastMsg == nil {
		return session.UserMessage{}, false
	}
	var msg session.UserMessage
	if err := json.Unmarshal(lastMsg.Payload, &msg); err != nil {
		return session.UserMessage{}, false
	}
	return msg, true
}
