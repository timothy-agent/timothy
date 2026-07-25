package extract

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	// before emitting content — 300 would starve the one-sentence
	// reply the same way extraction's old 1000 cap did.
	mergeMaxTokens = 2000
)

const mergeSystem = `You merge near-duplicate memory entries into ONE canonical fact. Reply with ONLY the merged fact as a single self-contained sentence (absolute dates, full names). Keep every distinct detail; drop only repetition.`

// ConsolidateStore is the slice of the memory store consolidation
// needs.
type ConsolidateStore interface {
	NearDupPairs(ctx context.Context, threshold float64) ([][2]string, error)
	Get(ctx context.Context, id string) (store.Memory, error)
	Insert(ctx context.Context, m store.Memory) (string, error)
	Promote(ctx context.Context, id string) error
	Supersede(ctx context.Context, oldID, newID string) error
	ArchiveStaleEpisodic(ctx context.Context, olderThan time.Time) (int64, error)
	DecayStaleSemantic(ctx context.Context, olderThan time.Time, factor float64, limit int) ([]string, error)
}

// Metrics counts every consolidation action; any counter may be nil
// (tests).
type Metrics struct {
	Merges   prometheus.Counter
	Archived prometheus.Counter
	Decayed  prometheus.Counter
}

// Consolidator is the daily lifecycle job (D-011): merge near-dup
// groups, archive unused episodic memories, decay unconfirmed
// semantic facts.
type Consolidator struct {
	gw      Gateway
	store   ConsolidateStore
	log     Logger
	metrics Metrics
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
// waits one interval — a restart never triggers an immediate sweep.
func (c *Consolidator) RunLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Run(ctx); err != nil && ctx.Err() == nil {
				c.log.Warn("consolidation pass failed", "error", err)
			}
		}
	}
}

// Run executes one full pass. Each stage is independent: a failure in
// one logs and the others still run.
func (c *Consolidator) Run(ctx context.Context) error {
	var errs []string
	if err := c.mergeNearDups(ctx); err != nil {
		errs = append(errs, err.Error())
	}
	if err := c.archiveStale(ctx); err != nil {
		errs = append(errs, err.Error())
	}
	if err := c.decayStale(ctx); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("consolidate: %s", strings.Join(errs, "; "))
	}
	return nil
}

// mergeNearDups groups pairwise near-duplicates (union-find over the
// similarity edges), asks the mini category for one canonical fact
// per group, and supersedes the members with it.
func (c *Consolidator) mergeNearDups(ctx context.Context) error {
	pairs, err := c.store.NearDupPairs(ctx, nearDupSimilarity)
	if err != nil {
		return err
	}
	for _, group := range groupPairs(pairs) {
		if err := c.mergeGroup(ctx, group); err != nil {
			c.log.Warn("near-dup group merge failed; group kept as-is", "error", err, "size", len(group))
			continue
		}
		if c.metrics.Merges != nil {
			c.metrics.Merges.Inc()
		}
	}
	return nil
}

func (c *Consolidator) mergeGroup(ctx context.Context, ids []string) error {
	members := make([]store.Memory, 0, len(ids))
	var lines []string
	for _, id := range ids {
		m, err := c.store.Get(ctx, id)
		if err != nil {
			return err
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
		return nil // group dissolved before we got here
	}

	merged, err := c.mergedContent(ctx, lines)
	if err != nil {
		return err
	}
	vecs, err := c.gw.Embed(ctx, []string{merged}, "memory-consolidate")
	if err != nil {
		return fmt.Errorf("embed merged fact: %w", err)
	}

	// The merged fact inherits the group's strongest provenance: it
	// replaces confirmed knowledge, so it activates directly.
	canonical := members[0]
	newID, err := c.store.Insert(ctx, store.Memory{
		Type: canonical.Type, Content: merged, Embedding: store.Vector(vecs[0]),
		EntityRefs: unionRefs(members), SourceSession: canonical.SourceSession,
		SourceSeq: canonical.SourceSeq, Confidence: maxConfidence(members),
	})
	if err != nil {
		return err
	}
	if err := c.store.Promote(ctx, newID); err != nil {
		return err
	}
	for _, m := range members {
		if err := c.store.Supersede(ctx, m.ID, newID); err != nil {
			c.log.Warn("supersede after merge failed", "member", m.ID, "merged", newID, "error", err)
		}
	}
	c.log.Info("near-dup group merged", "members", len(members), "merged", newID)
	return nil
}

func (c *Consolidator) mergedContent(ctx context.Context, lines []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, llmTimeout)
	defer cancel()
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

func (c *Consolidator) archiveStale(ctx context.Context) error {
	n, err := c.store.ArchiveStaleEpisodic(ctx, time.Now().Add(-episodicArchiveAfter))
	if err != nil {
		return err
	}
	if n > 0 {
		c.log.Info("stale episodic memories archived", "count", n)
		if c.metrics.Archived != nil {
			c.metrics.Archived.Add(float64(n))
		}
	}
	return nil
}

func (c *Consolidator) decayStale(ctx context.Context) error {
	ids, err := c.store.DecayStaleSemantic(ctx, time.Now().Add(-semanticDecayAfter), decayFactor, decayBatch)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		c.log.Info("stale semantic facts decayed; queued for reconfirmation", "count", len(ids))
		if c.metrics.Decayed != nil {
			c.metrics.Decayed.Add(float64(len(ids)))
		}
	}
	return nil
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
