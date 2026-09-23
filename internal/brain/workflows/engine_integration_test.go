//go:build integration

package workflows

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// storeSpawner validates and inserts like missions.Driver.Create, minus
// provisioning.
type storeSpawner struct{ store *missions.Store }

func (s storeSpawner) Create(ctx context.Context, m missions.Mission) (string, error) {
	if err := missions.ValidateCreate(ctx, m, missions.ValidateDeps{}); err != nil {
		return "", err
	}
	return s.store.Create(ctx, m)
}

// TestStartRunStepMissionGetsAgentDefaults covers issue #816: a workflow
// step's mission row carries its agent's route, review route, prompt
// overlay and harness, resolved through missions.ResolveDefaults.
func TestStartRunStepMissionGetsAgentDefaults(t *testing.T) {
	wfStore := testStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := wfStore.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	agentReg := agents.NewStore(wfStore.db, log)
	agentID, err := agentReg.Create(ctx, agents.Agent{
		Name: marker + "coder", Enabled: true, Route: "agent-route", ReviewRoute: "agent-review",
		PromptOverlay: "overlay", Harness: "claude-cli",
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		_, _ = conn.Exec(cctx, `DELETE FROM missions WHERE agent_id = $1`, agentID)
		_, _ = conn.Exec(cctx, `DELETE FROM workflow_runs WHERE workflow_id IN (SELECT id FROM workflows WHERE name LIKE $1 || '%')`, marker)
		_, _ = conn.Exec(cctx, `DELETE FROM agents WHERE id = $1`, agentID)
	})

	def, _ := json.Marshal(Definition{
		Entry: "build",
		Steps: map[string]Step{"build": {Goal: marker + "step goal", Kind: "coding", AgentID: agentID}},
		Edges: []Edge{{From: "build", On: "mission.done", To: "end", MaxIterations: 1}},
	})
	wfID, err := wfStore.Create(ctx, marker+"agent-defaults", def)
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	missionStore := missions.NewStore(wfStore.db, log)
	e := NewEngine(wfStore, storeSpawner{missionStore}, missionStore, log)
	e.SetResolveDeps(missions.ResolveDeps{
		Agent: func(ctx context.Context, id string) (missions.AgentDefaults, bool) {
			a, ok := agentReg.ResolveByID(ctx, id)
			if !ok {
				return missions.AgentDefaults{}, false
			}
			return missions.AgentDefaults{Route: a.Route, ReviewRoute: a.ReviewRoute, PromptOverlay: a.PromptOverlay, Harness: a.Harness}, true
		},
		RouteForRole:          func(context.Context, string) string { return "default" },
		CodingExecutorDefault: func(context.Context) string { return "pi" },
	})

	runID, err := e.StartRun(ctx, wfID, nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var missionID string
	if err := db.QueryRow(ctx, `SELECT id FROM missions WHERE workflow_run_id = $1`, runID).Scan(&missionID); err != nil {
		t.Fatalf("query step mission: %v", err)
	}
	m, err := missionStore.Get(ctx, missionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Route != "agent-route" || m.ReviewRoute != "agent-review" || m.PromptOverlay != "overlay" {
		t.Fatalf("step mission route=%q review=%q overlay=%q, want the agent's defaults", m.Route, m.ReviewRoute, m.PromptOverlay)
	}
	if m.Harness != "claude-cli" {
		t.Fatalf("step mission harness = %q, want the agent's claude-cli ahead of the settings default", m.Harness)
	}
	if m.WorkflowStep != "build" || !m.AutoApprovePlan || m.BudgetCurrency != "USD" {
		t.Fatalf("step mission step=%q auto_approve_plan=%v currency=%q", m.WorkflowStep, m.AutoApprovePlan, m.BudgetCurrency)
	}
	// Issue #817: the row records a workflow origin nobody is watching.
	if m.OriginKind != missions.OriginWorkflow || !m.Unattended {
		t.Fatalf("step mission origin_kind=%q unattended=%v, want workflow true", m.OriginKind, m.Unattended)
	}
	if m.PermissionTimeoutSeconds == nil || *m.PermissionTimeoutSeconds != 1800 {
		t.Fatalf("step mission permission_timeout_seconds = %v, want 1800", m.PermissionTimeoutSeconds)
	}
}
