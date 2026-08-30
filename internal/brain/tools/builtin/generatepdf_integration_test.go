//go:build integration

package builtin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/pdfgen"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	pdfgenclient "github.com/SumonMSelim/timothy/internal/platform/pdfgen"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

func integrationPDFService(t *testing.T) *pdfgen.Service {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	poolCtx, poolCancel := context.WithCancel(context.Background())
	t.Cleanup(poolCancel)
	pool := pgpool.New(poolCtx, dsn, log)
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
		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("%PDF-1.4 fake"))
	}))
	t.Cleanup(srv.Close)

	client := pdfgenclient.New(srv.URL)
	att := attachments.New(t.TempDir(), pool)
	return pdfgen.New(client, pool, att)
}

func TestGeneratePDFEmitsMediaRef(t *testing.T) {
	svc := integrationPDFService(t)
	tool := GeneratePDF(svc)

	collector := tools.NewCollector(func(_ context.Context, r io.Reader) (string, string, error) {
		data, err := io.ReadAll(r)
		if err != nil {
			return "", "", err
		}
		return string(data), "application/pdf", nil
	})
	ctx := tools.WithCollector(context.Background(), collector)

	args, _ := json.Marshal(generatePDFArgs{
		Documents: []generatePDFDocument{{Title: "Chapter 1", Content: "# unique-" + t.Name()}},
	})
	out, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Chapter 1") {
		t.Fatalf("out = %q, want it to name the document", out)
	}

	refs := collector.Drain()
	if len(refs) != 1 {
		t.Fatalf("refs = %d, want 1", len(refs))
	}
	if refs[0].Name != "Chapter 1.pdf" {
		t.Fatalf("ref name = %q, want %q", refs[0].Name, "Chapter 1.pdf")
	}
}

func TestGeneratePDFNoCollectorConfigured(t *testing.T) {
	svc := integrationPDFService(t)
	tool := GeneratePDF(svc)

	args, _ := json.Marshal(generatePDFArgs{
		Documents: []generatePDFDocument{{Title: "A", Content: "# unique-" + t.Name()}},
	})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("missing collector accepted")
	}
}
