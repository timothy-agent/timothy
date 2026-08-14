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
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/extract"
	"github.com/SumonMSelim/timothy/internal/memory/retrieval"
	"github.com/SumonMSelim/timothy/internal/memory/store"
)

type fakeExtractor struct {
	ids  []string
	err  error
	last extract.Request
}

func (f *fakeExtractor) Extract(_ context.Context, req extract.Request) ([]string, error) {
	f.last = req
	return f.ids, f.err
}

func testAPI(ext Extractor) *API {
	return &API{ext: ext, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func post(t *testing.T, a *API, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/extract", strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.handleExtract(rec, req)
	return rec
}

func TestExtractReturnsIDs(t *testing.T) {
	t.Parallel()
	ext := &fakeExtractor{ids: []string{"id-1", "id-2"}}
	rec := post(t, testAPI(ext), `{"session_id":"s1","source_seq":9,"text":"turn text"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var out struct {
		MemoryIDs []string `json:"memory_ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.MemoryIDs) != 2 {
		t.Fatalf("ids = %v, want 2", out.MemoryIDs)
	}
	if ext.last.SessionID != "s1" || ext.last.SourceSeq != 9 {
		t.Fatalf("request not passed through: %+v", ext.last)
	}
}

func TestExtractEmptyResultIsEmptyArray(t *testing.T) {
	t.Parallel()
	rec := post(t, testAPI(&fakeExtractor{}), `{"text":"nothing memorable"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"memory_ids":[]`) {
		t.Fatalf("body = %s, want empty array not null", rec.Body)
	}
}

func TestExtractValidation(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"empty text": `{"session_id":"s1","text":"  "}`,
		"bad json":   `{`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec := post(t, testAPI(&fakeExtractor{}), body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestExtractFailureIs502(t *testing.T) {
	t.Parallel()
	rec := post(t, testAPI(&fakeExtractor{err: errors.New("llm down")}), `{"text":"x"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "extraction_failed") {
		t.Fatalf("body = %s", rec.Body)
	}
}

type fakeSearcher struct {
	cands      map[string]*retrieval.Candidate
	sawEmb     store.Vector
	sawTypes   []store.MemoryType
	marked     []string
	failSearch error
}

func (f *fakeSearcher) Search(_ context.Context, _ string, emb store.Vector, types []store.MemoryType) (map[string]*retrieval.Candidate, error) {
	f.sawEmb, f.sawTypes = emb, types
	return f.cands, f.failSearch
}

func (f *fakeSearcher) MarkRetrieved(_ context.Context, ids []string) { f.marked = ids }

type fakeEmbedder struct{ err error }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string, _ string) ([][]float32, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0}
	}
	return out, "fake-embed", nil
}

func postRetrieve(t *testing.T, a *API, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/retrieve", strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.handleRetrieve(rec, req)
	return rec
}

func retrieveAPI(s Searcher, e Embedder) *API {
	return &API{search: s, embed: e, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestRetrieveReturnsPackedMemories(t *testing.T) {
	t.Parallel()
	s := &fakeSearcher{cands: map[string]*retrieval.Candidate{
		"m1": retrieval.NewCandidate("m1", store.TypeSemantic, "user lives in Porto", time.Now(), map[string]int{"vector": 1, "text": 1}),
	}}
	rec := postRetrieve(t, retrieveAPI(s, &fakeEmbedder{}), `{"query":"where does the user live?"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var out struct {
		Memories []retrievedMemory `json:"memories"`
		Tokens   int               `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Memories) != 1 || out.Memories[0].ID != "m1" || out.Tokens <= 0 {
		t.Fatalf("out = %+v", out)
	}
	if len(s.marked) != 1 || s.marked[0] != "m1" {
		t.Fatalf("marked = %v, want [m1]", s.marked)
	}
	if len(s.sawEmb) == 0 {
		t.Fatal("query embedding not passed to search")
	}
}

func TestRetrieveDegradesWithoutEmbedding(t *testing.T) {
	t.Parallel()
	s := &fakeSearcher{cands: map[string]*retrieval.Candidate{}}
	rec := postRetrieve(t, retrieveAPI(s, &fakeEmbedder{err: errors.New("no embedding route")}), `{"query":"anything"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (degraded)", rec.Code)
	}
	if s.sawEmb != nil {
		t.Fatal("embedding should be nil when embed fails")
	}
}

func TestRetrieveValidatesQuery(t *testing.T) {
	t.Parallel()
	rec := postRetrieve(t, retrieveAPI(&fakeSearcher{}, &fakeEmbedder{}), `{"query":" "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRetrievePassesTypesFilter(t *testing.T) {
	t.Parallel()
	s := &fakeSearcher{cands: map[string]*retrieval.Candidate{}}
	rec := postRetrieve(t, retrieveAPI(s, &fakeEmbedder{}), `{"query":"x","types":["semantic","procedural"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(s.sawTypes) != 2 || s.sawTypes[0] != store.TypeSemantic {
		t.Fatalf("types = %v", s.sawTypes)
	}
}

func TestRetrieveSearchFailureIs500(t *testing.T) {
	t.Parallel()
	s := &fakeSearcher{failSearch: errors.New("db down")}
	rec := postRetrieve(t, retrieveAPI(s, &fakeEmbedder{}), `{"query":"x"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
