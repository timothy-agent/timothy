//go:build integration

package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const usageMarker = "itest-usage-"

// testUsageAPI wires a usageAPI over a real Postgres so the new
// /totals handler is exercised through the exact same code path as
// production, mirroring aggregate_integration_test.go's DB harness.
func testUsageAPI(t *testing.T) (*usageAPI, *ledger.Ledger) {
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
	_, _ = db.Exec(ctx, "DELETE FROM cost_ledger WHERE provider LIKE $1 || '%'", usageMarker)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pool.Get()
		if err != nil {
			return
		}
		_, _ = conn.Exec(cctx, "DELETE FROM cost_ledger WHERE provider LIKE $1 || '%'", usageMarker)
	})

	return &usageAPI{agg: ledger.NewAggregator(pool), budgets: ledger.NewBudgetStore(pool)},
		ledger.New(pool, log, nil)
}

func usd(v float64) *float64 { return &v }

func TestHandleTotalsGroupsWholeRangeAsJSON(t *testing.T) {
	u, led := testUsageAPI(t)
	ctx := t.Context()
	from := time.Now().UTC().Add(-time.Hour)

	led.Record(ctx, ledger.Entry{
		Provider: usageMarker + "p1", Model: "m1", Route: "coding",
		Usage:     &stream.Usage{InputTokens: 100, OutputTokens: 50},
		LatencyMS: 10, Status: "ok", Cost: usd(0.05),
	})
	led.Record(ctx, ledger.Entry{
		Provider: usageMarker + "p1", Model: "m1", Route: "coding",
		Usage:     &stream.Usage{InputTokens: 40, OutputTokens: 10},
		LatencyMS: 10, Status: "ok", Cost: usd(0.02),
	})
	// A test-connection probe: must be excluded from the totals, same
	// as every other aggregate on this ledger.
	led.Record(ctx, ledger.Entry{
		Provider: usageMarker + "p1", Model: "m1", Route: "coding", Purpose: "test",
		Usage:     &stream.Usage{InputTokens: 999999, OutputTokens: 999999},
		LatencyMS: 10, Status: "ok", Cost: usd(99.0),
	})

	to := time.Now().UTC().Add(time.Hour)
	url := "/internal/admin/usage/totals?from=" + from.Format(time.RFC3339) +
		"&to=" + to.Format(time.RFC3339) + "&group=provider"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	u.handleTotals(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var out struct {
		Totals []ledger.GroupTotal `json:"totals"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got *ledger.GroupTotal
	for i := range out.Totals {
		if out.Totals[i].Group == usageMarker+"p1" {
			got = &out.Totals[i]
		}
	}
	if got == nil {
		t.Fatalf("provider %q missing from totals: %+v", usageMarker+"p1", out.Totals)
	}
	if got.Requests != 2 || got.Cost != 0.07 || got.InputTokens != 140 || got.OutputTokens != 60 {
		t.Fatalf("totals[p1] = %+v, want 2 req / $0.07 / 140 in / 60 out (test probe excluded)", got)
	}
}

// TestHandleUnpricedGroupsByProviderModel covers the new /usage/unpriced
// handler: unpriced rows (cost NULL) grouped by (provider, model), a
// priced row and a test-purpose row both excluded.
func TestHandleUnpricedGroupsByProviderModel(t *testing.T) {
	u, led := testUsageAPI(t)
	ctx := t.Context()
	from := time.Now().UTC().Add(-time.Hour)

	led.Record(ctx, ledger.Entry{
		Provider: usageMarker + "p1", Model: "m1",
		Usage: &stream.Usage{InputTokens: 100, OutputTokens: 50}, Status: "ok",
	})
	led.Record(ctx, ledger.Entry{
		Provider: usageMarker + "p1", Model: "m1",
		Usage: &stream.Usage{InputTokens: 999, OutputTokens: 999}, Status: "ok", Cost: usd(1.0),
	})
	led.Record(ctx, ledger.Entry{
		Provider: usageMarker + "p1", Model: "m1", Purpose: "test",
		Usage: &stream.Usage{InputTokens: 999999, OutputTokens: 999999}, Status: "ok",
	})

	to := time.Now().UTC().Add(time.Hour)
	url := "/internal/admin/usage/unpriced?from=" + from.Format(time.RFC3339) +
		"&to=" + to.Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	u.handleUnpriced(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var out struct {
		Groups []ledger.UnpricedGroup `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got *ledger.UnpricedGroup
	for i := range out.Groups {
		if out.Groups[i].Provider == usageMarker+"p1" && out.Groups[i].Model == "m1" {
			got = &out.Groups[i]
		}
	}
	if got == nil {
		t.Fatalf("provider/model %q/%q missing from groups: %+v", usageMarker+"p1", "m1", out.Groups)
	}
	if got.UnpricedInputTokens != 100 || got.UnpricedOutputTokens != 50 {
		t.Fatalf("groups[p1/m1] = %+v, want 100 in / 50 out (priced and test rows excluded)", got)
	}
}

func TestHandleTotalsRejectsUnknownGroup(t *testing.T) {
	u, _ := testUsageAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/internal/admin/usage/totals?group=nope", nil)
	w := httptest.NewRecorder()
	u.handleTotals(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unknown group", w.Code)
	}
}
