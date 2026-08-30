package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/pdfgen"
	pdfgenclient "github.com/SumonMSelim/timothy/internal/platform/pdfgen"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

func TestExportMessagePDFNotEnabled(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)

	w := do(a, a.handleExportMessagePDF, "POST", "/v1/chat/export-pdf", "Bearer tok", `{"title":"Reply","content":"# hi"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", w.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "not_enabled" {
		t.Fatalf("error = %q, want not_enabled", body.Error)
	}
}

func TestExportMessagePDFValidation(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	// A real (non-nil) Service is required so the handler's own content
	// validation runs before the not_enabled short-circuit; it's never
	// actually called against the sidecar since every case here is
	// rejected before Render.
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	a.pdfService = pdfgen.New(pdfgenclient.New("http://invalid"), pool, nil)

	t.Run("bad json", func(t *testing.T) {
		w := do(a, a.handleExportMessagePDF, "POST", "/v1/chat/export-pdf", "Bearer tok", `{`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400", w.Code)
		}
	})

	t.Run("empty content", func(t *testing.T) {
		w := do(a, a.handleExportMessagePDF, "POST", "/v1/chat/export-pdf", "Bearer tok", `{"title":"Reply"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400", w.Code)
		}
	})

	t.Run("oversize content", func(t *testing.T) {
		body := `{"content":"` + strings.Repeat("x", maxExportMarkdownBytes+1) + `"}`
		w := do(a, a.handleExportMessagePDF, "POST", "/v1/chat/export-pdf", "Bearer tok", body)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("code = %d, want 413", w.Code)
		}
	})
}
