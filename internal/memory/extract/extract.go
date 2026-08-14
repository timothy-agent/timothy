// Package extract turns raw conversation turns into staged long-term
// memories (D-011). One mini LLM call proposes atomic facts; code —
// never the model — validates them, deduplicates against active
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
	// outright. Near-dups still insert — the consolidation job merges
	// them by the same similarity measure.
	nearDupSimilarity  = 0.95
	exactDupSimilarity = 0.99

	// autoPromoteConfidence is the floor for episodic observations to
	// skip the confirmation queue.
	autoPromoteConfidence = 0.8
)

// system demands strict JSON. Content must be self-contained: absolute
// dates, no pronouns that need surrounding context.
const system = `You extract durable facts from a conversation excerpt for an AI assistant's long-term memory. Reply with ONLY a JSON array — no prose, no markdown fences:
[{"type":"episodic|semantic|procedural","content":"one atomic self-contained fact","entities":[{"type":"person|project|service|preference|decision|topic|place","name":"..."}],"confidence":0.0}]
Rules: each content is ONE fact, self-contained (absolute dates, full names, no "he"/"it"/"this project"). type: episodic = something that happened, semantic = a durable fact or preference, procedural = a how-to. Anything phrased as a rule, requirement, standing instruction, or directive is semantic, NEVER episodic — even when it was stated during an event. confidence in [0,1] reflects how certain the excerpt makes the fact. Skip small talk, transient state, and anything already obvious. Empty array when nothing qualifies.`

// Request is one extraction job: text from a completed turn or from
// turns about to be compacted away.
type Request struct {
	SessionID string `json:"session_id"`
	SourceSeq int64  `json:"source_seq"`
	Text      string `json:"text"`
	// Route overrides sideRoute for this job's LLM call — set by the
	// caller when the source turn/session executed a sensitive tool, so
	// extraction honors the same route floor the tool loop already
	// pinned the turn to instead of falling back to sideRoute's cloud
	// model.
	Route string `json:"route,omitempty"`
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

	var ids []string
	for i, f := range facts {
		emb := store.Vector(vecs[i])
		if len(emb) > 0 {
			dupID, sim, found, err := e.store.NearestActive(ctx, emb)
			if err != nil {
				return ids, fmt.Errorf("extract: dedup: %w", err)
			}
			if found && sim >= exactDupSimilarity {
				// Dropping the fact would otherwise lose the
				// confirmation signal entirely; best-effort, never
				// fails extraction.
				if err := e.store.Confirm(ctx, dupID); err != nil {
					e.log.Warn("confirm on exact duplicate failed; fact still dropped", "of", dupID, "error", err)
				}
				e.log.Info("memory dropped as exact duplicate",
					"of", dupID, "similarity", sim, "session_id", req.SessionID)
				continue
			}
			if found && sim >= nearDupSimilarity {
				// Near-dup: keep it, consolidation merges the group later.
				e.log.Info("memory near-duplicate kept for consolidation",
					"of", dupID, "similarity", sim, "session_id", req.SessionID)
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
	events, err := e.gw.Stream(ctx, gwclient.StreamRequest{
		Route: route,
		Purpose:      "memory-extract",
		System:       system,
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
// rejects the whole batch — a model that hallucinates structure once
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
// the directive direction — a false positive only queues an innocent
// fact for confirmation, a false negative activates an instruction
// without review. Keyword matching can never be complete; the fence
// (D-011 trust="data") is the containment for what slips through.
var sensitive = regexp.MustCompile(`(?i)` +
	`password|passphrase|token|secret|credential|api.?key|private.?key|ssh|vault|` +
	`always |never |prefer|instruct|direct(ed|s|ive)|require|rule|policy|` +
	`must |shall |should |do not |don't |ensure |make sure |` +
	`from now on|going forward|all future`)

// AutoPromote is the promotion policy — code, not LLM (D-011).
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
