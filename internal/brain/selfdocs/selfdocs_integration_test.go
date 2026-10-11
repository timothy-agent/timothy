//go:build integration

package selfdocs

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/memclient"
	"github.com/SumonMSelim/timothy/internal/memory/chunk"
	memstore "github.com/SumonMSelim/timothy/internal/memory/store"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

// Needs the compose stack up (make test-integration). The scenario
// runs twice: against memoryd's chunker and kb store in-process with a
// fixed embedding, and against the live memoryd, which skips when the
// gateway has no usable embedding route.

const itestName = "itest-selfdocs"

// localMemoryd is memoryd's ingest pipeline minus the gateway embed.
type localMemoryd struct{ kb *memstore.KBStore }

func (l localMemoryd) IngestDocument(ctx context.Context, id, title, markdown string) (int, error) {
	pieces := chunk.Split(title, markdown)
	vec := make(memstore.Vector, 1024)
	vec[0] = 1
	chunks := make([]memstore.KBChunk, len(pieces))
	for i, p := range pieces {
		chunks[i] = memstore.KBChunk{Seq: p.Seq, Breadcrumb: p.Breadcrumb, Content: p.Content, Embedding: vec, EmbeddingModel: "itest"}
	}
	if err := l.kb.ReplaceChunks(ctx, id, chunks); err != nil {
		return 0, err
	}
	return len(chunks), l.kb.SetIngested(ctx, id, len(chunks))
}

type countingIngest struct {
	next  Ingester
	calls int
	fail  bool
}

func (c *countingIngest) IngestDocument(ctx context.Context, id, title, markdown string) (int, error) {
	c.calls++
	if c.fail {
		return 0, errors.New("memoryd down")
	}
	return c.next.IngestDocument(ctx, id, title, markdown)
}

func setup(t *testing.T) (*kb.Store, *pgxpool.Pool, Ingester) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}

	poolCtx, poolCancel := context.WithCancel(context.Background())
	t.Cleanup(poolCancel)
	pool := pgpool.New(poolCtx, dsn, discard())
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, discard()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sweep := func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = db.Exec(cctx, `DELETE FROM kb_collections WHERE name = $1`, itestName)
		_, _ = db.Exec(cctx, `DELETE FROM kb_system_bundles WHERE name = $1`, itestName)
	}
	sweep()
	t.Cleanup(sweep)
	return kb.New(pool), db, localMemoryd{kb: memstore.NewKBStore(pool)}
}

func liveMemoryd(t *testing.T) Ingester {
	t.Helper()
	memURL := os.Getenv("MEMORYD_URL")
	if memURL == "" {
		memURL = "http://memoryd:8082"
	}
	resp, err := http.Get(memURL + "/health") //nolint:noctx // test probe
	if err != nil {
		t.Skipf("memoryd unreachable at %s: %v", memURL, err)
	}
	_ = resp.Body.Close()
	return memclient.New(memURL)
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type docRow struct {
	id, title, sourceType, sourceRef, provenance, status, appPath string
	chunks                                                        int
}

func docs(t *testing.T, db *pgxpool.Pool) map[string]docRow {
	t.Helper()
	rows, err := db.Query(t.Context(), `SELECT d.id, d.title, d.source_type, d.source_ref, d.provenance, d.status,
		coalesce(d.meta->>'app_path', ''), (SELECT count(*) FROM kb_chunks k WHERE k.document_id = d.id)
		FROM kb_documents d JOIN kb_collections c ON c.id = d.collection_id WHERE c.name = $1 AND c.system`, itestName)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]docRow{}
	for rows.Next() {
		var r docRow
		if err := rows.Scan(&r.id, &r.title, &r.sourceType, &r.sourceRef, &r.provenance, &r.status, &r.appPath, &r.chunks); err != nil {
			t.Fatal(err)
		}
		out[r.title] = r
	}
	return out
}

func bundleHash(t *testing.T, store *kb.Store) string {
	t.Helper()
	h, err := store.SystemBundleHash(t.Context(), itestName)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

const (
	itestPageTools = "---\ntitle: \"Tools\"\ndescription: \"Tools this build ships.\"\napp_path: \"/settings/tools\"\napp_label: \"Tools\"\nsource: \"manifest\"\n---\n\n# Tools\n\nsearch_web searches the public web through SearXNG.\n"
	itestPageGuide = "---\ntitle: Getting started\ndocs_url: https://timothy-agent.github.io/docs/start/\nsource: docs\n---\n\n# Getting started\n\nCopy deploy/env.example to deploy/.env and run make up.\n"
	itestPageNew   = "---\ntitle: Missions\nsource: docs\n---\n\n# Missions\n\nA mission runs in its own sandbox container.\n"
)

func TestSyncIntegration(t *testing.T) {
	store, db, local := setup(t)
	runSyncScenario(t, store, db, local)
}

func TestSyncIntegrationLiveMemoryd(t *testing.T) {
	store, db, _ := setup(t)
	live := liveMemoryd(t)
	if _, err := live.IngestDocument(t.Context(), "00000000-0000-0000-0000-000000000000", "probe", "probe"); err != nil &&
		(strings.Contains(err.Error(), "embedding_failed") || strings.Contains(err.Error(), "no_route")) {
		t.Skipf("live memoryd cannot embed: %v", err)
	}
	runSyncScenario(t, store, db, live)
}

func runSyncScenario(t *testing.T, store *kb.Store, db *pgxpool.Pool, mc Ingester) {
	t.Helper()
	ing := &countingIngest{next: mc}
	deps := func(dir string) Deps {
		return Deps{Dir: dir, Name: itestName, Store: store, Ingest: ing, Log: discard()}
	}

	v1 := writeFiles(t, map[string]string{
		"manifest.json": `{"version":"v1","files":["guide.md","tools.md"],"sha256":"itest-hash-1"}`,
		"tools.md":      itestPageTools, "guide.md": itestPageGuide,
	})
	if err := Sync(t.Context(), deps(v1)); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	first := docs(t, db)
	if len(first) != 2 || ing.calls != 2 {
		t.Fatalf("first sync docs = %+v, ingests = %d", first, ing.calls)
	}
	tools, guide := first["Tools"], first["Getting started"]
	if tools.sourceType != "selfdocs" || tools.provenance != "curated" || tools.appPath != "/settings/tools" || tools.status != "ready" || tools.chunks == 0 {
		t.Fatalf("tools doc = %+v", tools)
	}
	if guide.sourceRef != "https://timothy-agent.github.io/docs/start/" || guide.status != "ready" {
		t.Fatalf("guide doc = %+v", guide)
	}
	if h := bundleHash(t, store); h != "itest-hash-1" {
		t.Fatalf("bundle hash = %q", h)
	}

	// Second boot with the same bundle does nothing.
	if err := Sync(t.Context(), deps(v1)); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if ing.calls != 2 {
		t.Fatalf("second sync ingested again: calls = %d", ing.calls)
	}

	// A page missing title rejects the whole bundle; nothing changes.
	bad := writeFiles(t, map[string]string{
		"manifest.json": `{"version":"v2","files":["tools.md","x.md"],"sha256":"itest-hash-bad"}`,
		"tools.md":      itestPageTools, "x.md": "---\nsource: docs\n---\nno title\n",
	})
	if err := Sync(t.Context(), deps(bad)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad bundle err = %v, want ErrInvalid", err)
	}
	if got := docs(t, db); got["Tools"].id != tools.id || len(got) != 2 || bundleHash(t, store) != "itest-hash-1" {
		t.Fatalf("bad bundle touched the collection: %+v", got)
	}

	// A failed ingest leaves the bundle row on the old hash.
	v2 := writeFiles(t, map[string]string{
		"manifest.json": `{"version":"v2","files":["missions.md","tools.md"],"sha256":"itest-hash-2"}`,
		"tools.md":      itestPageTools, "missions.md": itestPageNew,
	})
	ing.fail = true
	if err := Sync(t.Context(), deps(v2)); err == nil {
		t.Fatal("failing ingest: Sync returned nil")
	}
	if h := bundleHash(t, store); h != "itest-hash-1" {
		t.Fatalf("failed sync moved the bundle row to %q", h)
	}

	// The changed bundle re-ingests and the old documents' chunks are gone.
	ing.fail = false
	if err := Sync(t.Context(), deps(v2)); err != nil {
		t.Fatalf("changed Sync: %v", err)
	}
	second := docs(t, db)
	if len(second) != 2 || second["Missions"].status != "ready" || second["Getting started"].id != "" {
		t.Fatalf("changed sync docs = %+v", second)
	}
	var stale int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM kb_chunks WHERE document_id = ANY($1::uuid[])`,
		[]string{tools.id, guide.id}).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatalf("old chunks left: %d", stale)
	}
	if h := bundleHash(t, store); h != "itest-hash-2" {
		t.Fatalf("bundle hash = %q, want itest-hash-2", h)
	}

	cs, err := store.ListCollections(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == itestName && (!c.System || c.BundleVersion != "v2" || c.Description != Description) {
			t.Fatalf("collection = %+v", c)
		}
	}
}

// TestSyncRefusesOperatorCollection: an operator collection that
// already holds the name is never cleared.
func TestSyncRefusesOperatorCollection(t *testing.T) {
	store, db, local := setup(t)
	if _, err := store.CreateCollection(t.Context(), itestName, "mine", 0); err != nil {
		t.Fatal(err)
	}
	dir := writeFiles(t, map[string]string{
		"manifest.json": `{"files":["tools.md"],"sha256":"itest-hash-op"}`, "tools.md": itestPageTools,
	})
	err := Sync(t.Context(), Deps{Dir: dir, Name: itestName, Store: store, Ingest: local, Log: discard()})
	if !errors.Is(err, kb.ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse", err)
	}
	var system bool
	if err := db.QueryRow(t.Context(), `SELECT system FROM kb_collections WHERE name = $1`, itestName).Scan(&system); err != nil || system {
		t.Fatalf("operator collection changed: system=%v err=%v", system, err)
	}
}
