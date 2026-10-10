//go:build integration

package selfdocs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/memclient"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
	memstore "github.com/SumonMSelim/timothy/internal/memory/store"
)

// localSearch is memoryd's /v1/kb-search minus the gateway embed: the
// query embeds to the same fixed vector localMemoryd stores.
type localSearch struct{ kb *memstore.KBStore }

func (l localSearch) KBSearch(ctx context.Context, query string, names, boost []string, mode string, k int) ([]memclient.KBChunkHit, error) {
	vec := make(memstore.Vector, 1024)
	vec[0] = 1
	hits, err := l.kb.KBSearch(ctx, query, vec, names, boost, memstore.KBSearchMode(mode), k)
	if err != nil {
		return nil, err
	}
	out := make([]memclient.KBChunkHit, len(hits))
	for i, h := range hits {
		out[i] = memclient.KBChunkHit{
			ChunkID: h.ChunkID, DocumentID: h.DocumentID, DocumentTitle: h.DocumentTitle, Collection: h.Collection,
			Breadcrumb: h.Breadcrumb, Content: h.Content, Score: h.Score, SourceRef: h.SourceRef,
		}
	}
	return out, nil
}

// syncFixture ingests the Tools and Getting started pages into the
// itest system collection.
func syncFixture(t *testing.T) (*kb.Store, localSearch) {
	t.Helper()
	store, _, local := setup(t)
	dir := writeFiles(t, map[string]string{
		"manifest.json": `{"version":"v-help","files":["guide.md","tools.md"],"sha256":"itest-help-hash"}`,
		"tools.md":      itestPageTools, "guide.md": itestPageGuide,
	})
	if err := Sync(t.Context(), Deps{Dir: dir, Name: itestName, Store: store, Ingest: local, Log: discard()}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return store, localSearch{kb: local.(localMemoryd).kb}
}

func TestHelpSearchIntegration(t *testing.T) {
	store, search := syncFixture(t)
	hits, err := HelpSearch(search, store, itestName)(t.Context(), "search the public web with searxng", 6)
	if err != nil {
		t.Fatalf("HelpSearch: %v", err)
	}
	byTitle := map[string]builtin.HelpHit{}
	for _, h := range hits {
		byTitle[h.Title] = h
	}
	toolsHit, ok := byTitle["Tools"]
	if !ok {
		t.Fatalf("Tools page not returned: %+v", hits)
	}
	if toolsHit.AppPath != "/settings/tools" || toolsHit.AppLabel != "Tools" || !strings.Contains(toolsHit.Excerpt, "SearXNG") {
		t.Fatalf("Tools hit = %+v", toolsHit)
	}
	if guide, ok := byTitle["Getting started"]; ok && (guide.AppPath != "" || guide.DocsURL != "https://timothy-agent.github.io/docs/start/") {
		t.Fatalf("Getting started hit = %+v", guide)
	}
}

// TestKBSearchExcludesSystemCollectionsIntegration is the search_kb
// regression of issue #1127: chat's whole-KB search (nil names, the
// agent's Knowledge as boost) never returns self-docs unless the boost
// names the collection.
func TestKBSearchExcludesSystemCollectionsIntegration(t *testing.T) {
	_, search := syncFixture(t)
	for _, mode := range []string{"hybrid", "semantic", "keyword"} {
		t.Run(mode, func(t *testing.T) {
			query := "search the public web with searxng"
			without, err := search.KBSearch(t.Context(), query, nil, []string{"some-operator-collection"}, mode, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range without {
				if h.Collection == itestName {
					t.Fatalf("whole-KB search without the collection allowlisted returned self-docs: %+v", h)
				}
			}
			with, err := search.KBSearch(t.Context(), query, nil, []string{itestName}, mode, 10)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, h := range with {
				found = found || h.Collection == itestName
			}
			if !found {
				t.Fatalf("whole-KB search with the collection allowlisted returned no self-docs: %+v", with)
			}
		})
	}
}

// TestTimothyHelpSmokeIntegration runs timothy_help through the
// schema-checked tool registry the loop dispatches through, over the
// real database: the result carries the running version and a
// relative app link the chat answer can cite.
func TestTimothyHelpSmokeIntegration(t *testing.T) {
	store, search := syncFixture(t)
	reg := tools.NewRegistry()
	agents := func(context.Context) ([]string, error) { return []string{"general"}, nil }
	if err := reg.Register(builtin.TimothyHelp(builtin.TimothyHelpConfig{
		Search:  HelpSearch(search, store, itestName),
		Version: "0.1.0-alpha.itest",
		Agents:  agents,
	})); err != nil {
		t.Fatal(err)
	}
	calls := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "itest_tool_calls_total"}, []string{"tool", "outcome"})
	constrained, err := tools.NewConstrained(reg, calls)
	if err != nil {
		t.Fatal(err)
	}
	out, err := constrained.Execute(t.Context(), "timothy_help", json.RawMessage(`{"query":"what can you do? which tools search the web"}`))
	if err != nil {
		t.Fatalf("timothy_help: %v", err)
	}
	for _, want := range []string{"- Version: 0.1.0-alpha.itest", "App screen: [Tools](/settings/tools)", "- Agents: general"} {
		if !strings.Contains(out, want) {
			t.Fatalf("timothy_help output missing %q:\n%s", want, out)
		}
	}
	if !constrained.Trusted("timothy_help") {
		t.Fatal("timothy_help result must be trusted")
	}
}

// TestDocumentMetaIntegration covers kb.Store.DocumentMeta: string
// fields come back per id, unknown ids are absent.
func TestDocumentMetaIntegration(t *testing.T) {
	store, db, local := setup(t)
	dir := writeFiles(t, map[string]string{
		"manifest.json": `{"files":["tools.md"],"sha256":"itest-meta-hash"}`, "tools.md": itestPageTools,
	})
	if err := Sync(t.Context(), Deps{Dir: dir, Name: itestName, Store: store, Ingest: local, Log: discard()}); err != nil {
		t.Fatal(err)
	}
	id := docs(t, db)["Tools"].id
	meta, err := store.DocumentMeta(t.Context(), []string{id, "00000000-0000-0000-0000-000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) != 1 || meta[id]["app_path"] != "/settings/tools" || meta[id]["app_label"] != "Tools" || meta[id]["source"] != "manifest" {
		t.Fatalf("meta = %+v", meta)
	}
	empty, err := store.DocumentMeta(t.Context(), nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("DocumentMeta(nil) = %+v, %v", empty, err)
	}
}
