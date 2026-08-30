package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeKBSearch struct {
	hits     []KBSearchHit
	err      error
	sawQuery string
	sawMode  string
	sawK     int
}

func (f *fakeKBSearch) search(_ context.Context, query, mode string, k int) ([]KBSearchHit, error) {
	f.sawQuery, f.sawMode, f.sawK = query, mode, k
	return f.hits, f.err
}

func TestKBSearchFormatsHits(t *testing.T) {
	t.Parallel()
	fk := &fakeKBSearch{hits: []KBSearchHit{
		{DocumentID: "doc-abc-123", DocumentTitle: "Runbook", Breadcrumb: "Runbook > Deploy", Content: "Run make deploy.", SourceRef: "runbook.md", Score: 0.8123},
		{DocumentTitle: "FAQ", Content: "Ask ops."},
	}}
	tool := KBSearch(fk.search)
	args, _ := json.Marshal(map[string]string{"query": "how to deploy"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"1. Runbook", "Runbook > Deploy", "runbook.md", "Source: kb://doc-abc-123 (score 0.8123)", "Run make deploy.", "2. FAQ", "Ask ops."} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if fk.sawQuery != "how to deploy" || fk.sawK != kbSearchDefaultK {
		t.Fatalf("query/k not passed through: query=%q k=%d", fk.sawQuery, fk.sawK)
	}
}

// TestKBSearchOmitsSourceLineWithoutDocumentID covers a hit with no
// DocumentID (a memclient response shape that predates this field, or
// a genuinely missing id) — formatKBHits must not render a bare
// "Source: kb://" ref with nothing after the scheme.
func TestKBSearchOmitsSourceLineWithoutDocumentID(t *testing.T) {
	t.Parallel()
	fk := &fakeKBSearch{hits: []KBSearchHit{{DocumentTitle: "FAQ", Content: "Ask ops."}}}
	tool := KBSearch(fk.search)
	args, _ := json.Marshal(map[string]string{"query": "q"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "Source: kb://") {
		t.Fatalf("output has a Source line with no document id:\n%s", out)
	}
}

func TestKBSearchNoResults(t *testing.T) {
	t.Parallel()
	fk := &fakeKBSearch{hits: nil}
	tool := KBSearch(fk.search)
	args, _ := json.Marshal(map[string]string{"query": "nothing"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "no matching passages found" {
		t.Fatalf("out = %q", out)
	}
}

func TestKBSearchRejectsEmptyQuery(t *testing.T) {
	t.Parallel()
	tool := KBSearch((&fakeKBSearch{}).search)
	args, _ := json.Marshal(map[string]string{"query": "  "})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected an error for empty query")
	}
}

func TestKBSearchRejectsInvalidMode(t *testing.T) {
	t.Parallel()
	tool := KBSearch((&fakeKBSearch{}).search)
	args, _ := json.Marshal(map[string]string{"query": "x", "mode": "vibes"})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected an error for invalid mode")
	}
}

func TestKBSearchRejectsKOutOfRange(t *testing.T) {
	t.Parallel()
	for _, k := range []int{0, 11, -1} {
		k := k
		t.Run("", func(t *testing.T) {
			t.Parallel()
			tool := KBSearch((&fakeKBSearch{}).search)
			args, _ := json.Marshal(map[string]any{"query": "x", "k": k})
			if _, err := tool.Execute(context.Background(), args); err == nil {
				t.Fatalf("k=%d: expected an error", k)
			}
		})
	}
}

func TestKBSearchPassesModeAndK(t *testing.T) {
	t.Parallel()
	fk := &fakeKBSearch{}
	tool := KBSearch(fk.search)
	args, _ := json.Marshal(map[string]any{"query": "x", "mode": "keyword", "k": 3})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fk.sawMode != "keyword" || fk.sawK != 3 {
		t.Fatalf("mode/k = %q/%d, want keyword/3", fk.sawMode, fk.sawK)
	}
}

func TestKBSearchPropagatesSearchError(t *testing.T) {
	t.Parallel()
	fk := &fakeKBSearch{err: errors.New("memoryd down")}
	tool := KBSearch(fk.search)
	args, _ := json.Marshal(map[string]string{"query": "x"})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected the search error to propagate")
	}
}
