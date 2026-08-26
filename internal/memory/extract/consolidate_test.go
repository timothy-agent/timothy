package extract

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/store"
)

func TestGroupPairs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		pairs [][2]string
		want  [][]string
	}{
		{name: "empty", pairs: nil, want: nil},
		{name: "one pair", pairs: [][2]string{{"a", "b"}}, want: [][]string{{"a", "b"}}},
		{name: "chain merges transitively", pairs: [][2]string{{"a", "b"}, {"b", "c"}},
			want: [][]string{{"a", "b", "c"}}},
		{name: "two disjoint groups", pairs: [][2]string{{"a", "b"}, {"x", "y"}},
			want: [][]string{{"a", "b"}, {"x", "y"}}},
		{name: "duplicate edges collapse", pairs: [][2]string{{"a", "b"}, {"a", "b"}},
			want: [][]string{{"a", "b"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := groupPairs(tc.pairs)
			if len(got) != len(tc.want) {
				t.Fatalf("groups = %v, want %v", got, tc.want)
			}
			for i := range got {
				if strings.Join(got[i], ",") != strings.Join(tc.want[i], ",") {
					t.Fatalf("group %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// consolidateStore fakes the store slice the consolidator needs.
type consolidateStore struct {
	fakeStore
	memories map[string]store.Memory
	pairs    [][2]string
	archived int64
	decayed  []string
	demoted  []string

	applyMergeErr  error
	superseded     map[string]string
	recentEpisodic []store.Memory

	pendingPairs [][2]string
	rejectErr    error
	rejected     []string
}

func (s *consolidateStore) RedundantPendingPairs(context.Context, float64) ([][2]string, error) {
	return s.pendingPairs, nil
}

func (s *consolidateStore) Reject(_ context.Context, id string) error {
	if s.rejectErr != nil {
		return s.rejectErr
	}
	s.rejected = append(s.rejected, id)
	return nil
}

func (s *consolidateStore) DemoteUnused(context.Context, time.Time, float64, int) ([]string, error) {
	return s.demoted, nil
}

func (s *consolidateStore) RecentEpisodic(context.Context, time.Time, int) ([]store.Memory, error) {
	return s.recentEpisodic, nil
}

func (s *consolidateStore) NearDupPairs(context.Context, float64) ([][2]string, error) {
	return s.pairs, nil
}

func (s *consolidateStore) Get(_ context.Context, id string) (store.Memory, error) {
	m, ok := s.memories[id]
	if !ok {
		return store.Memory{}, errors.New("not found")
	}
	return m, nil
}

// ApplyMerge records the merged fact as inserted and every member as
// superseded, in one call, mirroring the store's transactional apply.
func (s *consolidateStore) ApplyMerge(ctx context.Context, m store.Memory, memberIDs []string) (string, error) {
	if s.applyMergeErr != nil {
		return "", s.applyMergeErr
	}
	id, err := s.Insert(ctx, m)
	if err != nil {
		return "", err
	}
	if s.superseded == nil {
		s.superseded = map[string]string{}
	}
	for _, memberID := range memberIDs {
		s.superseded[memberID] = id
	}
	return id, nil
}

func (s *consolidateStore) ArchiveStaleEpisodic(context.Context, time.Time) (int64, error) {
	return s.archived, nil
}

func (s *consolidateStore) DecayStaleSemantic(context.Context, time.Time, float64, int) ([]string, error) {
	return s.decayed, nil
}

func activeMem(id, content string, conf float32, refs ...string) store.Memory {
	return store.Memory{
		ID: id, Type: store.TypeSemantic, Content: content,
		Status: store.StatusActive, Confidence: conf, EntityRefs: refs,
	}
}

func TestConsolidateMergesGroup(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{"The user lives in Porto, Portugal."}}
	st := &consolidateStore{
		pairs: [][2]string{{"m1", "m2"}},
		memories: map[string]store.Memory{
			"m1": activeMem("m1", "User lives in Porto.", 0.9, "ent-place"),
			"m2": activeMem("m2", "The user is based in Porto, Portugal.", 0.7, "ent-place", "ent-topic"),
		},
	}
	c := NewConsolidator(gw, st, testLog(), Metrics{})
	summary, err := c.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Merged != 1 || summary.Rejected != 0 {
		t.Fatalf("summary = %+v, want Merged:1 Rejected:0", summary)
	}

	if len(st.inserted) != 1 {
		t.Fatalf("inserted %d, want 1 merged fact", len(st.inserted))
	}
	merged := st.inserted[0]
	if merged.Content != "The user lives in Porto, Portugal." {
		t.Fatalf("merged content = %q", merged.Content)
	}
	if merged.Confidence != 0.9 {
		t.Fatalf("merged confidence = %v, want the group max 0.9", merged.Confidence)
	}
	if len(merged.EntityRefs) != 2 {
		t.Fatalf("merged refs = %v, want union of 2", merged.EntityRefs)
	}
	if len(merged.Embedding) == 0 {
		t.Fatal("merged fact has no embedding")
	}
	// The merged fact activates; both members point at it via a single
	// ApplyMerge call.
	mergedID := "mem-1"
	if len(st.superseded) != 2 || st.superseded["m1"] != mergedID || st.superseded["m2"] != mergedID {
		t.Fatalf("superseded = %v", st.superseded)
	}
}

func TestConsolidateDedupesPendingPairs(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{}
	st := &consolidateStore{
		pendingPairs: [][2]string{{"p1", "p2"}},
	}
	c := NewConsolidator(gw, st, testLog(), Metrics{})
	summary, err := c.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.PendingDeduped != 1 {
		t.Fatalf("summary.PendingDeduped = %d, want 1", summary.PendingDeduped)
	}
	if len(st.rejected) != 1 || st.rejected[0] != "p2" {
		t.Fatalf("rejected = %v, want [p2] (the newer half kept, p1 untouched)", st.rejected)
	}
}

func TestConsolidateDedupesPendingPairsSkipsRejectError(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{}
	st := &consolidateStore{
		pendingPairs: [][2]string{{"p1", "p2"}},
		rejectErr:    errors.New("already confirmed"),
	}
	c := NewConsolidator(gw, st, testLog(), Metrics{})
	summary, err := c.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.PendingDeduped != 0 {
		t.Fatalf("summary.PendingDeduped = %d, want 0 (reject failed, queue left as-is)", summary.PendingDeduped)
	}
}

func TestConsolidateSkipsDissolvedGroups(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{"never used"}}
	st := &consolidateStore{
		pairs: [][2]string{{"m1", "m2"}},
		memories: map[string]store.Memory{
			"m1": activeMem("m1", "fact", 0.9),
			"m2": {ID: "m2", Type: store.TypeSemantic, Content: "fact", Status: store.StatusArchived},
		},
	}
	c := NewConsolidator(gw, st, testLog(), Metrics{})
	summary, err := c.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Merged != 0 || summary.Rejected != 0 {
		t.Fatalf("dissolved group counted: summary = %+v, want all zero", summary)
	}
	if len(st.inserted) != 0 || len(st.superseded) != 0 {
		t.Fatalf("dissolved group still merged: inserted=%d superseded=%v",
			len(st.inserted), st.superseded)
	}
}

func TestConsolidateMergeFailureKeepsGroup(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{""}} // LLM returns nothing
	st := &consolidateStore{
		pairs: [][2]string{{"m1", "m2"}},
		memories: map[string]store.Memory{
			"m1": activeMem("m1", "fact one", 0.9),
			"m2": activeMem("m2", "fact one again", 0.8),
		},
	}
	c := NewConsolidator(gw, st, testLog(), Metrics{})
	summary, err := c.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v (stage failures log, they don't fail the pass)", err)
	}
	if summary.Merged != 0 || summary.Rejected != 1 {
		t.Fatalf("summary = %+v, want Merged:0 Rejected:1", summary)
	}
	if len(st.inserted) != 0 || len(st.superseded) != 0 {
		t.Fatal("failed merge still mutated the store")
	}
}

func TestMergeGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		members []string
		merged  string
		want    string
	}{
		{
			name:    "plausible merge passes",
			members: []string{"User lives in Porto.", "The user is based in Porto, Portugal."},
			merged:  "The user lives in Porto, Portugal.",
			want:    "",
		},
		{
			name:    "drops a significant detail",
			members: []string{"User was born in 1990 in Lisbon.", "The user grew up in Lisbon."},
			merged:  "The user is from somewhere.",
			want:    "token_loss",
		},
		{
			name:    "shrinks below the longest member despite retaining half the tokens",
			members: []string{"The user deployed version 2.3.4 to production on 2026-07-11 after three days of testing."},
			merged:  "User 2026 days 2.4.3.11",
			want:    "shrink",
		},
		{
			name: "bloats with preamble",
			members: []string{
				"User lives in Porto.",
			},
			merged: "Sure, here is a merged fact reflecting the information you provided about where the user currently resides, " +
				"which appears to be the beautiful coastal city of Porto in Portugal, a place known for its port wine and river views, " +
				"and this sentence keeps going just to pad out the length far past any reasonable multiple of the original short fact.",
			want: "bloat",
		},
		{
			name:    "empty signature set skips token check",
			members: []string{"a b c"}, // no token >= 4 runes and no digit
			merged:  "a b c",
			want:    "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mergeGuard(tc.members, tc.merged); got != tc.want {
				t.Fatalf("mergeGuard = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConsolidateGuardRejectKeepsGroup(t *testing.T) {
	t.Parallel()
	// The merged reply drops the members' significant detail — the
	// guard must reject it before ApplyMerge is ever called.
	gw := &fakeGateway{replies: []string{"The user is from somewhere."}}
	st := &consolidateStore{
		pairs: [][2]string{{"m1", "m2"}},
		memories: map[string]store.Memory{
			"m1": activeMem("m1", "User was born in 1990 in Lisbon.", 0.9),
			"m2": activeMem("m2", "The user grew up in Lisbon.", 0.8),
		},
	}
	c := NewConsolidator(gw, st, testLog(), Metrics{})
	summary, err := c.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Merged != 0 || summary.Rejected != 1 {
		t.Fatalf("summary = %+v, want Merged:0 Rejected:1", summary)
	}
	if len(st.inserted) != 0 || len(st.superseded) != 0 {
		t.Fatal("guard-rejected merge still mutated the store")
	}
}

func TestConsolidateApplyConflictKeepsGroup(t *testing.T) {
	t.Parallel()
	// A plausible merge, but ApplyMerge reports a member changed
	// underneath the read (wraps ErrNotFound) — classified "conflict",
	// group kept as-is, no partial mutation.
	gw := &fakeGateway{replies: []string{"The user lives in Porto, Portugal."}}
	st := &consolidateStore{
		pairs: [][2]string{{"m1", "m2"}},
		memories: map[string]store.Memory{
			"m1": activeMem("m1", "User lives in Porto.", 0.9),
			"m2": activeMem("m2", "The user is based in Porto, Portugal.", 0.7),
		},
		applyMergeErr: fmt.Errorf("apply merge supersede m1: %w", store.ErrNotFound),
	}
	c := NewConsolidator(gw, st, testLog(), Metrics{})
	summary, err := c.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v (stage failures log, they don't fail the pass)", err)
	}
	if summary.Merged != 0 || summary.Rejected != 1 {
		t.Fatalf("summary = %+v, want Merged:0 Rejected:1", summary)
	}
	if len(st.inserted) != 0 || len(st.superseded) != 0 {
		t.Fatal("conflicting merge still mutated the store")
	}
}

// Reflection distills recent episodics into pending semantic insights
// through the extractor pipeline; below the minimum episode count the
// pass is a no-op, and an unwired reflector disables it entirely.
func TestConsolidatorReflect(t *testing.T) {
	t.Parallel()
	episodics := make([]store.Memory, reflectMinEpisodics)
	for i := range episodics {
		episodics[i] = store.Memory{ID: fmt.Sprintf("e%d", i), Type: store.TypeEpisodic,
			Status: store.StatusActive, Content: "user asked about the juaab ALB alarm again", CreatedAt: time.Now()}
	}
	st := &consolidateStore{recentEpisodic: episodics}
	gw := &fakeGateway{replies: []string{`[{"type":"semantic","content":"The juaab admin ALB alarm recurs and the user always wants it triaged first.","entities":[],"confidence":0.8,"changes_behavior":true}]`}}
	inner := &fakeStore{}
	c := NewConsolidator(gw, st, testLog(), Metrics{})
	c.SetReflector(New(gw, inner, testLog()))

	summary, err := c.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Reflected != 1 || len(inner.inserted) != 1 {
		t.Fatalf("Reflected = %d inserted = %d, want 1/1", summary.Reflected, len(inner.inserted))
	}

	// Below the minimum: no LLM call, nothing minted.
	st.recentEpisodic = episodics[:reflectMinEpisodics-1]
	gw2 := &fakeGateway{replies: []string{`[]`}}
	inner2 := &fakeStore{}
	c2 := NewConsolidator(gw2, st, testLog(), Metrics{})
	c2.SetReflector(New(gw2, inner2, testLog()))
	summary, err = c2.Run(t.Context())
	if err != nil {
		t.Fatalf("Run below minimum: %v", err)
	}
	if summary.Reflected != 0 || gw2.calls != 0 {
		t.Fatalf("below minimum: Reflected=%d llm calls=%d, want 0/0", summary.Reflected, gw2.calls)
	}

	// Unwired reflector: pass disabled.
	c3 := NewConsolidator(gw2, st, testLog(), Metrics{})
	if s, err := c3.Run(t.Context()); err != nil || s.Reflected != 0 {
		t.Fatalf("unwired reflector: %+v err=%v", s, err)
	}
}

// TestConsolidateDemotesUnused checks that Run surfaces whatever the
// store's DemoteUnused selects into Summary.Demoted - the selection
// logic itself (eligible vs immune rows) lives in the store's own
// query and is covered by the store integration test.
func TestConsolidateDemotesUnused(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{}
	st := &consolidateStore{demoted: []string{"m1", "m2"}}
	c := NewConsolidator(gw, st, testLog(), Metrics{})
	summary, err := c.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Demoted != 2 {
		t.Fatalf("Demoted = %d, want 2", summary.Demoted)
	}
}

func TestConsolidateDemotesNothing(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{}
	st := &consolidateStore{}
	c := NewConsolidator(gw, st, testLog(), Metrics{})
	summary, err := c.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Demoted != 0 {
		t.Fatalf("Demoted = %d, want 0", summary.Demoted)
	}
}
