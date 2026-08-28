//go:build integration

package attachments

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

// pngBytes is a minimal valid 1x1 PNG — enough for http.DetectContentType
// to sniff "image/png".
var pngBytes = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func integrationStore(t *testing.T) *Store {
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
	return New(t.TempDir(), pool)
}

// pdfBytes is a minimal PDF header — enough for http.DetectContentType
// to sniff "application/pdf" (it only inspects the leading bytes).
var pdfBytes = []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<< /Type /Catalog >>\nendobj\n")

func TestStoreSavePDF(t *testing.T) {
	s := integrationStore(t)

	att, err := s.Save(t.Context(), bytes.NewReader(pdfBytes))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if att.Mime != "application/pdf" {
		t.Fatalf("Mime = %q, want application/pdf", att.Mime)
	}
	if _, err := os.Stat(filepath.Join(s.dir, att.ID+".pdf")); err != nil {
		t.Fatalf("expected %s.pdf on disk: %v", att.ID, err)
	}
}

func TestStoreSaveText(t *testing.T) {
	s := integrationStore(t)

	// http.DetectContentType returns "text/plain; charset=utf-8" for
	// both .txt and .md content — normalized to the bare "text/plain".
	att, err := s.Save(t.Context(), bytes.NewReader([]byte("# Notes\n\nplain text content")))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if att.Mime != "text/plain" {
		t.Fatalf("Mime = %q, want text/plain", att.Mime)
	}
	if _, err := os.Stat(filepath.Join(s.dir, att.ID+".txt")); err != nil {
		t.Fatalf("expected %s.txt on disk: %v", att.ID, err)
	}

	got, err := s.Get(t.Context(), att.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Mime != "text/plain" {
		t.Fatalf("stored Mime = %q, want text/plain (parameters stripped)", got.Mime)
	}
}

func TestStoreSaveDedup(t *testing.T) {
	s := integrationStore(t)

	first, err := s.Save(t.Context(), bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if first.Mime != "image/png" {
		t.Fatalf("Mime = %q, want image/png", first.Mime)
	}
	if first.SizeBytes != int64(len(pngBytes)) {
		t.Fatalf("SizeBytes = %d, want %d", first.SizeBytes, len(pngBytes))
	}

	second, err := s.Save(t.Context(), bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("same bytes produced different ids: %q vs %q", first.ID, second.ID)
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d files on disk, want exactly 1 (content-addressed dedup)", len(entries))
	}
}

func TestStoreSaveRejectsFakeExtension(t *testing.T) {
	s := integrationStore(t)

	// A .png-looking upload that is actually a zip must be rejected by
	// magic-byte sniffing, not the (absent, since we don't trust it)
	// client-declared type. Plain text now sniffs to the allowed
	// text/plain, so this uses zip's magic bytes instead to stay
	// genuinely unsupported.
	zipBytes := []byte{0x50, 0x4b, 0x03, 0x04, 0x00, 0x00, 0x00, 0x00}
	_, err := s.Save(t.Context(), bytes.NewReader(zipBytes))
	if !errors.Is(err, ErrUnsupportedMIME) {
		t.Fatalf("Save err = %v, want ErrUnsupportedMIME", err)
	}
}

func TestStoreOpenMissingID(t *testing.T) {
	s := integrationStore(t)

	_, _, err := s.Open(t.Context(), "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open err = %v, want ErrNotFound", err)
	}
}

func TestStoreGetMissingID(t *testing.T) {
	s := integrationStore(t)

	_, err := s.Get(t.Context(), "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get err = %v, want ErrNotFound", err)
	}
}

func TestStoreSaveRoundTripBytes(t *testing.T) {
	s := integrationStore(t)

	att, err := s.Save(t.Context(), bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	r, got, err := s.Open(t.Context(), att.ID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(raw, pngBytes) {
		t.Fatal("round-tripped bytes do not match original upload")
	}
	if got.ID != att.ID || got.Mime != att.Mime {
		t.Fatalf("Open metadata = %+v, want %+v", got, att)
	}
}

func TestStoreSaveSizeCap(t *testing.T) {
	s := integrationStore(t)

	// The store itself does not cap size (that's http.MaxBytesReader at
	// the handler); this test exercises a large-but-legal payload to
	// confirm streaming hashing works past a single buffer's worth of
	// data, using a size at MaxSizeBytes as the boundary the handler
	// enforces.
	big := bytes.Repeat([]byte{0}, 1<<20) // 1MB filler, well under the cap
	body := append(append([]byte{}, pngBytes...), big...)
	// Corrupts the PNG (sniffing only reads the header, still valid),
	// but proves large uploads stream through Save without truncation.
	att, err := s.Save(t.Context(), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Save large payload: %v", err)
	}
	if att.SizeBytes != int64(len(body)) {
		t.Fatalf("SizeBytes = %d, want %d", att.SizeBytes, len(body))
	}
	if att.SizeBytes > MaxSizeBytes {
		t.Fatalf("test payload %d exceeds MaxSizeBytes %d; fix the test", att.SizeBytes, MaxSizeBytes)
	}
}
