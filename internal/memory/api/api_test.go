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

	"github.com/SumonMSelim/timothy/internal/memory/extract"
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
