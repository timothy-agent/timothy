package selfdocs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/memclient"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
)

type fakeSearcher struct {
	hits               []memclient.KBChunkHit
	err                error
	sawNames, sawBoost []string
	sawMode, sawQuery  string
	sawK               int
}

func (f *fakeSearcher) KBSearch(_ context.Context, query string, names, boost []string, mode string, k int) ([]memclient.KBChunkHit, error) {
	f.sawQuery, f.sawNames, f.sawBoost, f.sawMode, f.sawK = query, names, boost, mode, k
	return f.hits, f.err
}

type fakeMeta struct {
	meta   map[string]map[string]string
	err    error
	sawIDs []string
}

func (f *fakeMeta) DocumentMeta(_ context.Context, ids []string) (map[string]map[string]string, error) {
	f.sawIDs = ids
	return f.meta, f.err
}

func TestHelpSearch(t *testing.T) {
	t.Parallel()
	s := &fakeSearcher{hits: []memclient.KBChunkHit{
		{DocumentID: "d1", DocumentTitle: "Connectors", Content: "Add a Gmail account.", SourceRef: "https://docs/connectors/"},
		{DocumentID: "d2", DocumentTitle: "Getting started", Content: "Run make up."},
	}}
	m := &fakeMeta{meta: map[string]map[string]string{"d1": {"app_path": "/settings/connectors", "app_label": "Connectors", "source": "docs"}}}
	got, err := HelpSearch(s, m, Collection)(t.Context(), "gmail", 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []builtin.HelpHit{
		{Title: "Connectors", Excerpt: "Add a Gmail account.", DocsURL: "https://docs/connectors/", AppPath: "/settings/connectors", AppLabel: "Connectors"},
		{Title: "Getting started", Excerpt: "Run make up."},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("hits = %+v, want %+v", got, want)
	}
	if !slices.Equal(s.sawNames, []string{Collection}) || s.sawBoost != nil || s.sawMode != "hybrid" || s.sawK != 4 || s.sawQuery != "gmail" {
		t.Fatalf("search called with names=%v boost=%v mode=%q k=%d query=%q", s.sawNames, s.sawBoost, s.sawMode, s.sawK, s.sawQuery)
	}
	if !slices.Equal(m.sawIDs, []string{"d1", "d2"}) {
		t.Fatalf("meta ids = %v", m.sawIDs)
	}
}

func TestHelpSearchErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		s    *fakeSearcher
		m    *fakeMeta
		want string
	}{
		{"search fails", &fakeSearcher{err: errors.New("memoryd down")}, &fakeMeta{}, "memoryd down"},
		{"meta fails", &fakeSearcher{hits: []memclient.KBChunkHit{{DocumentID: "d1"}}}, &fakeMeta{err: errors.New("db down")}, "db down"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := HelpSearch(tc.s, tc.m, Collection)(t.Context(), "q", 6)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
