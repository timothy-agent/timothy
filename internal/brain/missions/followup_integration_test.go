//go:build integration

package missions

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// TestChatFollowUpLandsWithAgentDefaults covers issue #841 end to end
// against a real Store: CreateFollowUp resolves through
// ResolveDefaults, so a follow-up mission's persisted row carries the
// agent's CURRENT overlay (not the parent's snapshot), origin_kind
// followup, and the agent id, and a route with no usable provider
// leaves no child row at all.
func TestChatFollowUpLandsWithAgentDefaults(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("db.Get: %v", err)
	}
	agentName := marker + tag + " agent"
	var agentID string
	if err := db.QueryRow(ctx, `INSERT INTO agents (name, prompt_overlay) VALUES ($1, $2) RETURNING id`,
		agentName, "fresh overlay").Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.Exec(cctx, `DELETE FROM agents WHERE id = $1`, agentID); err != nil {
			t.Logf("cleanup agent: %v", err)
		}
	})

	agentResolver := func(_ context.Context, id string) (AgentDefaults, bool) {
		if id != agentID {
			return AgentDefaults{}, false
		}
		var overlay string
		if err := db.QueryRow(context.Background(), `SELECT prompt_overlay FROM agents WHERE id = $1`, id).Scan(&overlay); err != nil {
			return AgentDefaults{}, false
		}
		return AgentDefaults{PromptOverlay: overlay}, true
	}

	t.Run("resolves defaults", func(t *testing.T) {
		parentID, err := s.Create(ctx, Mission{
			Goal: marker + tag + " parent", Kind: KindGeneral, Route: "default", AgentID: agentID,
		})
		if err != nil {
			t.Fatalf("Create parent: %v", err)
		}
		if err := s.ApplyTransition(ctx, parentID, Transition{Next: StepState{Phase: PhaseDone, Status: StatusDone}}); err != nil {
			t.Fatalf("ApplyTransition parent to terminal: %v", err)
		}

		logger := slog.Default()
		d := NewDriver(s, followUpBlockedRunner(), nil, nil, nil, nil, nil, nil, logger)
		d.SetResolveDeps(ResolveDeps{Agent: agentResolver})

		childID, err := d.CreateFollowUp(ctx, parentID, FollowUpOptions{Goal: marker + tag + " child"})
		if err != nil {
			t.Fatalf("CreateFollowUp: %v", err)
		}
		child, err := s.Get(ctx, childID)
		if err != nil {
			t.Fatalf("Get child: %v", err)
		}
		if child.OriginKind != OriginFollowup {
			t.Fatalf("child.OriginKind = %q, want %q", child.OriginKind, OriginFollowup)
		}
		if child.AgentID != agentID {
			t.Fatalf("child.AgentID = %q, want %q", child.AgentID, agentID)
		}
		if child.PromptOverlay != "fresh overlay" {
			t.Fatalf("child.PromptOverlay = %q, want the agent's current overlay", child.PromptOverlay)
		}
		if child.ParentMissionID != parentID {
			t.Fatalf("child.ParentMissionID = %q, want %q", child.ParentMissionID, parentID)
		}
	})

	t.Run("unusable route leaves no child row", func(t *testing.T) {
		parentID, err := s.Create(ctx, Mission{
			Goal: marker + tag + " parent dead route", Kind: KindGeneral, Route: "dead", AgentID: agentID,
		})
		if err != nil {
			t.Fatalf("Create parent: %v", err)
		}
		if err := s.ApplyTransition(ctx, parentID, Transition{Next: StepState{Phase: PhaseDone, Status: StatusDone}}); err != nil {
			t.Fatalf("ApplyTransition parent to terminal: %v", err)
		}

		logger := slog.Default()
		d := NewDriver(s, followUpBlockedRunner(), nil, nil, nil, nil, nil, nil, logger)
		d.SetResolveDeps(ResolveDeps{Agent: agentResolver, ResolveRoute: resolveFixture})

		if _, err := d.CreateFollowUp(ctx, parentID, FollowUpOptions{Goal: marker + tag + " child dead route"}); err == nil {
			t.Fatal("expected the D-100 route gate to reject a dead route")
		}
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM missions WHERE parent_mission_id = $1`, parentID).Scan(&count); err != nil {
			t.Fatalf("count children: %v", err)
		}
		if count != 0 {
			t.Fatalf("children of %s = %d, want none: the route gate should leave no child row", parentID, count)
		}
	})
}
