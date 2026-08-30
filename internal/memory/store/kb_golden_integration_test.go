//go:build integration

package store

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
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
	// vector group: no lexical overlap
	{text: "where do the logs go?", dim: 1, want: "v-loki"},
	{text: "how long are request journeys kept?", dim: 2, want: "v-tempo"},
	{text: "what happens to old measurements?", dim: 3, want: "v-mimir"},

	// keyword group: shared words, useless embedding
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
		hits, err := st.KBSearch(t.Context(), q.text, kbBasis(q.dim), []string{kbGoldenCollection}, nil, KBSearchHybrid, 5)
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

// TestKBGoldenEmptyCollectionsSearchesWholeKB pins issue #368's default:
// nil/empty collectionNames must not scope the search at all: hits
// from both seeded collections come back for a query that matches
// content present in both.
func TestKBGoldenEmptyCollectionsSearchesWholeKB(t *testing.T) {
	st := seedKBGolden(t)
	hits, err := st.KBSearch(t.Context(), "where do the logs go?", kbBasis(1), nil, nil, KBSearchHybrid, 10)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.Collection] = true
	}
	if !seen[kbGoldenCollection] || !seen[kbGoldenOther] {
		t.Fatalf("whole-KB search got collections %v, want both %s and %s", seen, kbGoldenCollection, kbGoldenOther)
	}
}

// TestKBGoldenBoostRanksHigherWithoutFiltering pins the boost contract:
// a collection in boostCollections must never hide the other
// collection's equally-relevant chunk, it only has to rank first.
func TestKBGoldenBoostRanksHigherWithoutFiltering(t *testing.T) {
	st := seedKBGolden(t)
	hits, err := st.KBSearch(t.Context(), "where do the logs go?", kbBasis(1), nil, []string{kbGoldenOther}, KBSearchHybrid, 10)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("got %d hits, want at least 2 (one per collection)", len(hits))
	}
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.Collection] = true
	}
	if !seen[kbGoldenCollection] || !seen[kbGoldenOther] {
		t.Fatalf("boost filtered out a collection: got %v, want both present", seen)
	}
	if hits[0].Collection != kbGoldenOther {
		t.Fatalf("top hit collection = %q, want boosted collection %q ranked first", hits[0].Collection, kbGoldenOther)
	}
}

func TestKBGoldenOffTopicReturnsNothing(t *testing.T) {
	st := seedKBGolden(t)
	// No lexical overlap with any chunk (mind stemming: "filings"
	// would match "files"), embedding on an unused axis: both legs
	// must gate it out: returning nothing beats returning the k
	// least-far chunks.
	hits, err := st.KBSearch(t.Context(), "quarterly marmalade recipes for zebras", kbBasis(950), []string{kbGoldenCollection}, nil, KBSearchHybrid, 5)
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
		nil, []string{kbGoldenCollection}, nil, KBSearchKeyword, 5)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	got := kbHitKeys(t, hits)
	if !got["t-alloy"] || !got["t-dash"] {
		t.Fatalf("multi-topic keyword search missed a partial answer: got %v", got)
	}
}

// D-080 (issue #372): provenance-weighted ranking.
const kbProvenanceCollection = "itest-kbprov"

// seedKBProvenancePool opens (and migrates) the shared test pool, and
// registers itest-kbprov% cleanup, without seeding any fixture rows:
// shared by every provenance test below so each can seed its own
// collections.
func seedKBProvenancePool(t *testing.T) *pgpool.Pool {
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
	_, _ = db.Exec(ctx, "DELETE FROM kb_collections WHERE name LIKE 'itest-kbprov%'")
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		_, _ = conn.Exec(cctx, "DELETE FROM kb_collections WHERE name LIKE 'itest-kbprov%'")
	})
	return pool
}

// seedKBProvenanceDoc inserts one collection (if not already present),
// one document at the given provenance tier, and a single chunk.
func seedKBProvenanceDoc(t *testing.T, st *KBStore, collection, docTitle, provenance, content string, emb Vector) string {
	t.Helper()
	ctx := t.Context()
	db, err := st.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var collID string
	err = db.QueryRow(ctx, "SELECT id FROM kb_collections WHERE name = $1", collection).Scan(&collID)
	if err != nil {
		if err := db.QueryRow(ctx, "INSERT INTO kb_collections (name) VALUES ($1) RETURNING id",
			collection).Scan(&collID); err != nil {
			t.Fatalf("insert collection %s: %v", collection, err)
		}
	}
	var docID string
	if err := db.QueryRow(ctx, `INSERT INTO kb_documents (collection_id, title, provenance, status)
		VALUES ($1, $2, $3, 'ready') RETURNING id`, collID, docTitle, provenance).Scan(&docID); err != nil {
		t.Fatalf("insert document %s: %v", docTitle, err)
	}
	if err := st.ReplaceChunks(ctx, docID, []KBChunk{
		{Seq: 0, Content: content, Embedding: emb, EmbeddingModel: "itest"},
	}); err != nil {
		t.Fatalf("ReplaceChunks %s: %v", docTitle, err)
	}
	return docID
}

// seedKBProvenance builds three documents in one collection, one per
// tier, each with a single chunk on the same basis dimension (dim
// 500, so relevance never interferes with tier ordering), plus two
// curated chunks at different relevance to pin within-tier ordering
// stays untouched. The less relevant chunk shares the query axis at
// similarity 0.6 rather than sitting on an orthogonal basis: fully
// orthogonal would fall below the semantic similarity floor (0.25)
// and never return at all.
func seedKBProvenance(t *testing.T) *KBStore {
	t.Helper()
	pool := seedKBProvenancePool(t)
	st := NewKBStore(pool)

	for _, tier := range []string{"curated", "mission", "web"} {
		seedKBProvenanceDoc(t, st, kbProvenanceCollection, tier+"-doc", tier,
			tier+" tier content about widgets", kbBasis(500))
	}
	lessRelevant := make(Vector, 1024)
	lessRelevant[600] = 0.6
	lessRelevant[601] = 0.8
	seedKBProvenanceDoc(t, st, kbProvenanceCollection, "within-tier-doc-a", "curated",
		"more relevant curated chunk", kbBasis(600))
	seedKBProvenanceDoc(t, st, kbProvenanceCollection, "within-tier-doc-b", "curated",
		"less relevant curated chunk", lessRelevant)

	return st
}

// TestKBGoldenProvenanceOrdering pins AC1: with comparable relevance,
// curated ranks above mission, mission above web.
func TestKBGoldenProvenanceOrdering(t *testing.T) {
	st := seedKBProvenance(t)
	hits, err := st.KBSearch(t.Context(), "widgets", kbBasis(500), []string{kbProvenanceCollection}, nil, KBSearchHybrid, 10)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	var order []string
	for _, h := range hits {
		if h.Content == "curated tier content about widgets" ||
			h.Content == "mission tier content about widgets" ||
			h.Content == "web tier content about widgets" {
			order = append(order, strings.Fields(h.Content)[0])
		}
	}
	if len(order) != 3 {
		t.Fatalf("got %d tier hits, want 3 (order %v)", len(order), order)
	}
	if order[0] != "curated" || order[1] != "mission" || order[2] != "web" {
		t.Fatalf("tier order = %v, want [curated mission web]", order)
	}
}

// TestKBGoldenProvenanceWithinTierUnchanged pins AC2: within one
// provenance tier, relevance still decides order (the multiplier is
// identical for both chunks so it can't reorder them).
func TestKBGoldenProvenanceWithinTierUnchanged(t *testing.T) {
	st := seedKBProvenance(t)
	hits, err := st.KBSearch(t.Context(), "widgets", kbBasis(600), []string{kbProvenanceCollection}, nil, KBSearchSemantic, 10)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	var order []string
	for _, h := range hits {
		if h.Content == "more relevant curated chunk" || h.Content == "less relevant curated chunk" {
			order = append(order, h.Content)
		}
	}
	if len(order) != 2 {
		t.Fatalf("got %d within-tier hits, want 2", len(order))
	}
	if order[0] != "more relevant curated chunk" {
		t.Fatalf("within-tier order = %v, want the on-axis chunk first", order)
	}
}

// TestKBGoldenProvenanceComposesWithBoost pins the interplay
// requirement: the collection boost (1.5x) and the provenance weight
// (curated 1.0x, mission 0.8x) both apply post-fusion and compose by
// multiplication, not by one overriding the other. A curated doc in an
// unboosted collection scores base*1.0; the same content as a
// mission-tier doc in a boosted collection scores base*1.5*0.8 = 1.2x
// base: strictly above the curated baseline. That only holds if the
// two multipliers actually multiply together: an override (whichever
// factor "wins") or a component missing from the product would not
// reproduce this exact ratio.
func TestKBGoldenProvenanceComposesWithBoost(t *testing.T) {
	st := NewKBStore(seedKBProvenancePool(t))

	const boostedCollection = "itest-kbprov-boosted"
	const plainCollection = "itest-kbprov-plain"
	seedKBProvenanceDoc(t, st, boostedCollection, "boosted-mission-doc", "mission", "widgets everywhere", kbBasis(700))
	seedKBProvenanceDoc(t, st, plainCollection, "plain-curated-doc", "curated", "widgets everywhere", kbBasis(700))

	hits, err := st.KBSearch(t.Context(), "widgets", kbBasis(700),
		[]string{boostedCollection, plainCollection}, []string{boostedCollection}, KBSearchSemantic, 10)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2 (one per collection): %+v", len(hits), hits)
	}
	var boostedScore, plainScore float64
	for _, h := range hits {
		switch h.Collection {
		case boostedCollection:
			boostedScore = h.Score
		case plainCollection:
			plainScore = h.Score
		}
	}
	if boostedScore == 0 || plainScore == 0 {
		t.Fatalf("missing expected collection in hits: %+v", hits)
	}
	const wantRatio = 1.5 * 0.8 // boostFactorLit * provenanceWeightMissionLit
	gotRatio := boostedScore / plainScore
	if diff := gotRatio - wantRatio; diff > 0.01 || diff < -0.01 {
		t.Fatalf("boosted/plain score ratio = %.4f, want %.4f (boost * provenance weight composed by multiplication)", gotRatio, wantRatio)
	}
}

// D-082 (issue #419): per-document chunk cap for result diversity.
const kbDiversityCollection = "itest-kbdiversity"

// seedKBDiversity builds one dominant document with 5 chunks all on the
// query's basis dimension (so every chunk fuses to a similar high score
// and would otherwise fill top-k alone), plus one chunk each in three
// other documents on the same dimension: enough non-dominant chunks
// (3) that capping the dominant document to kbPerDocumentCap (2) still
// fills k=5 without needing to backfill into the dominant document.
func seedKBDiversity(t *testing.T) *KBStore {
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
	_, _ = db.Exec(ctx, "DELETE FROM kb_collections WHERE name = $1", kbDiversityCollection)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		_, _ = conn.Exec(cctx, "DELETE FROM kb_collections WHERE name = $1", kbDiversityCollection)
	})

	st := NewKBStore(pool)
	var collID string
	if err := db.QueryRow(ctx,
		"INSERT INTO kb_collections (name) VALUES ($1) RETURNING id", kbDiversityCollection).Scan(&collID); err != nil {
		t.Fatalf("insert collection: %v", err)
	}

	// Dominant document: 5 chunks, all on-axis (similarity 1.0).
	var dominantID string
	if err := db.QueryRow(ctx, `INSERT INTO kb_documents (collection_id, title, status)
		VALUES ($1, 'dominant', 'ready') RETURNING id`, collID).Scan(&dominantID); err != nil {
		t.Fatalf("insert dominant document: %v", err)
	}
	dominantChunks := make([]KBChunk, 5)
	for i := range dominantChunks {
		dominantChunks[i] = KBChunk{Seq: i, Content: fmt.Sprintf("dominant chunk %d about widgets", i), Embedding: kbBasis(700), EmbeddingModel: "itest"}
	}
	if err := st.ReplaceChunks(ctx, dominantID, dominantChunks); err != nil {
		t.Fatalf("ReplaceChunks dominant: %v", err)
	}

	// Three other documents, one chunk each, still on-axis so they rank
	// below the dominant document's chunks purely by keyword overlap
	// (fewer shared lexemes) but clear the semantic floor identically.
	for _, title := range []string{"other-a", "other-b", "other-c"} {
		var docID string
		if err := db.QueryRow(ctx, `INSERT INTO kb_documents (collection_id, title, status)
			VALUES ($1, $2, 'ready') RETURNING id`, collID, title).Scan(&docID); err != nil {
			t.Fatalf("insert document %s: %v", title, err)
		}
		if err := st.ReplaceChunks(ctx, docID, []KBChunk{
			{Seq: 0, Content: title + " widgets", Embedding: kbBasis(700), EmbeddingModel: "itest"},
		}); err != nil {
			t.Fatalf("ReplaceChunks %s: %v", title, err)
		}
	}
	return st
}

// TestKBGoldenPerDocumentCapDiversifies pins AC1: a dominant document's
// fused chunks would otherwise fill every slot; the cap limits it to
// kbPerDocumentCap and the freed slots surface the other documents.
func TestKBGoldenPerDocumentCapDiversifies(t *testing.T) {
	st := seedKBDiversity(t)
	hits, err := st.KBSearch(t.Context(), "widgets", kbBasis(700), []string{kbDiversityCollection}, nil, KBSearchHybrid, 5)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	if len(hits) != 5 {
		t.Fatalf("got %d hits, want 5", len(hits))
	}
	docCounts := map[string]int{}
	for _, h := range hits {
		docCounts[h.DocumentTitle]++
	}
	if docCounts["dominant"] != kbPerDocumentCap {
		t.Fatalf("dominant document contributed %d chunks, want %d (cap)", docCounts["dominant"], kbPerDocumentCap)
	}
	if docCounts["other-a"] == 0 || docCounts["other-b"] == 0 || docCounts["other-c"] == 0 {
		t.Fatalf("expected all three other documents to surface via freed slots, got %+v", docCounts)
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Fatalf("hits not score-descending at index %d: %+v", i, hits)
		}
	}
}

// TestKBGoldenPerDocumentCapBackfillsSingleDoc pins AC2: with only one
// matching document, the cap must not starve k below the available hit
// count; slots backfill with that document's own chunks.
func TestKBGoldenPerDocumentCapBackfillsSingleDoc(t *testing.T) {
	st := seedKBDiversity(t)
	hits, err := st.KBSearch(t.Context(), "dominant chunk", kbBasis(700), []string{kbDiversityCollection}, nil, KBSearchKeyword, 5)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	// Keyword mode only matches chunks sharing the "dominant"/"chunk"
	// lexemes, unique to the dominant document's 5 chunks: the cap must
	// still backfill all 5 rather than stopping at kbPerDocumentCap.
	docCounts := map[string]int{}
	for _, h := range hits {
		docCounts[h.DocumentTitle]++
	}
	if docCounts["dominant"] != 5 {
		t.Fatalf("dominant document contributed %d chunks, want 5 (backfilled, single matching doc)", docCounts["dominant"])
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Fatalf("hits not score-descending at index %d: %+v", i, hits)
		}
	}
}

func TestKBGoldenSemanticFloor(t *testing.T) {
	st := seedKBGolden(t)
	// On-axis query hits; orthogonal query (similarity 0) must not.
	hits, err := st.KBSearch(t.Context(), "anything", kbBasis(1), []string{kbGoldenCollection}, nil, KBSearchSemantic, 5)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	if got := kbHitKeys(t, hits); !got["v-loki"] || len(hits) != 1 {
		t.Fatalf("semantic on-axis: got %d hits %v, want exactly v-loki", len(hits), got)
	}
	hits, err = st.KBSearch(t.Context(), "anything", kbBasis(999), []string{kbGoldenCollection}, nil, KBSearchSemantic, 5)
	if err != nil {
		t.Fatalf("KBSearch: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("semantic orthogonal: got %d hits, want 0 (similarity floor)", len(hits))
	}
}
