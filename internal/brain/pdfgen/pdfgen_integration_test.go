//go:build integration

package pdfgen

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pdfgen"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

func integrationService(t *testing.T, renderCalls *atomic.Int32) (*Service, string) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		renderCalls.Add(1)
		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("%PDF-1.4 fake"))
	}))
	t.Cleanup(srv.Close)

	client := pdfgen.New(srv.URL)
	dir := t.TempDir()
	att := attachments.New(dir, pool)
	return New(client, pool, att), dir
}

func TestServiceRenderCacheMissThenHit(t *testing.T) {
	var calls atomic.Int32
	s, _ := integrationService(t, &calls)

	docs := []pdfgen.Document{{Title: "Report", Content: "# unique-" + t.Name()}}
	opts := pdfgen.Options{TOC: true}

	first, err := s.Render(t.Context(), docs, opts)
	if err != nil {
		t.Fatalf("Render (miss): %v", err)
	}
	if first.Cached {
		t.Fatal("first render reported Cached, want a fresh miss")
	}
	if calls.Load() != 1 {
		t.Fatalf("sidecar calls = %d, want 1", calls.Load())
	}

	second, err := s.Render(t.Context(), docs, opts)
	if err != nil {
		t.Fatalf("Render (hit): %v", err)
	}
	if !second.Cached {
		t.Fatal("second render did not report Cached")
	}
	if second.AttachmentID != first.AttachmentID {
		t.Fatalf("AttachmentID = %q, want %q (cache hit)", second.AttachmentID, first.AttachmentID)
	}
	if calls.Load() != 1 {
		t.Fatalf("sidecar calls = %d after cache hit, want still 1 (no second render)", calls.Load())
	}
}

func TestServiceRenderRegeneratesWhenAttachmentMissing(t *testing.T) {
	var calls atomic.Int32
	s, _ := integrationService(t, &calls)

	docs := []pdfgen.Document{{Title: "Report", Content: "# unique-" + t.Name()}}
	opts := pdfgen.Options{}

	first, err := s.Render(t.Context(), docs, opts)
	if err != nil {
		t.Fatalf("Render (miss): %v", err)
	}

	// Simulate the attachment row/file disappearing from under the
	// cache row: pdf_renders still points at an id that Open can no
	// longer find, so the next call must treat it as a miss.
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := db.Exec(t.Context(), "DELETE FROM attachments WHERE id = $1", first.AttachmentID); err != nil {
		t.Fatalf("delete attachment row: %v", err)
	}

	second, err := s.Render(t.Context(), docs, opts)
	if err != nil {
		t.Fatalf("Render (regenerate): %v", err)
	}
	if second.Cached {
		t.Fatal("render with a dangling cache row reported Cached, want regeneration")
	}
	if calls.Load() != 2 {
		t.Fatalf("sidecar calls = %d, want 2 (miss, then regenerate)", calls.Load())
	}
}

func TestServiceRenderRegeneratesWhenAttachmentFileGone(t *testing.T) {
	var calls atomic.Int32
	s, dir := integrationService(t, &calls)

	docs := []pdfgen.Document{{Title: "Report", Content: "# unique-" + t.Name()}}
	opts := pdfgen.Options{}

	first, err := s.Render(t.Context(), docs, opts)
	if err != nil {
		t.Fatalf("Render (miss): %v", err)
	}

	// Metadata row survives but the file on disk is gone (cleanup, a
	// moved attachments dir): Open fails with fs.ErrNotExist and the
	// next call must regenerate, rewriting the file in place.
	if err := os.Remove(filepath.Join(dir, first.AttachmentID+".pdf")); err != nil {
		t.Fatalf("remove attachment file: %v", err)
	}

	second, err := s.Render(t.Context(), docs, opts)
	if err != nil {
		t.Fatalf("Render (regenerate): %v", err)
	}
	if second.Cached {
		t.Fatal("render with the file missing reported Cached, want regeneration")
	}
	if calls.Load() != 2 {
		t.Fatalf("sidecar calls = %d, want 2 (miss, then regenerate)", calls.Load())
	}

	// The regeneration must have healed the file: a third call is a
	// plain cache hit again.
	third, err := s.Render(t.Context(), docs, opts)
	if err != nil {
		t.Fatalf("Render (healed hit): %v", err)
	}
	if !third.Cached {
		t.Fatal("render after regeneration did not report Cached")
	}
	if calls.Load() != 2 {
		t.Fatalf("sidecar calls = %d after healed hit, want still 2", calls.Load())
	}
}
