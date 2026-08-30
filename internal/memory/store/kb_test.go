package store

import "testing"

// TestApplyPerDocumentCapDropsOverflow pins the core cap behavior: a
// dominant document's excess chunks are dropped, never reordered, and
// the freed slots go to the next-ranked chunks of other documents.
func TestApplyPerDocumentCapDropsOverflow(t *testing.T) {
	candidates := []KBSearchHit{
		{ChunkID: "a1", DocumentID: "docA", Score: 0.9},
		{ChunkID: "a2", DocumentID: "docA", Score: 0.8},
		{ChunkID: "a3", DocumentID: "docA", Score: 0.7},
		{ChunkID: "a4", DocumentID: "docA", Score: 0.6},
		{ChunkID: "b1", DocumentID: "docB", Score: 0.5},
		{ChunkID: "c1", DocumentID: "docC", Score: 0.4},
	}
	got := applyPerDocumentCap(candidates, 4)
	if len(got) != 4 {
		t.Fatalf("got %d hits, want 4", len(got))
	}
	var docACount int
	seenDocs := map[string]bool{}
	for _, h := range got {
		if h.DocumentID == "docA" {
			docACount++
		}
		seenDocs[h.DocumentID] = true
	}
	if docACount != kbPerDocumentCap {
		t.Fatalf("docA count = %d, want %d (cap)", docACount, kbPerDocumentCap)
	}
	if !seenDocs["docB"] || !seenDocs["docC"] {
		t.Fatalf("expected freed slots to include docB and docC, got %+v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Score > got[i-1].Score {
			t.Fatalf("result not score-descending at index %d: %+v", i, got)
		}
	}
}

// TestApplyPerDocumentCapBackfillsNarrowCorpus pins the backfill
// contract: when only one document matches, the cap must not starve
// the result down below min(k, available) hits.
func TestApplyPerDocumentCapBackfillsNarrowCorpus(t *testing.T) {
	candidates := []KBSearchHit{
		{ChunkID: "a1", DocumentID: "docA", Score: 0.9},
		{ChunkID: "a2", DocumentID: "docA", Score: 0.8},
		{ChunkID: "a3", DocumentID: "docA", Score: 0.7},
		{ChunkID: "a4", DocumentID: "docA", Score: 0.6},
	}
	got := applyPerDocumentCap(candidates, 5)
	if len(got) != 4 {
		t.Fatalf("got %d hits, want 4 (all available, backfilled past the cap)", len(got))
	}
	for i, h := range got {
		if h.ChunkID != candidates[i].ChunkID {
			t.Fatalf("backfill reordered results: got %+v, want candidate order", got)
		}
	}
}

// TestApplyPerDocumentCapNoOverflowIsNoop pins the common case: when no
// document exceeds the cap, selection just truncates to k in order.
func TestApplyPerDocumentCapNoOverflowIsNoop(t *testing.T) {
	candidates := []KBSearchHit{
		{ChunkID: "a1", DocumentID: "docA", Score: 0.9},
		{ChunkID: "b1", DocumentID: "docB", Score: 0.8},
		{ChunkID: "c1", DocumentID: "docC", Score: 0.7},
	}
	got := applyPerDocumentCap(candidates, 2)
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2", len(got))
	}
	if got[0].ChunkID != "a1" || got[1].ChunkID != "b1" {
		t.Fatalf("got %+v, want top 2 by score in order", got)
	}
}
