//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/platform/metrics"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const decideMarker = "itest-decide-"

// TestDecideTypedOptionsEndToEnd posts typed options over HTTP to the
// decide handler, served by the llm fallback on a chat stub, with the
// real Postgres ledger. The chat model sees one "id: description" line
// per option, the reply's id with colons comes back exactly, and the
// attempt is booked under the returned ledger id.
func TestDecideTypedOptionsEndToEnd(t *testing.T) {
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
	providerName := decideMarker + fmt.Sprint(time.Now().UnixNano())
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		if conn, err := pool.Get(); err == nil {
			_, _ = conn.Exec(cctx, "DELETE FROM cost_ledger WHERE provider = $1", providerName)
		}
	})

	prompts := make(chan string, 1)
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var all strings.Builder
		for _, m := range body.Messages {
			all.WriteString(m.Content)
			all.WriteString("\n")
		}
		prompts <- all.String()
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"`Local:Qwen3:4B`.\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":21,\"completion_tokens\":4}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer chat.Close()

	rows := []router.ProviderRow{{ID: "p1", Name: providerName, Kind: "api", Driver: "openaicompat",
		BaseURL: chat.URL, DefaultModel: "m1", Enabled: true}}
	routes := []router.RouteRow{{Name: "summarize", Role: "summarize",
		Chain: []router.ChainEntry{{ProviderID: "p1", Model: "m1"}}, Enabled: true}}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "" }, nil)
	a := &API{store: &fakeSource{snap: snap}, ledger: ledger.New(pool, log, nil), log: log}
	a.providerCalls = metrics.New().NewCounterVec("provider_calls_total", "test", "provider", "route", "status")
	srv := httptest.NewServer(http.HandlerFunc(a.handleDecide))
	defer srv.Close()

	reqBody := `{"kind":"choice","question":"Which route fits?","state":"summarize this file","purpose":"route_intent","session_id":"itest-s1","options":[` +
		`{"id":"openai","description":"any OpenAI model"},` +
		`{"id":"openai:gpt-5","description":"flagship: slow"},` +
		`{"id":"local:qwen3:4b","description":"cheap"}]}`
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader([]byte(reqBody)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code = %d body=%s", resp.StatusCode, raw)
	}
	out := decodeDecide(t, raw)
	if out.Choice != "local:qwen3:4b" || out.Provider != providerName || out.Native || out.LedgerID == "" {
		t.Fatalf("out = %+v", out)
	}

	prompt := <-prompts
	for _, want := range []string{"- openai: any OpenAI model\n", "- openai:gpt-5: flagship: slow\n", "- local:qwen3:4b: cheap\n"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt %q lacks %q", prompt, want)
		}
	}

	var status, purpose, route, effort string
	var outTokens int
	if err := db.QueryRow(ctx, `SELECT status, purpose, route, effort, output_tokens FROM cost_ledger WHERE id = $1`, out.LedgerID).
		Scan(&status, &purpose, &route, &effort, &outTokens); err != nil {
		t.Fatalf("ledger row %s: %v", out.LedgerID, err)
	}
	if status != "ok" || purpose != "route_intent" || route != "summarize" || effort != "low" || outTokens != 4 {
		t.Fatalf("ledger row = status %q purpose %q route %q effort %q output_tokens %d", status, purpose, route, effort, outTokens)
	}

	old := `{"kind":"choice","question":"q","options":["openai","local:qwen3:4b"]}`
	resp, err = http.Post(srv.URL, "application/json", bytes.NewReader([]byte(old)))
	if err != nil {
		t.Fatalf("POST old shape: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("old string options: code = %d, want 400", resp.StatusCode)
	}
}
