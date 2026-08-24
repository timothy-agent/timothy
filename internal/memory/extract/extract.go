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
	"regexp"
	"strings"
	"time"

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

// Storer is the slice of the memory store extraction needs.
type Storer interface {
	Insert(ctx context.Context, m store.Memory) (string, error)
	Promote(ctx context.Context, id string) error
	Confirm(ctx context.Context, id string) error
	UpsertEntity(ctx context.Context, typ, name string) (string, error)
	NearestActive(ctx context.Context, embedding store.Vector) (id string, similarity float64, ok bool, err error)
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

	// nearDupSimilarity marks a candidate as restating known
	// knowledge; exactDupSimilarity (or byte-equal content) drops it
	// outright. Near-dups still insert - the consolidation job merges
	// them by the same similarity measure.
	nearDupSimilarity  = 0.95
	exactDupSimilarity = 0.99

	// autoPromoteConfidence is the floor for episodic observations to
	// skip the confirmation queue.
	autoPromoteConfidence = 0.8
)

// system demands strict JSON. Content must be self-contained: absolute
// dates, no pronouns that need surrounding context.
const system = `You extract durable facts from a conversation excerpt for an AI assistant's long-term memory. Reply with ONLY a JSON array - no prose, no markdown fences:
[{"type":"episodic|semantic|procedural","content":"one atomic self-contained fact","entities":[{"type":"person|project|service|preference|decision|topic|place","name":"..."}],"confidence":0.0,"changes_behavior":true}]
Rules: each content is ONE fact, self-contained (absolute dates, full names, no "he"/"it"/"this project"). type: episodic = something that happened, semantic = a durable fact or preference, procedural = a how-to. Anything phrased as a rule, requirement, standing instruction, or directive is semantic, NEVER episodic - even when it was stated during an event. confidence in [0,1] reflects how certain the excerpt makes the fact. changes_behavior: true only when knowing this fact would change how the assistant acts or answers in a FUTURE conversation - facts about the user, their preferences, their projects, their world. General knowledge the excerpt happened to discuss (documentation facts, quiz answers, how a technology works) is false. Skip small talk, transient state, and anything already obvious. Empty array when nothing qualifies.`

// missionSystem is the mission-digest extraction contract. The input
// is a mission's OutcomeDigest - goal, title, kind, unit statuses -
// which is a RECORD, not knowledge: everything in its header lines
// already lives in the missions table. Only deltas qualify.
const missionSystem = `You extract durable facts from a completed background mission's outcome digest for an AI assistant's long-term memory. Reply with ONLY a JSON array - no prose, no markdown fences:
[{"type":"episodic|semantic|procedural","content":"one atomic self-contained fact","entities":[{"type":"person|project|service|preference|decision|topic|place","name":"..."}],"confidence":0.0,"changes_behavior":true}]
Set changes_behavior true only when knowing the fact would change how the assistant acts in a future conversation. ONLY these qualify: (a) a user preference or standing instruction the mission revealed, (b) a durable fact about the outside world DISCOVERED during execution (an API's behavior, a service's quirk, a deadline that exists), (c) a lesson from a failure worth avoiding next time. NEVER extract the mission's goal, title, kind, unit statuses, artifact names, or terminal state - those are bookkeeping the system already stores, not knowledge. Each content must be self-contained (absolute dates, full names, no pronouns). Anything phrased as a rule or standing instruction is semantic, never episodic. Most digests contain NOTHING worth remembering: an empty array is the expected common answer.`

// reflectionSystem is the consolidation reflection contract: distill
// recurring patterns across recent episodic memories into rare,
// cross-cutting semantic insights - the slow-learning half of the
// episodic/semantic split (memory-extraction-v2 plan, slice 4). The
// episodics themselves stay; this only proposes what they add up to.
const reflectionSystem = `You are reviewing an AI assistant's recent episodic memories (things that happened) to distill durable insights for long-term memory. Reply with ONLY a JSON array - no prose, no markdown fences:
[{"type":"semantic","content":"one atomic self-contained insight","entities":[{"type":"person|project|service|preference|decision|topic|place","name":"..."}],"confidence":0.0,"changes_behavior":true}]
An insight qualifies ONLY when a RECURRING pattern across several distinct episodes reveals something durable no single episode states: a habit, a recurring problem, a stable preference, a relationship between things the user deals with repeatedly. Propose at most 3 insights per review, each grounded in at least 2 separate episodes. Never restate a single episode, never summarize the list, never propose general knowledge. An empty array is the expected common answer.`

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
}

// Extractor runs the pipeline.
type Extractor struct {
	gw    Gateway
	store Storer
	log   *slog.Logger
}

func New(gw Gateway, st Storer, log *slog.Logger) *Extractor {
	return &Extractor{gw: gw, store: st, log: log}
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
	var ids []string
	for i, f := range facts {
		if f.ChangesBehavior != nil && !*f.ChangesBehavior {
			// The model itself judged this fact wouldn't change future
			// behavior (general knowledge the conversation happened to
			// touch) - the utility gate drops it before it can queue.
			e.log.Info("memory dropped by utility gate", "session_id", req.SessionID)
			continue
		}
		if echoesDeny(f.Content, deny) {
			// The model restated the digest's own goal/title header -
			// bookkeeping the missions table already records, never a
			// memory. Code enforces what the prompt asks for (D-011).
			e.log.Info("memory dropped as source-header echo", "session_id", req.SessionID)
			continue
		}
		emb := store.Vector(vecs[i])
		if len(emb) > 0 {
			dupID, sim, found, err := e.store.NearestActive(ctx, emb)
			if err != nil {
				return ids, fmt.Errorf("extract: dedup: %w", err)
			}
			if found && sim >= nearDupSimilarity {
				// Exact and near duplicates both reinforce the existing
				// row instead of inserting: repetition is a confidence
				// signal, not new knowledge. Inserting near-dups "for
				// consolidation to merge later" flooded the confirmation
				// queue with the same fact many times over.
				if err := e.store.Confirm(ctx, dupID); err != nil {
					e.log.Warn("confirm on duplicate failed; fact still dropped", "of", dupID, "error", err)
				}
				e.log.Info("memory duplicate reinforced existing row",
					"of", dupID, "similarity", sim, "session_id", req.SessionID)
				continue
			}
		}

		refs := make([]string, 0, len(f.Entities))
		for _, ent := range f.Entities {
			id, err := e.store.UpsertEntity(ctx, ent.Type, ent.Name)
			if err != nil {
				return ids, fmt.Errorf("extract: entity: %w", err)
			}
			refs = append(refs, id)
		}

		id, err := e.store.Insert(ctx, store.Memory{
			Type: store.MemoryType(f.Type), Content: f.Content, Embedding: emb,
			EntityRefs: refs, SourceSession: req.SessionID, SourceSeq: req.SourceSeq,
			Confidence: f.Confidence,
		})
		if err != nil {
			return ids, fmt.Errorf("extract: insert: %w", err)
		}
		ids = append(ids, id)

		if AutoPromote(f) {
			if err := e.store.Promote(ctx, id); err != nil {
				e.log.Warn("auto-promote failed; memory stays pending", "id", id, "error", err)
			}
		}
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
		Route: route,
		Purpose:      "memory-extract",
		System:       sys,
		Messages:     []provider.Message{{Role: "user", Content: req.Text}},
		// Reasoning models spend thinking tokens from the same budget
		// before emitting content; 1000 starved the JSON reply entirely
		// (stream ended incomplete with zero content chunks).
		MaxTokens:    4000,
		SessionID:    req.SessionID,
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
	// "would knowing this change a future turn?". Pointer so an older
	// model reply that omits the field keeps the fact (nil = keep);
	// only an explicit false drops it.
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

// sensitive marks content that must never skip the confirmation queue
// regardless of type or confidence: credentials-adjacent topics and
// standing-instruction phrasing. The list is deliberately broad in
// the directive direction - a false positive only queues an innocent
// fact for confirmation, a false negative activates an instruction
// without review. Keyword matching can never be complete; the fence
// (D-011 trust="data") is the containment for what slips through.
var sensitive = regexp.MustCompile(`(?i)` +
	`password|passphrase|token|secret|credential|api.?key|private.?key|ssh|vault|` +
	`always |never |prefer|instruct|direct(ed|s|ive)|require|rule|policy|` +
	`must |shall |should |do not |don't |ensure |make sure |` +
	`from now on|going forward|all future`)

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

// denyText collects the source-record lines a proposed fact must not
// restate. Only mission digests carry them: OutcomeDigest's "mission
// goal:" and "mission title:" headers are bookkeeping, not knowledge,
// and models reliably extract them as "facts" without this fence.
func denyText(req Request) []string {
	if req.Source != "mission" {
		return nil
	}
	var deny []string
	for _, line := range strings.Split(req.Text, "\n") {
		for _, prefix := range []string{"mission goal:", "mission title:"} {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				if v := strings.TrimSpace(rest); v != "" {
					deny = append(deny, strings.ToLower(v))
				}
			}
		}
	}
	return deny
}

// echoesDeny reports whether content substantially restates any deny
// line: most of the deny line's words (>70%) reappear in the fact.
// Word-overlap rather than substring, because extraction paraphrases
// ("The user mandated a mission goal to create...") instead of quoting.
func echoesDeny(content string, deny []string) bool {
	if len(deny) == 0 {
		return false
	}
	words := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(content)) {
		words[strings.Trim(w, ".,;:'\"()")] = true
	}
	for _, d := range deny {
		fields := strings.Fields(d)
		if len(fields) == 0 {
			continue
		}
		hits := 0
		for _, w := range fields {
			if words[strings.Trim(w, ".,;:'\"()")] {
				hits++
			}
		}
		if float64(hits)/float64(len(fields)) > 0.7 {
			return true
		}
	}
	return false
}
