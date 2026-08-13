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
	replacedDocID string
	replacedCount int
	replaceErr    error

	searchHits []store.KBSearchHit
	searchErr  error
	sawQuery   string
	sawNames   []string
	sawMode    store.KBSearchMode
	sawK       int
	sawEmb     store.Vector
}

func (f *fakeKB) ReplaceChunks(_ context.Context, documentID string, chunks []store.KBChunk) error {
	f.replacedDocID = documentID
	f.replacedCount = len(chunks)
	return f.replaceErr
}

func (f *fakeKB) KBSearch(_ context.Context, query string, embedding store.Vector, names []string, mode store.KBSearchMode, k int) ([]store.KBSearchHit, error) {
	f.sawQuery, f.sawEmb, f.sawNames, f.sawMode, f.sawK = query, embedding, names, mode, k
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

func TestKBSearchRequiresCollectionNames(t *testing.T) {
	t.Parallel()
	a := kbAPIFor(&fakeKB{}, nil, &fakeEmbedder{})
	rec := postJSON(t, a.handleKBSearch, "/v1/kb-search", `{"query":"x","collection_names":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
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
