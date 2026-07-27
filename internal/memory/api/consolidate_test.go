package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SumonMSelim/timothy/internal/memory/extract"
)

// fakeConsolidateRunner scripts one Run outcome.
type fakeConsolidateRunner struct {
	summary extract.Summary
	err     error
}

func (f *fakeConsolidateRunner) Run(context.Context) (extract.Summary, error) {
	return f.summary, f.err
}

func consolidateReq(t *testing.T, runner ConsolidateRunner) *httptest.ResponseRecorder {
	t.Helper()
	a := &API{consolidate: runner, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodPost, "/v1/consolidate", nil)
	rec := httptest.NewRecorder()
	a.handleConsolidate(rec, req)
	return rec
}

func TestConsolidateReturnsSummary(t *testing.T) {
	t.Parallel()
	runner := &fakeConsolidateRunner{summary: extract.Summary{Merged: 2, Rejected: 1, Archived: 3, Decayed: 4}}
	rec := consolidateReq(t, runner)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var out struct {
		Merged   int    `json:"merged"`
		Rejected int    `json:"rejected"`
		Archived int    `json:"archived"`
		Decayed  int    `json:"decayed"`
		Errors   string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Merged != 2 || out.Rejected != 1 || out.Archived != 3 || out.Decayed != 4 {
		t.Fatalf("summary = %+v, want 2/1/3/4", out)
	}
	if out.Errors != "" {
		t.Fatalf("errors = %q, want empty on success", out.Errors)
	}
}

func TestConsolidateSurfacesPartialError(t *testing.T) {
	t.Parallel()
	runner := &fakeConsolidateRunner{
		summary: extract.Summary{Merged: 1},
		err:     errors.New("consolidate: near-dup pairs: boom"),
	}
	rec := consolidateReq(t, runner)
	// Stages are independent — a stage failure still returns 200 with
	// whatever counts did complete, plus the errors field.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var out struct {
		Merged int    `json:"merged"`
		Errors string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Merged != 1 {
		t.Fatalf("merged = %d, want 1 (partial counts preserved)", out.Merged)
	}
	if out.Errors == "" {
		t.Fatal("errors field empty, want the Run error surfaced")
	}
}
