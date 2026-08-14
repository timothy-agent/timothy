//go:build integration

package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

// Golden retrieval eval for the knowledge base, mirroring the memories
// eval (internal/memory/retrieval/golden_integration_test.go): basis
// vectors stand in for real embeddings, so the vector leg discriminates
// perfectly and the eval isolates the SQL, not the embedding model.

const kbGoldenCollection = "itest-kbgolden"
const kbGoldenOther = "itest-kbgolden-other"

func kbBasis(dim int) Vector {
	v := make(Vector, 1024)
	v[dim] = 1
	return v
}

// kbFixture is one chunk; queries reference fixtures by key.
type kbFixture struct {
	key     string
	content string
	dim     int
}

// Two groups, each reachable primarily through ONE leg: vector-group
// queries paraphrase with no lexical overlap; keyword-group queries
// share words while their embedding points at an unused dimension.
var kbCorpus = []kbFixture{
	{key: "v-loki", dim: 1, content: "Log aggregation runs in single-binary mode and listens on port 3100."},
	{key: "v-tempo", dim: 2, content: "Distributed tracing storage retains spans for fourteen days."},
	{key: "v-mimir", dim: 3, content: "Long-term metrics storage compacts blocks every two hours."},

	{key: "t-alloy", dim: 20, content: "Grafana Alloy scrapes node exporters and forwards to the collector."},
	{key: "t-otel", dim: 21, content: "The OpenTelemetry Collector fans out traces, logs, and metrics."},
	{key: "t-dash", dim: 22, content: "Dashboards provision from JSON files under the provisioning directory."},
}

type kbGolden struct {
	text string
	dim  int
	want string
}

var kbGoldens = []kbGolden{
	// vector group — no lexical overlap
	{text: "where do the logs go?", dim: 1, want: "v-loki"},
	{text: "how long are request journeys kept?", dim: 2, want: "v-tempo"},
	{text: "what happens to old measurements?", dim: 3, want: "v-mimir"},

	// keyword group — shared words, useless embedding
	{text: "grafana alloy node exporters", dim: 900, want: "t-alloy"},
	{text: "opentelemetry collector fans out", dim: 901, want: "t-otel"},
	{text: "dashboards provisioning JSON", dim: 902, want: "t-dash"},

	// multi-topic: AND-semantics would return nothing; each chunk
	// answering PART of the question must surface.
	{text: "how does alloy scrape exporters and where do dashboards provision from?", dim: 903, want: "t-alloy"},
	{text: "how does alloy scrape exporters and where do dashboards provision from?", dim: 904, want: "t-dash"},
}

func seedKBGolden(t *testing.T) *KBStore {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, _ = db.Exec(ctx, "DELETE FROM kb_collections WHERE name LIKE 'itest-kbgolden%'")
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		_, _ = conn.Exec(cctx, "DELETE FROM kb_collections WHERE name LIKE 'itest-kbgolden%'")
	})

	seed := func(collection, title string, fixtures []kbFixture) {
		var collID string
		if err := db.QueryRow(ctx,
			"INSERT INTO kb_collections (name) VALUES ($1) RETURNING id", collection).Scan(&collID); err != nil {
			t.Fatalf("insert collection: %v", err)
		}
		var docID string
		if err := db.QueryRow(ctx, `INSERT INTO kb_documents (collection_id, title, status)
			VALUES ($1, $2, 'ready') RETURNING id`, collID, title).Scan(&docID); err != nil {
			t.Fatalf("insert document: %v", err)
		}
		chunks := make([]KBChunk, len(fixtures))
		for i, f := range fixtures {
			chunks[i] = KBChunk{Seq: i, Content: f.content, Embedding: kbBasis(f.dim), EmbeddingModel: "itest"}
		}
		st := NewKBStore(pool)
		if err := st.ReplaceChunks(ctx, docID, chunks); err != nil {
			t.Fatalf("ReplaceChunks: %v", err)
		}
	}
	seed(kbGoldenCollection, "golden", kbCorpus)
	// A second collection holding the SAME content proves scoping: it
	// must never leak into searches scoped to the golden collection.
	seed(kbGoldenOther, "other", kbCorpus)
	return NewKBStore(pool)
}

func kbHitKeys(t *testing.T, hits []KBSearchHit) map[string]bool {
	t.Helper()
	byContent := map[string]string{}
	for _, f := range kbCorpus {
		byContent[f.content] = f.key
	}
	got := map[string]bool{}
	for _, h := range hits {
		got[byContent[h.Content]] = true
	}
	return got
}

func TestKBGoldenRecall(t *testing.T) {
	st := seedKBGolden(t)
	hitCount := 0
	for _, q := range kbGoldens {
		hits, err := st.KBSearch(t.Context(), q.text, kbBasis(q.dim), []string{kbGoldenCollection}, KBSearchHybrid, 5)
		if err != nil {
			t.Fatalf("KBSearch %q: %v", q.text, err)
		}
		for _, h := range hits {
			if h.Collection != kbGoldenCollection {
				t.Fatalf("query %q leaked collection %q", q.text, h.Collection)
			}
		}
		if kbHitKeys(t, hits)[q.want] {
			hitCount++
		} else {
			t.Logf("miss: %q wanted %s", q.text, q.want)
		}
	}
	recall := float64(hitCount) / float64(len(kbGoldens))
	if recall < 1.0 {
		t.Fatalf("recall@5 = %.2f, want 1.0 (basis vectors leave no excuse)", recall)
	}
}

func TestKBGoldenOffTopicReturnsNothing(t *testing.T) {
	st := seedKBGolden(t)
	// No lexical overlap with any chunk (mind stemming: "filings"
	// would match "files"), embedding on an unused axis: both legs
	// must gate it out — returning nothing beats returning the k
	// least-far chunks.
	hits, err := st.KBSearch(t.Context(), "quarterly marmalade recipes for zebras", kbBasis(950), []string{kbGoldenCollection}, KBSearchHybrid, 5)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("off-topic query returned %d hits, want 0", len(hits))
	}
}

func TestKBGoldenKeywordOnlyMultiTopic(t *testing.T) {
	st := seedKBGolden(t)
	hits, err := st.KBSearch(t.Context(),
		"how does alloy scrape exporters and where do dashboards provision from?",
		nil, []string{kbGoldenCollection}, KBSearchKeyword, 5)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	got := kbHitKeys(t, hits)
	if !got["t-alloy"] || !got["t-dash"] {
		t.Fatalf("multi-topic keyword search missed a partial answer: got %v", got)
	}
}

func TestKBGoldenSemanticFloor(t *testing.T) {
	st := seedKBGolden(t)
	// On-axis query hits; orthogonal query (similarity 0) must not.
	hits, err := st.KBSearch(t.Context(), "anything", kbBasis(1), []string{kbGoldenCollection}, KBSearchSemantic, 5)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	if got := kbHitKeys(t, hits); !got["v-loki"] || len(hits) != 1 {
		t.Fatalf("semantic on-axis: got %d hits %v, want exactly v-loki", len(hits), got)
	}
	hits, err = st.KBSearch(t.Context(), "anything", kbBasis(999), []string{kbGoldenCollection}, KBSearchSemantic, 5)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("semantic orthogonal: got %d hits, want 0 (similarity floor)", len(hits))
	}
}
