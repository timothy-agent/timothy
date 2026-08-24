package extract

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/memory/store"
)

const (
	// episodicArchiveAfter: episodic memories neither created nor
	// retrieved within this window archive out of the active set.
	episodicArchiveAfter = 180 * 24 * time.Hour
	// semanticDecayAfter: semantic facts unconfirmed this long start
	// losing confidence and queue for user reconfirmation.
	semanticDecayAfter = 365 * 24 * time.Hour
	decayFactor        = 0.8
	decayBatch         = 10 // stalest per run; a queue, not a flood

	// Reasoning models spend thinking tokens from the same budget
	// before emitting content - 300 would starve the one-sentence
	// reply the same way extraction's old 1000 cap did.
	mergeMaxTokens = 2000

	// reflection pass bounds: enough recent episodes to plausibly hold
	// a pattern, a hard cap on how many are read, and how far back
	// "recent" reaches.
	reflectMinEpisodics = 10
	reflectMaxEpisodics = 200
	reflectWindow       = 7 * 24 * time.Hour

	// mergeGuard thresholds. A guard false positive only keeps a
	// near-dup group as-is (extraction already tolerates that state)
	// and retries next pass with a fresh LLM sample; a false negative
	// permanently loses a detail. So each signal is loose and they
	// reject on OR, the opposite of an AND-gated guard that only
	// blocks a rewrite outright.
	guardMinTokenRetention = 0.5
	guardMinLengthRatio    = 0.4
	guardMaxLengthRatio    = 4.0
)

const mergeSystem = `You merge near-duplicate memory entries into ONE canonical fact. Reply with ONLY the merged fact as a single self-contained sentence (absolute dates, full names). Keep every distinct detail; drop only repetition.`

// ConsolidateStore is the slice of the memory store consolidation
// needs.
type ConsolidateStore interface {
	NearDupPairs(ctx context.Context, threshold float64) ([][2]string, error)
	Get(ctx context.Context, id string) (store.Memory, error)
	ApplyMerge(ctx context.Context, m store.Memory, memberIDs []string) (string, error)
	ArchiveStaleEpisodic(ctx context.Context, olderThan time.Time) (int64, error)
	DecayStaleSemantic(ctx context.Context, olderThan time.Time, factor float64, limit int) ([]string, error)
	RecentEpisodic(ctx context.Context, since time.Time, limit int) ([]store.Memory, error)
}

// Metrics counts every consolidation action; any counter may be nil
// (tests).
type Metrics struct {
	Merges   prometheus.Counter
	Rejects  *prometheus.CounterVec // reason: token_loss|shrink|bloat|conflict|error
	Archived prometheus.Counter
	Decayed  prometheus.Counter
}

// Summary counts one Run pass. RunLoop discards it; the manual
// trigger endpoint returns it.
type Summary struct {
	Merged    int `json:"merged"`
	Rejected  int `json:"rejected"`
	Archived  int `json:"archived"`
	Decayed   int `json:"decayed"`
	Reflected int `json:"reflected"`
}

// Consolidator is the daily lifecycle job (D-011): merge near-dup
// groups, archive unused episodic memories, decay unconfirmed
// semantic facts.
type Consolidator struct {
	gw      Gateway
	store   ConsolidateStore
	log     Logger
	metrics Metrics

	// reflector runs the reflection pass through the Extractor's full
	// pipeline (dedup-reinforce, utility gate, promotion policy) with
	// the reflection contract - nil disables the pass entirely, the
	// same nil-gated optional-dependency contract the drivers use.
	reflector *Extractor
}

// SetReflector wires the extractor the reflection pass distills
// episodics through - a setter because cmd/memoryd/main.go builds the
// Extractor and Consolidator side by side.
func (c *Consolidator) SetReflector(e *Extractor) {
	c.reflector = e
}

// Logger is the slog surface consolidation uses.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

func NewConsolidator(gw Gateway, st ConsolidateStore, log Logger, m Metrics) *Consolidator {
	return &Consolidator{gw: gw, store: st, log: log, metrics: m}
}

// RunLoop runs a pass every interval until ctx ends. The first pass
// waits one interval - a restart never triggers an immediate sweep.
func (c *Consolidator) RunLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := c.Run(ctx); err != nil && ctx.Err() == nil {
				c.log.Warn("consolidation pass failed", "error", err)
			}
		}
	}
}

// Run executes one full pass. Each stage is independent: a failure in
// one logs and the others still run; the returned Summary counts
// whatever did complete even when err is non-nil.
func (c *Consolidator) Run(ctx context.Context) (Summary, error) {
	var summary Summary
	var errs []string

	merged, rejected, err := c.mergeNearDups(ctx)
	summary.Merged, summary.Rejected = merged, rejected
	if err != nil {
		errs = append(errs, err.Error())
	}

	archived, err := c.archiveStale(ctx)
	summary.Archived = archived
	if err != nil {
		errs = append(errs, err.Error())
	}

	decayed, err := c.decayStale(ctx)
	summary.Decayed = decayed
	if err != nil {
		errs = append(errs, err.Error())
	}

	reflected, err := c.reflect(ctx)
	summary.Reflected = reflected
	if err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return summary, fmt.Errorf("consolidate: %s", strings.Join(errs, "; "))
	}
	return summary, nil
}

// mergeNearDups groups pairwise near-duplicates (union-find over the
// similarity edges), asks the mini category for one canonical fact
// per group, and applies the merges that survive the guard.
func (c *Consolidator) mergeNearDups(ctx context.Context) (merged, rejected int, err error) {
	pairs, err := c.store.NearDupPairs(ctx, nearDupSimilarity)
	if err != nil {
		return 0, 0, err
	}
	for _, group := range groupPairs(pairs) {
		reject, err := c.mergeGroup(ctx, group)
		switch {
		case err != nil:
			reason := "error"
			if errors.Is(err, store.ErrNotFound) {
				reason = "conflict"
			}
			c.log.Warn("near-dup group merge failed; group kept as-is, retried next pass",
				"error", err, "size", len(group), "reason", reason)
			rejected++
			if c.metrics.Rejects != nil {
				c.metrics.Rejects.WithLabelValues(reason).Inc()
			}
		case reject == dissolvedGroup:
			// Fewer than 2 members were still active by the time we
			// read them - nothing to merge, nothing to count.
		case reject != "":
			rejected++
			if c.metrics.Rejects != nil {
				c.metrics.Rejects.WithLabelValues(reject).Inc()
			}
		default:
			merged++
			if c.metrics.Merges != nil {
				c.metrics.Merges.Inc()
			}
		}
	}
	return merged, rejected, nil
}

// dissolvedGroup is mergeGroup's internal reject sentinel for a group
// that no longer has 2+ active members by the time it's read - never
// surfaced as a Rejects metric label, just silently skipped.
const dissolvedGroup = "dissolved"

// mergeGroup asks the LLM for one canonical fact covering the group,
// guards the result, and applies the merge transactionally. An empty
// reject string with a nil error means the merge applied; a non-empty
// reject means the group was dissolved or the guard rejected the
// rewrite - either way the group is kept as-is and nothing is
// counted as an error. A dissolved group (fewer than 2 active
// members) returns (dissolvedGroup, nil) and counts nothing, same as
// today.
func (c *Consolidator) mergeGroup(ctx context.Context, ids []string) (reject string, err error) {
	members := make([]store.Memory, 0, len(ids))
	var lines []string
	for _, id := range ids {
		m, err := c.store.Get(ctx, id)
		if err != nil {
			return "", err
		}
		// The pair scan and this read race with other supersedes;
		// only still-active rows join the merge.
		if m.Status != store.StatusActive {
			continue
		}
		members = append(members, m)
		lines = append(lines, "- "+m.Content)
	}
	if len(members) < 2 {
		return dissolvedGroup, nil // group dissolved before we got here
	}

	merged, err := c.mergedContent(ctx, lines)
	if err != nil {
		return "", err
	}

	memberContents := make([]string, len(members))
	for i, m := range members {
		memberContents[i] = m.Content
	}
	if reason := mergeGuard(memberContents, merged); reason != "" {
		c.log.Warn("merge rejected by guard; group kept, retried next pass",
			"reason", reason, "size", len(members))
		return reason, nil
	}

	vecs, _, err := c.gw.Embed(ctx, []string{merged}, "memory-consolidate")
	if err != nil {
		return "", fmt.Errorf("embed merged fact: %w", err)
	}

	// The merged fact inherits the group's strongest provenance: it
	// replaces confirmed knowledge, so it activates directly.
	canonical := members[0]
	memberIDs := make([]string, len(members))
	for i, m := range members {
		memberIDs[i] = m.ID
	}
	newID, err := c.store.ApplyMerge(ctx, store.Memory{
		Type: canonical.Type, Content: merged, Embedding: store.Vector(vecs[0]),
		EntityRefs: unionRefs(members), SourceSession: canonical.SourceSession,
		SourceSeq: canonical.SourceSeq, Confidence: maxConfidence(members),
	}, memberIDs)
	if err != nil {
		return "", err
	}
	c.log.Info("near-dup group merged", "members", len(members), "merged", newID)
	return "", nil
}

// sigTokens lowercases s and splits it on runs of non-letter,
// non-digit characters, keeping tokens that contain a digit (dates,
// versions, quantities - least paraphrasable) or are at least 4 runes
// long (a cheap stopword filter).
func sigTokens(s string) map[string]bool {
	out := map[string]bool{}
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		tok := b.String()
		b.Reset()
		hasDigit := strings.ContainsFunc(tok, unicode.IsDigit)
		if hasDigit || len([]rune(tok)) >= 4 {
			out[tok] = true
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		} else {
			flush()
		}
	}
	flush()
	return out
}

// mergeGuard returns "" when merged plausibly retains the members'
// information, else a reject reason: "token_loss", "shrink", or
// "bloat". Accepted limitation: near-dup members already share most
// tokens, so dropping ONE member's unique detail can still pass token
// retention - this is a residual LLM-judge-free risk, not something
// the guard can catch; the reject counters make persistent cases
// visible instead.
func mergeGuard(members []string, merged string) string {
	sig := map[string]bool{}
	longest := 0
	for _, m := range members {
		for t := range sigTokens(m) {
			sig[t] = true
		}
		if n := len([]rune(m)); n > longest {
			longest = n
		}
	}

	if len(sig) > 0 {
		mergedTokens := sigTokens(merged)
		retained := 0
		for t := range sig {
			if mergedTokens[t] {
				retained++
			}
		}
		if float64(retained)/float64(len(sig)) < guardMinTokenRetention {
			return "token_loss"
		}
	}

	mergedLen := len([]rune(merged))
	if longest > 0 {
		ratio := float64(mergedLen) / float64(longest)
		if ratio < guardMinLengthRatio {
			return "shrink"
		}
		if ratio > guardMaxLengthRatio {
			return "bloat"
		}
	}
	return ""
}

func (c *Consolidator) mergedContent(ctx context.Context, lines []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, llmTimeout)
	defer cancel()
	// Always sideRoute, never a sensitive-tool route override: this
	// merges lines from already-active memories across an unbounded,
	// cross-session batch, not one turn/session whose sensitivity is
	// known - there is no single caller-side sensitivity flag that
	// would even apply here.
	events, err := c.gw.Stream(ctx, gwclient.StreamRequest{
		Route: sideRoute,
		Purpose:      "memory-consolidate",
		System:       mergeSystem,
		Messages:     []provider.Message{{Role: "user", Content: strings.Join(lines, "\n")}},
		MaxTokens:    mergeMaxTokens,
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
			return "", fmt.Errorf("merge llm: %s", ev.Err.Message)
		}
	}
	merged := strings.TrimSpace(b.String())
	if merged == "" {
		return "", fmt.Errorf("merge llm returned nothing")
	}
	return merged, nil
}

// reflect distills recurring patterns across recent episodic memories
// into semantic insights (memory-extraction-v2 slice 4): the fast
// per-event layer stays episodic, and this slow pass is where
// cross-cutting semantic knowledge gets minted. The insights run
// through the Extractor's normal pipeline - dedup reinforces instead
// of re-inserting on repeat passes over the same episodes, the utility
// gate drops non-behavioral output, and semantic facts land pending
// so the user confirms them (AutoPromote never activates semantics).
func (c *Consolidator) reflect(ctx context.Context) (int, error) {
	if c.reflector == nil {
		return 0, nil
	}
	episodics, err := c.store.RecentEpisodic(ctx, time.Now().Add(-reflectWindow), reflectMaxEpisodics)
	if err != nil {
		return 0, fmt.Errorf("reflect: %w", err)
	}
	if len(episodics) < reflectMinEpisodics {
		return 0, nil
	}
	var b strings.Builder
	for _, m := range episodics {
		fmt.Fprintf(&b, "- (%s) %s\n", m.CreatedAt.Format("2006-01-02"), m.Content)
	}
	ids, err := c.reflector.Extract(ctx, Request{
		SessionID: "reflection", Text: b.String(), Source: "reflection",
	})
	if err != nil {
		return 0, fmt.Errorf("reflect: %w", err)
	}
	if len(ids) > 0 {
		c.log.Info("reflection minted semantic insights", "count", len(ids), "episodes", len(episodics))
	}
	return len(ids), nil
}

func (c *Consolidator) archiveStale(ctx context.Context) (int, error) {
	n, err := c.store.ArchiveStaleEpisodic(ctx, time.Now().Add(-episodicArchiveAfter))
	if err != nil {
		return 0, err
	}
	if n > 0 {
		c.log.Info("stale episodic memories archived", "count", n)
		if c.metrics.Archived != nil {
			c.metrics.Archived.Add(float64(n))
		}
	}
	return int(n), nil
}

func (c *Consolidator) decayStale(ctx context.Context) (int, error) {
	ids, err := c.store.DecayStaleSemantic(ctx, time.Now().Add(-semanticDecayAfter), decayFactor, decayBatch)
	if err != nil {
		return 0, err
	}
	if len(ids) > 0 {
		c.log.Info("stale semantic facts decayed; queued for reconfirmation", "count", len(ids))
		if c.metrics.Decayed != nil {
			c.metrics.Decayed.Add(float64(len(ids)))
		}
	}
	return len(ids), nil
}

// groupPairs unions similarity edges into merge groups (size >= 2),
// deterministic order.
func groupPairs(pairs [][2]string) [][]string {
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" || parent[x] == x {
			parent[x] = x
			return x
		}
		root := find(parent[x])
		parent[x] = root
		return root
	}
	var order []string
	for _, p := range pairs {
		for _, id := range p {
			if _, seen := parent[id]; !seen {
				order = append(order, id)
			}
		}
		a, b := find(p[0]), find(p[1])
		if a != b {
			parent[b] = a
		}
	}
	byRoot := map[string][]string{}
	for _, id := range order {
		root := find(id)
		byRoot[root] = append(byRoot[root], id)
	}
	var groups [][]string
	for _, id := range order {
		if find(id) == id && len(byRoot[id]) >= 2 {
			groups = append(groups, byRoot[id])
		}
	}
	return groups
}

func unionRefs(members []store.Memory) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range members {
		for _, r := range m.EntityRefs {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}

func maxConfidence(members []store.Memory) float32 {
	best := float32(0)
	for _, m := range members {
		if m.Confidence > best {
			best = m.Confidence
		}
	}
	return best
}
