package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/memory/store"
)

type fakeKB struct {
	replacedDocID  string
	replacedCount  int
	replacedChunks []store.KBChunk
	replaceErr     error

	searchHits []store.KBSearchHit
	searchErr  error
	sawQuery   string
	sawNames   []string
	sawBoost   []string
	sawMode    store.KBSearchMode
	sawK       int
	sawEmb     store.Vector
}

func (f *fakeKB) ReplaceChunks(_ context.Context, documentID string, chunks []store.KBChunk) error {
	f.replacedDocID = documentID
	f.replacedCount = len(chunks)
	f.replacedChunks = chunks
	return f.replaceErr
}

func (f *fakeKB) KBSearch(_ context.Context, query string, embedding store.Vector, names, boost []string, mode store.KBSearchMode, k int) ([]store.KBSearchHit, error) {
	f.sawQuery, f.sawEmb, f.sawNames, f.sawBoost, f.sawMode, f.sawK = query, embedding, names, boost, mode, k
	return f.searchHits, f.searchErr
}

type fakeDocStatus struct {
	ingestedID    string
	ingestedCount int
	failedID      string
	failedMsg     string
}

func (f *fakeDocStatus) SetIngested(_ context.Context, documentID string, chunkCount int) error {
	f.ingestedID, f.ingestedCount = documentID, chunkCount
	return nil
}

func (f *fakeDocStatus) SetFailed(_ context.Context, documentID string, errMsg string) error {
	f.failedID, f.failedMsg = documentID, errMsg
	return nil
}

func kbAPIFor(kb KBManager, docs DocumentStatusSetter, embed Embedder) *API {
	return &API{kb: kb, kbDocs: docs, embed: embed, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func postJSON(t *testing.T, handler http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestIngestChunksEmbedsAndStores(t *testing.T) {
	t.Parallel()
	kb := &fakeKB{}
	docs := &fakeDocStatus{}
	a := kbAPIFor(kb, docs, &fakeEmbedder{})
	body := `{"document_id":"doc-1","title":"Doc","markdown":"## Section\nsome content here"}`
	rec := postJSON(t, a.handleIngest, "/v1/ingest-document", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if kb.replacedDocID != "doc-1" || kb.replacedCount == 0 {
		t.Fatalf("chunks not stored: doc=%s count=%d", kb.replacedDocID, kb.replacedCount)
	}
	if docs.ingestedID != "doc-1" || docs.ingestedCount != kb.replacedCount {
		t.Fatalf("status not reported ingested: %+v", docs)
	}
}

// batchSizeEmbedder records how many texts each Embed call carried.
type batchSizeEmbedder struct {
	fakeEmbedder
	batches []int
}

func (b *batchSizeEmbedder) Embed(ctx context.Context, texts []string, purpose string) ([][]float32, string, error) {
	b.batches = append(b.batches, len(texts))
	return b.fakeEmbedder.Embed(ctx, texts, purpose)
}

func TestIngestBatchesEmbeddingsAndRecordsModel(t *testing.T) {
	t.Parallel()
	// Enough sections to force multiple embed batches.
	var md strings.Builder
	for i := range kbEmbedBatchSize + 5 {
		md.WriteString("## Section ")
		md.WriteString(strings.Repeat("x", i+1))
		md.WriteString("\ncontent for this section\n\n")
	}
	body, err := json.Marshal(map[string]string{"document_id": "doc-b", "title": "Doc", "markdown": md.String()})
	if err != nil {
		t.Fatal(err)
	}

	kb := &fakeKB{}
	embed := &batchSizeEmbedder{}
	a := kbAPIFor(kb, &fakeDocStatus{}, embed)
	rec := postJSON(t, a.handleIngest, "/v1/ingest-document", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(embed.batches) < 2 {
		t.Fatalf("embed batches = %v, want the chunk set split across calls", embed.batches)
	}
	for _, n := range embed.batches {
		if n > kbEmbedBatchSize {
			t.Fatalf("batch of %d exceeds the %d cap", n, kbEmbedBatchSize)
		}
	}
	total := 0
	for _, n := range embed.batches {
		total += n
	}
	if total != kb.replacedCount {
		t.Fatalf("embedded %d texts for %d stored chunks", total, kb.replacedCount)
	}
	for _, c := range kb.replacedChunks {
		if c.EmbeddingModel != "fake-embed" {
			t.Fatalf("embedding_model = %q, want the model the embedder reported", c.EmbeddingModel)
		}
	}
}

func TestIngestValidation(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"missing document_id": `{"markdown":"x"}`,
		"missing markdown":    `{"document_id":"d1"}`,
		"bad json":            `{`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := kbAPIFor(&fakeKB{}, &fakeDocStatus{}, &fakeEmbedder{})
			rec := postJSON(t, a.handleIngest, "/v1/ingest-document", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestIngestEmbeddingFailureReportsFailedStatus(t *testing.T) {
	t.Parallel()
	docs := &fakeDocStatus{}
	a := kbAPIFor(&fakeKB{}, docs, &fakeEmbedder{err: errors.New("gateway down")})
	rec := postJSON(t, a.handleIngest, "/v1/ingest-document", `{"document_id":"doc-2","markdown":"content"}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if docs.failedID != "doc-2" {
		t.Fatalf("failure not reported: %+v", docs)
	}
}

func TestIngestStoreFailureReportsFailedStatus(t *testing.T) {
	t.Parallel()
	kb := &fakeKB{replaceErr: errors.New("db down")}
	docs := &fakeDocStatus{}
	a := kbAPIFor(kb, docs, &fakeEmbedder{})
	rec := postJSON(t, a.handleIngest, "/v1/ingest-document", `{"document_id":"doc-3","markdown":"content"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if docs.failedID != "doc-3" {
		t.Fatalf("failure not reported: %+v", docs)
	}
}

func TestKBSearchDefaultsToHybridAndEmbeds(t *testing.T) {
	t.Parallel()
	kb := &fakeKB{searchHits: []store.KBSearchHit{{ChunkID: "c1", Score: 0.9}}}
	a := kbAPIFor(kb, nil, &fakeEmbedder{})
	rec := postJSON(t, a.handleKBSearch, "/v1/kb-search", `{"query":"what is X","collection_names":["docs"]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if kb.sawMode != store.KBSearchHybrid {
		t.Fatalf("mode = %s, want hybrid", kb.sawMode)
	}
	if len(kb.sawEmb) == 0 {
		t.Fatal("hybrid mode must embed the query")
	}
	if kb.sawK != kbSearchDefaultK {
		t.Fatalf("k = %d, want default %d", kb.sawK, kbSearchDefaultK)
	}
	var out struct {
		Results []kbSearchHitJSON `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Results) != 1 || out.Results[0].ChunkID != "c1" {
		t.Fatalf("results = %+v", out.Results)
	}
}

func TestKBSearchKeywordModeSkipsEmbedding(t *testing.T) {
	t.Parallel()
	kb := &fakeKB{}
	embed := &fakeEmbedder{}
	a := kbAPIFor(kb, nil, embed)
	rec := postJSON(t, a.handleKBSearch, "/v1/kb-search", `{"query":"x","collection_names":["docs"],"mode":"keyword"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if kb.sawMode != store.KBSearchKeyword {
		t.Fatalf("mode = %s, want keyword", kb.sawMode)
	}
	if kb.sawEmb != nil {
		t.Fatal("keyword mode must not embed the query")
	}
}

// TestKBSearchEmptyCollectionNamesSearchesWholeKB pins issue #368's
// default: collection_names is optional, and an empty/omitted list
// reaches the store as nil (whole-KB), not a 400.
func TestKBSearchEmptyCollectionNamesSearchesWholeKB(t *testing.T) {
	t.Parallel()
	kb := &fakeKB{}
	a := kbAPIFor(kb, nil, &fakeEmbedder{})
	rec := postJSON(t, a.handleKBSearch, "/v1/kb-search", `{"query":"x","collection_names":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(kb.sawNames) != 0 {
		t.Fatalf("sawNames = %v, want empty (whole-KB)", kb.sawNames)
	}
}

// TestKBSearchPassesBoostCollectionsThrough pins that boost_collections
// travels to the store call unchanged, separate from collection_names.
func TestKBSearchPassesBoostCollectionsThrough(t *testing.T) {
	t.Parallel()
	kb := &fakeKB{}
	a := kbAPIFor(kb, nil, &fakeEmbedder{})
	rec := postJSON(t, a.handleKBSearch, "/v1/kb-search", `{"query":"x","boost_collections":["docs"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(kb.sawBoost) != 1 || kb.sawBoost[0] != "docs" {
		t.Fatalf("sawBoost = %v, want [docs]", kb.sawBoost)
	}
}

func TestKBSearchKCapsAtMax(t *testing.T) {
	t.Parallel()
	kb := &fakeKB{}
	a := kbAPIFor(kb, nil, &fakeEmbedder{})
	rec := postJSON(t, a.handleKBSearch, "/v1/kb-search", `{"query":"x","collection_names":["docs"],"k":50}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if kb.sawK != kbSearchMaxK {
		t.Fatalf("k = %d, want capped at %d", kb.sawK, kbSearchMaxK)
	}
}

func TestKBSearchSemanticEmbeddingFailureIs502(t *testing.T) {
	t.Parallel()
	a := kbAPIFor(&fakeKB{}, nil, &fakeEmbedder{err: errors.New("no route")})
	rec := postJSON(t, a.handleKBSearch, "/v1/kb-search", `{"query":"x","collection_names":["docs"],"mode":"semantic"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestKBSearchHybridDegradesToKeywordOnEmbedFailure(t *testing.T) {
	t.Parallel()
	kb := &fakeKB{}
	a := kbAPIFor(kb, nil, &fakeEmbedder{err: errors.New("no route")})
	rec := postJSON(t, a.handleKBSearch, "/v1/kb-search", `{"query":"x","collection_names":["docs"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (degraded)", rec.Code)
	}
	if kb.sawMode != store.KBSearchKeyword {
		t.Fatalf("mode = %s, want degraded to keyword", kb.sawMode)
	}
}
