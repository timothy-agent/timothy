//go:build integration

package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

// TestDeleteAgentReferencedByAutomationReturns409 covers issues
// #815/#821: an agent an automation runs as cannot be deleted, and the
// refusal names the automation.
func TestDeleteAgentReferencedByAutomationReturns409(t *testing.T) {
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

	prefix := fmt.Sprintf("itest-api-agent-inuse-%d-", time.Now().UnixNano())
	var agentID string
	if err := db.QueryRow(ctx, `INSERT INTO agents (name) VALUES ($1) RETURNING id`, prefix+"agent").Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO automations (name, agent_id, action) VALUES ($1, $2, '{"kind":"mission"}')`, prefix+"automation", agentID); err != nil {
		t.Fatalf("insert automation: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = db.Exec(cctx, `DELETE FROM automations WHERE name LIKE $1 || '%'`, prefix)
		_, _ = db.Exec(cctx, `DELETE FROM agents WHERE name LIKE $1 || '%'`, prefix)
	})

	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerAgents(m.Handle, agents.NewStore(pool, log))

	req := httptest.NewRequest("DELETE", "/v1/admin/agents/"+agentID, nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatalf("DELETE referenced agent = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	assertErrorBody(t, w, "in_use", prefix+"automation")

	var still bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agents WHERE id = $1)`, agentID).Scan(&still); err != nil {
		t.Fatalf("check agent: %v", err)
	}
	if !still {
		t.Fatal("agent was deleted despite the 409")
	}
}
