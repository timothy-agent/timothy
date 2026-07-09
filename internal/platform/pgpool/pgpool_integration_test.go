//go:build integration

package pgpool

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestConnectsToRealDatabase(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}

	p := New(t.Context(), dsn, discard())

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := p.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}

	db, err := p.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var one int
	if err := db.QueryRow(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 = %d, %v", one, err)
	}

	status, detail := p.Status()
	if status != "ok" || detail != "" {
		t.Fatalf("Status() = %q, %q; want ok", status, detail)
	}
}
