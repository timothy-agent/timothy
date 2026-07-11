package extract

import (
	"context"
	"errors"
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

	superseded map[string]string
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

func (s *consolidateStore) Supersede(_ context.Context, oldID, newID string) error {
	if s.superseded == nil {
		s.superseded = map[string]string{}
	}
	s.superseded[oldID] = newID
	return nil
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
	if err := c.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
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
	// The merged fact activates; both members point at it.
	if len(st.promoted) != 1 {
		t.Fatalf("promoted = %v, want the merged id", st.promoted)
	}
	if len(st.superseded) != 2 || st.superseded["m1"] != st.promoted[0] || st.superseded["m2"] != st.promoted[0] {
		t.Fatalf("superseded = %v", st.superseded)
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
	if err := c.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
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
	if err := c.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v (stage failures log, they don't fail the pass)", err)
	}
	if len(st.inserted) != 0 || len(st.superseded) != 0 {
		t.Fatal("failed merge still mutated the store")
	}
}
