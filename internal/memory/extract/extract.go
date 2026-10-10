// Package extract turns raw conversation turns into staged long-term
// memories (D-011). One mini LLM call proposes atomic facts; code -
// never the model - validates them, deduplicates against active
// memories, resolves entities, and decides promotion. Extraction is
// best-effort by contract: a failure must never fail or delay the
// user-facing turn.
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/memory/store"
)

// Gateway is the slice of the gateway client extraction needs.
type Gateway interface {
	Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error)
	Embed(ctx context.Context, texts []string, purpose string) ([][]float32, string, error)
}

// Storer is the slice of the memory store extraction needs. Extract
// only reads until ApplyExtraction writes the whole run at once.
type Storer interface {
	Get(ctx context.Context, id string) (store.Memory, error)
	HasPendingCorrection(ctx context.Context, id string) (bool, error)
	NearestActiveOnly(ctx context.Context, embedding store.Vector) (id string, similarity float64, ok bool, err error)
	NearestActive(ctx context.Context, embedding store.Vector) (id string, similarity float64, status store.Status, ok bool, err error)
	RejectedWithContent(ctx context.Context, content string) (id string, ok bool, err error)
	ApplyExtraction(ctx context.Context, confirm []store.Confirmation, proposals []store.Proposal) ([]string, error)
}

const (
	llmTimeout  = 30 * time.Second
	maxAttempts = 2
	maxFacts    = 20

	// sideRoute serves extraction and consolidation side-calls. The
	// "mini" route these calls used before was never seeded by any
	// migration, so every one failed with no_route (same bug brain's
	// classifyRoute already fixed). "summarize" is a real fixed route
	// and these calls need its model quality: small local models fail
	// the strict JSON contract more often than they meet it.
	sideRoute = "summarize"

	// NearDupSimilarity marks a candidate as a possible correction when
	// its content differs from the closest known memory.
	NearDupSimilarity = 0.95

	// autoPromoteConfidence is the floor for episodic observations to
	// skip the confirmation queue.
	autoPromoteConfidence = 0.8
)

// system demands strict JSON. Content must be self-contained: absolute
// dates, no pronouns that need surrounding context.
const system = `You extract durable facts from a conversation excerpt for an AI assistant's long-term memory. Reply with ONLY a JSON array - no prose, no markdown fences:
[{"type":"episodic|semantic|procedural","content":"one atomic self-contained fact","entities":[{"type":"person|project|service|preference|decision|topic|place","name":"..."}],"confidence":0.0,"changes_behavior":true}]
Rules: each content is ONE fact, self-contained (absolute dates, full names, no "he"/"it"/"this project"). type: episodic = something that happened, semantic = a durable fact or preference, procedural = a how-to. Anything phrased as a rule, requirement, standing instruction, or directive is semantic, NEVER episodic - even when it was stated during an event. confidence in [0,1] reflects how certain the excerpt makes the fact. changes_behavior is REQUIRED on every fact: true only when knowing this fact would change how the assistant acts or answers in a FUTURE conversation - facts about the user, their preferences, their projects, their world. General knowledge the excerpt happened to discuss is false - for example, "The capital of the Netherlands is Amsterdam" or "HTTP 429 means rate limiting" are general knowledge, not facts about the user. Skip small talk, transient state, and anything already obvious. Also skip filesystem paths, directory/file permissions and ownership, container or sandbox environment details, UUIDs and other identifiers, and any other machine-state observations - these are transient environment facts, not durable knowledge about the user. Empty array when nothing qualifies.`

// missionSystem is the mission-digest extraction contract. The input
// is a mission's OutcomeDigest - goal, title, kind, unit statuses -
// which is a RECORD, not knowledge: everything in its header lines
// already lives in the missions table. Only deltas qualify.
const missionSystem = `You extract durable facts from a completed background mission's outcome digest for an AI assistant's long-term memory. Reply with ONLY a JSON array - no prose, no markdown fences:
[{"type":"episodic|semantic|procedural","content":"one atomic self-contained fact","entities":[{"type":"person|project|service|preference|decision|topic|place","name":"..."}],"confidence":0.0,"changes_behavior":true}]
Set changes_behavior true only when knowing the fact would change how the assistant acts in a future conversation. ONLY these qualify: (a) a user preference or standing instruction the mission revealed, (b) a durable fact about the outside world DISCOVERED during execution (an API's behavior, a service's quirk, a deadline that exists), (c) a lesson from a failure worth avoiding next time. NEVER extract the mission's goal, title, kind, unit statuses, artifact names, or terminal state - those are bookkeeping the system already stores, not knowledge. NEVER extract execution-environment observations - installed packages, library or tool availability or versions, filesystem paths, file or directory permissions and ownership, container or sandbox details, absence of files in a repository, or any other machine-state observation; these describe the sandbox, not the world. A summary of what a scan, search, or inbox check found during one run is a transient result, not a durable fact - do not extract it; only a durable fact it revealed (for example a deadline that exists) qualifies, stated on its own with absolute dates. Each content must be self-contained (absolute dates, full names, no pronouns). Anything phrased as a rule or standing instruction is semantic, never episodic. changes_behavior is REQUIRED on every fact. Most digests contain NOTHING worth remembering: an empty array is the expected common answer.`

// reflectionSystem is the consolidation reflection contract: distill
// recurring patterns across recent episodic memories into rare,
// cross-cutting semantic insights - the slow-learning half of the
// episodic/semantic split (memory-extraction-v2 plan, slice 4). The
// episodics themselves stay; this only proposes what they add up to.
const reflectionSystem = `You are reviewing an AI assistant's recent episodic memories (things that happened) to distill durable insights for long-term memory. Reply with ONLY a JSON array - no prose, no markdown fences:
[{"type":"semantic","content":"one atomic self-contained insight","entities":[{"type":"person|project|service|preference|decision|topic|place","name":"..."}],"confidence":0.0,"changes_behavior":true}]
An insight qualifies ONLY when a RECURRING pattern across several distinct episodes reveals something durable no single episode states: a habit, a recurring problem, a stable preference, a relationship between things the user deals with repeatedly. Propose at most 3 insights per review, each grounded in at least 2 separate episodes. Never restate a single episode, never summarize the list, never propose general knowledge. changes_behavior is REQUIRED on every insight. An empty array is the expected common answer.`

// Request is one extraction job: text from a completed turn or from
// turns about to be compacted away.
type Request struct {
	SessionID string `json:"session_id"`
	SourceSeq int64  `json:"source_seq"`
	Text      string `json:"text"`
	// Route overrides sideRoute for this job's LLM call - set by the
	// caller when the source turn/session executed a sensitive tool, so
	// extraction honors the same route floor the tool loop already
	// pinned the turn to instead of falling back to sideRoute's cloud
	// model.
	Route string `json:"route,omitempty"`
	// Source names what produced Text: "chat" (default), "mission"
	// (a terminal mission's OutcomeDigest), or "compaction". Mission
	// digests get their own extraction contract - the generic prompt
	// dutifully extracted the digest's own goal/title/kind header
	// lines as "facts", flooding the confirmation queue with
	// restatements of things the missions table already records.
	Source string `json:"source,omitempty"`
	// Deny lists values a fact must not restate: system-owned
	// knowledge (operator settings like timezone) that the model
	// otherwise re-extracts as if it were a discovered fact about the
	// user.
	Deny []string `json:"deny,omitempty"`
	// Recalled lists the memory contents injected into the source turn.
	// A fact restating one is the assistant echoing it back, so it is
	// neither stored nor counted as a confirmation; a correction of one
	// still goes through.
	Recalled []string `json:"recalled,omitempty"`
	// Actor is the provenance stamped on inserted rows; empty means
	// "agent". In-process callers only (D-143): never decoded from JSON.
	Actor string `json:"-"`
}

// Extractor runs the pipeline.
type Extractor struct {
	gw    Gateway
	store Storer
	log   *slog.Logger
	drops *prometheus.CounterVec // gate label; nil disables
}

func New(gw Gateway, st Storer, log *slog.Logger) *Extractor {
	return &Extractor{gw: gw, store: st, log: log}
}

// SetGateDrops counts every proposed fact a gate or dedup check drops,
// by gate.
func (e *Extractor) SetGateDrops(c *prometheus.CounterVec) {
	e.drops = c
}

// Gate labels for memory_extract_gate_drops_total.
const (
	gateUtility           = "utility"
	gateDenyEcho          = "deny_echo"
	gateRecalledEcho      = "recalled_echo"
	gateBoundedWindow     = "bounded_window"
	gateBatchDuplicate    = "batch_duplicate"
	gateActiveDuplicate   = "active_duplicate"
	gateOpenCorrection    = "open_correction"
	gateRejectedDuplicate = "rejected_duplicate"
	gatePendingDuplicate  = "pending_duplicate"
)

func (e *Extractor) drop(gate string) {
	if e.drops != nil {
		e.drops.WithLabelValues(gate).Inc()
	}
}

// Extract proposes, validates, dedupes, and inserts facts; it returns
// the inserted memory ids (pre-compaction callers record them in
// compaction_applied.facts_extracted). An extraction that yields no
// facts is a success with an empty result.
func (e *Extractor) Extract(ctx context.Context, req Request) ([]string, error) {
	facts, err := e.propose(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(facts) == 0 {
		return nil, nil
	}

	texts := make([]string, len(facts))
	for i, f := range facts {
		texts[i] = f.Content
	}
	// No embedding route is a degraded mode, not a failure: facts
	// store without vectors (text and entity legs still retrieve
	// them) and near-dup detection skips. Mirrors retrieval's
	// partial-recall-beats-none stance.
	vecs, _, err := e.gw.Embed(ctx, texts, "memory-extract")
	if err != nil {
		e.log.Warn("embedding failed; storing facts without vectors", "error", err, "session_id", req.SessionID)
		vecs = make([][]float32, len(facts))
	}

	deny := denyText(req)
	var proposals []store.Proposal
	var confirms []store.Confirmation
	correcting := map[string]bool{} // active ids this run already proposes to supersede
	var batch []batchMemory         // accepted so far, this run only
	for i, f := range facts {
		if f.ChangesBehavior != nil && !*f.ChangesBehavior {
			// The model itself judged this fact wouldn't change future
			// behavior (general knowledge the conversation happened to
			// touch) - the utility gate drops it before it can queue.
			e.log.Info("memory dropped by utility gate", "session_id", req.SessionID)
			e.drop(gateUtility)
			continue
		}
		if echoesDeny(f.Content, deny) || mentionsSetting(f.Content, req.Deny) {
			// The model restated the digest's own goal/title header -
			// bookkeeping the missions table already records, never a
			// memory. Code enforces what the prompt asks for (D-011).
			e.log.Info("memory dropped as source-header echo", "session_id", req.SessionID)
			e.drop(gateDenyEcho)
			continue
		}
		if echoesRecalled(f.Content, req.Recalled) {
			e.log.Info("memory dropped as echo of a recalled memory", "session_id", req.SessionID)
			e.drop(gateRecalledEcho)
			continue
		}
		if f.Type != string(store.TypeEpisodic) && boundedWindow(f.Content) {
			// A semantic or procedural fact phrased as a bounded
			// observation window ("last 24 hours", "currently") describes
			// one run's result, not a durable fact. Episodic facts keep -
			// something that happened IS time-scoped. Code enforces what
			// the prompt asks for (D-011).
			e.log.Info("memory dropped as bounded-window observation", "session_id", req.SessionID)
			e.drop(gateBoundedWindow)
			continue
		}
		emb := store.Vector(vecs[i])
		if len(emb) > 0 {
			// Intra-batch dedup: one extraction run can propose the same
			// fact twice (e.g. stated then restated in the same turn).
			// Compare against facts already accepted earlier in this
			// same run, before either the DB or NearestActive sees them.
			if nearDupVector(emb, f.Content, batch) {
				e.log.Info("memory dropped as intra-batch duplicate", "session_id", req.SessionID)
				e.drop(gateBatchDuplicate)
				continue
			}
		}

		supersedes := ""
		if len(emb) == 0 {
			// No vector to compare by: a normalized text match still
			// keeps a rejected fact out of the queue.
			rejectedID, found, err := e.store.RejectedWithContent(ctx, f.Content)
			if err != nil {
				return nil, fmt.Errorf("extract: rejected text dedup: %w", err)
			}
			if found {
				e.log.Info("memory dropped as text match of rejected fact",
					"of", rejectedID, "session_id", req.SessionID)
				e.drop(gateRejectedDuplicate)
				continue
			}
		} else {
			activeID, sim, found, err := e.store.NearestActiveOnly(ctx, emb)
			if err != nil {
				return nil, fmt.Errorf("extract: active dedup: %w", err)
			}
			if found && sim >= NearDupSimilarity {
				active, err := e.store.Get(ctx, activeID)
				if err != nil {
					return nil, fmt.Errorf("extract: load duplicate: %w", err)
				}
				if !IsCorrection(f.Content, active.Content) {
					// A restatement reinforces the existing row instead of
					// inserting: repetition is a confidence signal, not new
					// knowledge.
					confirms = append(confirms, store.Confirmation{ID: activeID, Confidence: f.Confidence})
					e.log.Info("memory duplicate reinforced existing row",
						"of", activeID, "similarity", sim, "session_id", req.SessionID)
					e.drop(gateActiveDuplicate)
					continue
				}
				// One open correction per fact: later turns repeating the
				// change must not stack more cards on the queue.
				open := correcting[activeID]
				if !open {
					open, err = e.store.HasPendingCorrection(ctx, activeID)
					if err != nil {
						return nil, fmt.Errorf("extract: pending correction: %w", err)
					}
				}
				if open {
					e.log.Info("memory correction already pending; skipped",
						"of", activeID, "session_id", req.SessionID)
					e.drop(gateOpenCorrection)
					continue
				}
				supersedes = activeID
				correcting[activeID] = true
			} else {
				dupID, sim, status, found, err := e.store.NearestActive(ctx, emb)
				if err != nil {
					return nil, fmt.Errorf("extract: dedup: %w", err)
				}
				if found && sim >= NearDupSimilarity && status != store.StatusActive {
					dup, err := e.store.Get(ctx, dupID)
					if err != nil {
						return nil, fmt.Errorf("extract: load duplicate: %w", err)
					}
					// A different fact that only embeds close to an
					// unconfirmed or rejected one still reaches review.
					if !IsCorrection(f.Content, dup.Content) {
						if status == store.StatusRejected {
							// Rejection is a durable teaching signal: a
							// reworded re-proposal is dropped, never re-queued.
							e.log.Info("memory dropped as near-duplicate of rejected fact",
								"of", dupID, "similarity", sim, "session_id", req.SessionID)
							e.drop(gateRejectedDuplicate)
						} else {
							e.log.Info("memory duplicate matched pending row; skipped",
								"of", dupID, "similarity", sim, "session_id", req.SessionID)
							e.drop(gatePendingDuplicate)
						}
						continue
					}
				}
			}
		}

		entities := make([]store.EntityKey, 0, len(f.Entities))
		for _, ent := range f.Entities {
			entities = append(entities, store.EntityKey{Type: ent.Type, Name: ent.Name})
		}
		proposals = append(proposals, store.Proposal{
			Memory: store.Memory{
				Type: store.MemoryType(f.Type), Content: f.Content, Embedding: emb,
				SourceSession: req.SessionID, SourceSeq: req.SourceSeq,
				Actor: req.Actor, Confidence: f.Confidence, Supersedes: supersedes,
			},
			Entities: entities,
			Promote:  AutoPromote(f) && supersedes == "",
		})
		if len(emb) > 0 {
			batch = append(batch, batchMemory{content: f.Content, embedding: emb})
		}
	}
	if len(proposals) == 0 && len(confirms) == 0 {
		return nil, nil
	}
	// One transaction for the whole run (D-144): a failure leaves
	// nothing behind, not a half-applied batch or orphan entities.
	ids, err := e.store.ApplyExtraction(ctx, confirms, proposals)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	return ids, nil
}

// propose runs the LLM call; invalid output retries once, then the
// whole extraction drops (logged by the caller).
func (e *Extractor) propose(ctx context.Context, req Request) ([]Fact, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		raw, err := e.proposeOnce(ctx, req)
		if err == nil {
			facts, perr := ParseFacts(raw)
			if perr == nil {
				return facts, nil
			}
			err = perr
		}
		lastErr = err
	}
	return nil, fmt.Errorf("extract: %w", lastErr)
}

func (e *Extractor) proposeOnce(ctx context.Context, req Request) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, llmTimeout)
	defer cancel()

	route := sideRoute
	if req.Route != "" {
		route = req.Route
	}
	sys := system
	switch req.Source {
	case "mission":
		sys = missionSystem
	case "reflection":
		sys = reflectionSystem
	}
	events, err := e.gw.Stream(ctx, gwclient.StreamRequest{
		Route:    route,
		Purpose:  "memory-extract",
		System:   sys,
		Messages: []provider.Message{{Role: "user", Content: req.Text}},
		// Reasoning models spend thinking tokens from the same budget
		// before emitting content; 1000 starved the JSON reply entirely
		// (stream ended incomplete with zero content chunks).
		MaxTokens: 4000,
		SessionID: req.SessionID,
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for ev := range events {
		switch ev.Type {
		case stream.EventChunk:
			b.WriteString(ev.Text)
		case stream.EventError:
			return "", fmt.Errorf("extract llm: %s", ev.Err.Message)
		}
	}
	return b.String(), nil
}

// Fact is one candidate memory as proposed by the model.
type Fact struct {
	Type       string       `json:"type"`
	Content    string       `json:"content"`
	Entities   []FactEntity `json:"entities"`
	Confidence float32      `json:"confidence"`
	// ChangesBehavior is the utility gate: the model's own answer to
	// "would knowing this change a future turn?". Required on every
	// fact - ParseFacts rejects a fact where it's nil (missing from the
	// reply), triggering propose()'s retry instead of silently keeping
	// an unjudged fact.
	ChangesBehavior *bool `json:"changes_behavior,omitempty"`
}

type FactEntity struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

var validFactTypes = map[string]bool{
	"episodic": true, "semantic": true, "procedural": true,
}

var validEntityTypes = map[string]bool{
	"person": true, "project": true, "service": true, "preference": true,
	"decision": true, "topic": true, "place": true,
}

// ParseFacts decodes the model's reply strictly: fences stripped,
// unknown fields rejected, every enum and range checked. One bad fact
// rejects the whole batch - a model that hallucinates structure once
// gets its retry, not partial trust.
func ParseFacts(raw string) ([]Fact, error) {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var facts []Fact
	if err := dec.Decode(&facts); err != nil {
		return nil, fmt.Errorf("invalid facts JSON: %w", err)
	}
	if len(facts) > maxFacts {
		facts = facts[:maxFacts]
	}
	for i, f := range facts {
		if !validFactTypes[f.Type] {
			return nil, fmt.Errorf("fact %d: invalid type %q", i, f.Type)
		}
		if strings.TrimSpace(f.Content) == "" {
			return nil, fmt.Errorf("fact %d: empty content", i)
		}
		if f.Confidence < 0 || f.Confidence > 1 {
			return nil, fmt.Errorf("fact %d: confidence %v out of range", i, f.Confidence)
		}
		if f.ChangesBehavior == nil {
			return nil, fmt.Errorf("fact %d: missing changes_behavior", i)
		}
		for j, ent := range f.Entities {
			if !validEntityTypes[ent.Type] {
				return nil, fmt.Errorf("fact %d entity %d: invalid type %q", i, j, ent.Type)
			}
			if strings.TrimSpace(ent.Name) == "" {
				return nil, fmt.Errorf("fact %d entity %d: empty name", i, j)
			}
		}
	}
	return facts, nil
}

// boundedWindowPattern catches bounded-time-window phrasing: a
// semantic or procedural fact phrased this way describes what was
// true during one observation window, not a durable fact about the
// world. "as of" is deliberately excluded - the extraction contract
// demands absolute dates and legitimate facts use exactly that
// phrasing (e.g. "as of 2026-08-24 the user works at Cielara").
var boundedWindowPattern = regexp.MustCompile(`(?i)\b(` +
	`(?:last|past)\s+(?:\d+|one|two|three|four|five|six|seven|eight|nine|ten|twelve|twenty[- ]four)\s+(?:hours?|days?|weeks?|months?)|` +
	`today|yesterday|tonight|` +
	`this\s+(?:morning|afternoon|evening|week|month)|` +
	`currently|right now|at the moment|so far` +
	`)\b`)

// boundedWindow reports whether content describes a bounded
// observation window rather than a durable fact.
func boundedWindow(content string) bool {
	return boundedWindowPattern.MatchString(content)
}

// sensitive marks content that must never skip the confirmation queue
// regardless of type or confidence: credentials-adjacent topics and
// standing-instruction phrasing. The list is deliberately broad in
// the directive direction - a false positive only queues an innocent
// fact for confirmation, a false negative activates an instruction
// without review. Keyword matching can never be complete; the fence
// (D-011 trust="data") is the containment for what slips through.
var sensitive = regexp.MustCompile(`(?i)` + credentialPattern + `|` +
	`always |never |prefer|instruct|direct(ed|s|ive)|require|rule|policy|` +
	`must |shall |should |do not |don't |ensure |make sure |` +
	`from now on|going forward|all future`)

// credentialPattern is the credentials-adjacent half of sensitive. A
// clean, user-entered memory add is reviewed only on this half: the
// user's own standing instruction is the point of "remember" (D-011).
const credentialPattern = `password|passphrase|token|secret|credential|api.?key|private.?key|ssh|vault`

var credential = regexp.MustCompile(`(?i)` + credentialPattern)

// AutoPromote is the promotion policy - code, not LLM (D-011).
// Episodic observations with high confidence activate directly;
// semantic and procedural facts (preferences, identity, standing
// instructions live here) always wait for the user, as does anything
// credentials-adjacent.
func AutoPromote(f Fact) bool {
	if f.Type != string(store.TypeEpisodic) {
		return false
	}
	if f.Confidence < autoPromoteConfidence {
		return false
	}
	return !sensitive.MatchString(f.Content)
}

// MentionsCredential reports whether content is credentials-adjacent,
// which keeps even a clean user-entered memory in the review queue.
func MentionsCredential(content string) bool {
	return credential.MatchString(content)
}

// denyText collects the source-record lines a proposed fact must not
// restate. Mission digests contribute OutcomeDigest's "mission goal:"
// and "mission title:" headers - bookkeeping, not knowledge, that
// models reliably extract as "facts" without this fence. req.Deny
// (operator settings) is checked separately by mentionsSetting.
func denyText(req Request) []string {
	var deny []string
	if req.Source == "mission" {
		for _, line := range strings.Split(req.Text, "\n") {
			for _, prefix := range []string{"mission goal:", "mission title:"} {
				if rest, ok := strings.CutPrefix(line, prefix); ok {
					if v := strings.TrimSpace(rest); v != "" {
						deny = append(deny, strings.ToLower(v))
					}
				}
			}
		}
	}
	return deny
}

type batchMemory struct {
	content   string
	embedding store.Vector
}

// nearDupVector suppresses restatements within one model response, but
// keeps an explicit change in negation polarity for user review. One
// response does not swap its own facts, so a word swap here is a
// paraphrase, not a correction.
func nearDupVector(emb store.Vector, content string, accepted []batchMemory) bool {
	for _, other := range accepted {
		if cosineSimilarity(emb, other.embedding) >= NearDupSimilarity && !oppositeNegation(content, other.content) {
			return true
		}
	}
	return false
}

// IsCorrection reports whether next changes the fact prev states rather
// than restating it: a negation flip, or a meaningful word swapped for
// another (a city, number or name). Rewording that only adds or drops
// words, punctuation or stopwords is a restatement.
func IsCorrection(next, prev string) bool {
	if oppositeNegation(next, prev) {
		return true
	}
	a, b := meaningfulWords(next), meaningfulWords(prev)
	return hasWordNotIn(a, b) && hasWordNotIn(b, a)
}

func meaningfulWords(content string) map[string]bool {
	words := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(content)) {
		w = strings.TrimSuffix(strings.Trim(w, ".,;:'\"()!?"), "'s")
		if meaningfulDenyWord(w) {
			words[w] = true
		}
	}
	return words
}

func hasWordNotIn(a, b map[string]bool) bool {
	for w := range a {
		if !b[w] {
			return true
		}
	}
	return false
}

// echoesRecalled reports whether content restates a memory injected
// into the source turn without correcting it.
func echoesRecalled(content string, recalled []string) bool {
	for _, r := range recalled {
		if !IsCorrection(content, r) && echoesDeny(content, []string{r}) {
			return true
		}
	}
	return false
}

func oppositeNegation(a, b string) bool {
	return hasNegation(a) != hasNegation(b)
}

func hasNegation(content string) bool {
	for _, word := range strings.Fields(strings.ToLower(content)) {
		switch strings.Trim(word, ".,;:'\"()!?") {
		case "not", "no", "never", "isn't", "aren't", "wasn't", "weren't",
			"doesn't", "don't", "didn't", "can't", "cannot", "won't", "without":
			return true
		}
	}
	return false
}

func cosineSimilarity(a, b store.Vector) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// shortDenyWords is the deny-line length (content words) below which
// containment alone is not an echo (D-145).
const shortDenyWords = 3

// echoesDeny reports whether content substantially restates any deny
// line: most of the deny line's content words (>70%) reappear in the fact.
// Word-overlap rather than substring, because extraction paraphrases
// ("The user mandated a mission goal to create...") instead of quoting.
// A deny line under shortDenyWords words (a mission titled "Research")
// also needs the fact to be mostly that phrase: at most twice its
// word count (D-145).
func echoesDeny(content string, deny []string) bool {
	return overlapsDeny(content, deny, true)
}

// mentionsSetting reports whether content restates an operator setting
// value (the timezone): plain overlap, no short-line rule, since a
// setting value is one distinctive token the fact merely has to carry
// (D-145).
func mentionsSetting(content string, values []string) bool {
	return overlapsDeny(content, values, false)
}

func overlapsDeny(content string, deny []string, shortRule bool) bool {
	if len(deny) == 0 {
		return false
	}
	words := map[string]bool{}
	factWords := 0
	for _, w := range strings.Fields(strings.ToLower(content)) {
		word := strings.Trim(w, ".,;:'\"()")
		if meaningfulDenyWord(word) {
			words[word] = true
			factWords++
		}
	}
	for _, d := range deny {
		var fields []string
		for _, word := range strings.Fields(strings.ToLower(d)) {
			word = strings.Trim(word, ".,;:'\"()")
			if meaningfulDenyWord(word) {
				fields = append(fields, word)
			}
		}
		if len(fields) == 0 {
			continue
		}
		if shortRule && len(fields) < shortDenyWords && factWords > 2*len(fields) {
			continue
		}
		hits := 0
		for _, w := range fields {
			if words[w] {
				hits++
			}
		}
		if float64(hits)/float64(len(fields)) > 0.7 {
			return true
		}
	}
	return false
}

func meaningfulDenyWord(word string) bool {
	switch word {
	case "a", "an", "and", "are", "as", "at", "be", "been", "being",
		"but", "by", "did", "do", "does", "for", "from", "had", "has",
		"have", "he", "her", "hers", "him", "his", "i", "if", "in",
		"into", "is", "it", "its", "me", "my", "of", "on", "or", "our",
		"ours", "she", "that", "the", "their", "theirs", "them", "they",
		"this", "those", "to", "us", "was", "we", "were", "what", "when",
		"where", "which", "who", "with", "you", "your", "yours":
		return false
	default:
		return word != ""
	}
}
