//go:build integration

package agents

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const marker = "itest-agent-"

func testStore(t *testing.T) *Store {
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
	// Sweep at setup AND teardown; the teardown uses a fresh one-shot
	// connection because the pool dies with t.Context().
	_, _ = db.Exec(ctx, "DELETE FROM agents WHERE name LIKE $1 || '%'", marker)
	_, _ = db.Exec(ctx, "DELETE FROM admin_audit WHERE entity = 'agent'")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Logf("teardown sweep skipped: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		_, _ = conn.Exec(ctx, "DELETE FROM agents WHERE name LIKE $1 || '%'", marker)
		_, _ = conn.Exec(ctx, "DELETE FROM admin_audit WHERE entity = 'agent'")
	})
	return NewStore(pool, log)
}

func TestAgentCRUDAndResolve(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	name := marker + "researcher"
	id, err := s.Create(ctx, Agent{
		Name: name, Description: "d", PromptOverlay: "overlay",
		Route: "research", Skills: []string{"research-task"},
		Tools: []string{"web_search"}, Memory: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Create(ctx, Agent{Name: "Bad Name!"}); err == nil {
		t.Fatal("invalid name accepted")
	}

	a, ok := s.Resolve(ctx, name)
	if !ok || a.Route != "research" || a.PromptOverlay != "overlay" || len(a.Tools) != 1 {
		t.Fatalf("Resolve = %+v ok=%v", a, ok)
	}

	// ResolveByID is Resolve's id-keyed counterpart (missions' create
	// handler uses this — the picker sends a.id, not a.name).
	byID, ok := s.ResolveByID(ctx, id)
	if !ok || byID.Name != name || byID.Route != "research" {
		t.Fatalf("ResolveByID = %+v ok=%v", byID, ok)
	}
	if _, ok := s.ResolveByID(ctx, "00000000-0000-0000-0000-000000000000"); ok {
		t.Fatal("unknown id resolved as known")
	}
	if def, ok := s.ResolveByID(ctx, ""); !ok || def.Name != "general" {
		t.Fatalf("ResolveByID(\"\") = %+v ok=%v, want seeded general", def, ok)
	}

	// Enabled is the auto-dispatch candidate set: the seeded default
	// plus this enabled fixture, never a disabled agent, never the
	// synthetic zero-value fallback (it carries an empty Name).
	disabledName := marker + "off"
	if _, err := s.Create(ctx, Agent{Name: disabledName, Enabled: false}); err != nil {
		t.Fatalf("Create disabled: %v", err)
	}
	byName := map[string]bool{}
	for _, e := range s.Enabled(ctx) {
		if e.Name == "" {
			t.Fatal("Enabled returned an entry with an empty name")
		}
		byName[e.Name] = true
	}
	if !byName[name] || !byName["general"] {
		t.Fatalf("Enabled() = %v, want %s and general present", byName, name)
	}
	if byName[disabledName] {
		t.Fatalf("Enabled() included disabled agent %s", disabledName)
	}
	// Unknown names fall back to the default but report false.
	if _, ok := s.Resolve(ctx, marker+"ghost"); ok {
		t.Fatal("unknown agent resolved as known")
	}
	// Empty name resolves the seeded default.
	def, ok := s.Resolve(ctx, "")
	if !ok || def.Name != "general" {
		t.Fatalf("default agent = %+v ok=%v, want seeded general", def, ok)
	}

	route := "default"
	if err := s.Patch(ctx, id, Patch{Route: &route}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if a, _ := s.Resolve(ctx, name); a.Route != "default" {
		t.Fatalf("patched route = %q (cache must invalidate)", a.Route)
	}

	// The seeded default is protected; moving the flag frees it.
	agentsList, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var generalID string
	for _, a := range agentsList {
		if a.Name == "general" {
			generalID = a.ID
		}
	}
	if err := s.Delete(ctx, generalID); err == nil {
		t.Fatal("default agent deletion allowed")
	}
	if err := s.SetDefault(ctx, id); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	defer func() {
		// Hand the flag back so other tests (and the dev DB) keep the
		// seeded default.
		if err := s.SetDefault(ctx, generalID); err != nil {
			t.Errorf("restore default: %v", err)
		}
	}()
	if def, _ := s.Resolve(ctx, ""); def.Name != name {
		t.Fatalf("default after SetDefault = %q, want %s", def.Name, name)
	}
	if err := s.Delete(ctx, id); err == nil {
		t.Fatal("new default agent deletion allowed")
	}
}

// TestAgentMissionColumns covers the columns missions (internal/brain/
// missions) rely on: a chat-only agent leaves them at their zero
// values, a mission-capable agent round-trips all three through
// Create, Resolve, and Patch.
func TestAgentMissionColumns(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	name := marker + "mission-capable"
	budget := 5.5
	id, err := s.Create(ctx, Agent{
		Name: name, Route: "default", Enabled: true,
		ReviewRoute: "research", BudgetUSD: &budget,
		ApprovalAllowlist: []string{"shell_exec"}, Harness: "claude-cli",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	a, ok := s.Resolve(ctx, name)
	if !ok || a.ReviewRoute != "research" || a.BudgetUSD == nil || *a.BudgetUSD != budget ||
		len(a.ApprovalAllowlist) != 1 || a.ApprovalAllowlist[0] != "shell_exec" || a.Harness != "claude-cli" {
		t.Fatalf("Resolve = %+v ok=%v, want mission columns round-tripped", a, ok)
	}

	newBudget := 9.0
	reviewRoute := "default"
	newHarness := "codex-cli"
	if err := s.Patch(ctx, id, Patch{ReviewRoute: &reviewRoute, BudgetUSD: &newBudget, Harness: &newHarness}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if a, _ := s.Resolve(ctx, name); a.ReviewRoute != "default" || a.BudgetUSD == nil || *a.BudgetUSD != newBudget || a.Harness != "codex-cli" {
		t.Fatalf("patched mission columns = %+v (cache must invalidate)", a)
	}

	// A chat-only agent (none of these fields set) leaves them at zero
	// values — meaningless to chat, but must not error or default to
	// something surprising.
	chatOnly := marker + "chat-only"
	if _, err := s.Create(ctx, Agent{Name: chatOnly, Enabled: true}); err != nil {
		t.Fatalf("Create chat-only: %v", err)
	}
	if a, _ := s.Resolve(ctx, chatOnly); a.ReviewRoute != "" || a.BudgetUSD != nil || len(a.ApprovalAllowlist) != 0 || a.Harness != "" {
		t.Fatalf("chat-only agent mission columns = %+v, want all zero", a)
	}

	// An unregistered harness name is rejected on create and on patch;
	// empty stays valid (inherit).
	if _, err := s.Create(ctx, Agent{Name: marker + "bad-harness", Enabled: true, Harness: "not-a-harness"}); err == nil {
		t.Fatal("Create with unknown harness accepted")
	}
	badHarness := "not-a-harness"
	if err := s.Patch(ctx, id, Patch{Harness: &badHarness}); err == nil {
		t.Fatal("Patch with unknown harness accepted")
	}
	emptyHarness := ""
	if err := s.Patch(ctx, id, Patch{Harness: &emptyHarness}); err != nil {
		t.Fatalf("Patch clearing harness to empty: %v", err)
	}
	if a, _ := s.Resolve(ctx, name); a.Harness != "" {
		t.Fatalf("harness after clearing = %q, want empty (inherit)", a.Harness)
	}
}
