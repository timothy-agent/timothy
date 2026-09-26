//go:build integration

package automations

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// TestStarterAppliesTriggerToolAllowlist pins issue #857 end to end: a
// run's mission carries its trigger's tool_allowlist cut down to the
// agent's Tools, falls back to no tools when nothing survives, and
// stays NULL when the trigger has no allowlist.
func TestStarterAppliesTriggerToolAllowlist(t *testing.T) {
	h := newDispatchHarness(t)
	h.starter.resolve = missions.ResolveDeps{Agent: func(context.Context, string) (missions.AgentDefaults, bool) {
		return missions.AgentDefaults{Tools: []string{"shell", "search_web"}}, true
	}}
	create := func(name string, trigger Trigger) string {
		t.Helper()
		trigger.Enabled = true
		id, err := h.store.Create(t.Context(), Automation{
			Name: h.tag + name, AgentID: h.agentID,
			Action:      Action{Kind: ActionMission, Mission: &missions.MissionTemplate{Goal: h.tag + name + " goal", Kind: missions.KindGeneral, Light: true}},
			Concurrency: ConcurrencySkip, MaxConcurrent: 1, MaxRunsPerHour: 60, Enabled: true,
			Triggers: []Trigger{trigger},
		})
		if err != nil {
			t.Fatalf("create automation: %v", err)
		}
		return id
	}
	missionOf := func(automationID string) missions.Mission {
		t.Helper()
		rs := h.wantStatuses(automationID, RunRunning)
		m, err := h.missions.Get(t.Context(), rs[len(rs)-1].MissionID)
		if err != nil {
			t.Fatalf("Get mission: %v", err)
		}
		return m
	}

	cases := []struct {
		name    string
		trigger Trigger
		want    []string
	}{
		{"manual subset", Trigger{Kind: TriggerManual, ToolAllowlist: []string{"shell"}}, []string{"shell"}},
		{"manual beyond the agent", Trigger{Kind: TriggerManual, ToolAllowlist: []string{"shell", "fetch_url"}}, []string{"shell"}},
		{"manual empty intersection", Trigger{Kind: TriggerManual, ToolAllowlist: []string{"write_file"}}, []string{noToolsAllowlist}},
		{"manual without allowlist", Trigger{Kind: TriggerManual}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := create(tc.name, tc.trigger)
			h.fireNow(id)
			h.pass()
			if got := missionOf(id).ToolAllowlist; !slices.Equal(got, tc.want) || (tc.want == nil) != (got == nil) {
				t.Fatalf("mission tool_allowlist = %#v, want %#v", got, tc.want)
			}
		})
	}

	// A run fired by its own trigger (trigger_id set) uses that trigger.
	id := create("cron trigger", Trigger{Kind: TriggerCron, Config: json.RawMessage(`{"expr":"0 9 * * *"}`), ToolAllowlist: []string{"search_web"}})
	a, err := h.store.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get automation: %v", err)
	}
	now := h.clock
	triggerID := a.Triggers[0].ID
	if _, _, err := h.store.CreateRun(t.Context(), nil, Run{AutomationID: id, TriggerID: &triggerID, DedupKey: h.tag + "cron", Status: RunStarting, CreatedAt: now, StartedAt: &now}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	h.pass()
	if got := missionOf(id).ToolAllowlist; !slices.Equal(got, []string{"search_web"}) {
		t.Fatalf("cron-fired mission tool_allowlist = %v, want [search_web]", got)
	}
}

// TestStarterSkipsRunWithDeletedTrigger pins issue #865: a run whose
// trigger vanished between dispatch and start (trigger_id goes NULL,
// ON DELETE SET NULL) is skipped with reason trigger_gone instead of
// starting unrestricted, never counts toward the breaker, and frees its
// concurrency slot for a run queued behind it.
func TestStarterSkipsRunWithDeletedTrigger(t *testing.T) {
	h := newDispatchHarness(t)
	id, err := h.store.Create(t.Context(), Automation{
		Name: h.tag + "trigger-gone", AgentID: h.agentID,
		Action:      Action{Kind: ActionMission, Mission: &missions.MissionTemplate{Goal: h.tag + "trigger-gone goal", Kind: missions.KindGeneral, Light: true}},
		Concurrency: ConcurrencyQueue, MaxConcurrent: 1, MaxRunsPerHour: 60, Enabled: true,
		Triggers: []Trigger{{Kind: TriggerCron, Config: json.RawMessage(`{"expr":"0 9 * * *"}`), ToolAllowlist: []string{"search_web"}, Enabled: true}},
	})
	if err != nil {
		t.Fatalf("create automation: %v", err)
	}
	a, err := h.store.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get automation: %v", err)
	}
	triggerID := a.Triggers[0].ID
	now := h.clock
	if _, _, err := h.store.CreateRun(t.Context(), nil, Run{
		AutomationID: id, TriggerID: &triggerID, DedupKey: h.tag + "gone", Status: RunStarting, CreatedAt: now, StartedAt: &now,
		Event: json.RawMessage(`{"kind":"cron.due"}`),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// A run.now behind it queues (concurrency=queue, the run above
	// counts as active): it must be promoted once the starting run is
	// skipped rather than sitting behind a slot nothing will ever free.
	h.fireNow(id)
	h.wantStatuses(id, RunStarting, RunQueued)

	h.exec(`DELETE FROM automation_triggers WHERE id = $1`, triggerID)
	// One Pass both skips the gone-trigger run and, in the same batch,
	// starts the run.now it frees up: it has no trigger_id and no
	// manual trigger to read one from, so it starts unrestricted, same
	// as before this issue (the regression case).
	h.pass()

	rs := h.wantStatuses(id, "skipped/trigger_gone", RunRunning)
	if rs[0].MissionID != "" {
		t.Fatalf("skipped run mission_id = %q, want empty (never started)", rs[0].MissionID)
	}
	if a, err = h.store.Get(t.Context(), id); err != nil {
		t.Fatalf("Get automation: %v", err)
	}
	if a.ConsecutiveFailures != 0 || !a.Enabled {
		t.Fatalf("after a trigger_gone skip: failures %d enabled %v, want 0/true (never counts toward the breaker)", a.ConsecutiveFailures, a.Enabled)
	}
	m, err := h.missions.Get(t.Context(), rs[1].MissionID)
	if err != nil {
		t.Fatalf("Get promoted mission: %v", err)
	}
	if m.ToolAllowlist != nil {
		t.Fatalf("promoted run.now mission ToolAllowlist = %v, want nil", m.ToolAllowlist)
	}
}

// TestStarterRejectsDelegatedHarnessWithAllowlist pins issue #865's
// create-time gate: a coding automation whose harness is a delegated
// CLI and whose firing trigger's tool_allowlist excludes shell or
// write_file fails at create, even though the agent's own Tools would
// otherwise let every entry through.
func TestStarterRejectsDelegatedHarnessWithAllowlist(t *testing.T) {
	h := newDispatchHarness(t)
	h.starter.resolve = missions.ResolveDeps{Agent: func(context.Context, string) (missions.AgentDefaults, bool) {
		return missions.AgentDefaults{Tools: []string{"shell", "write_file", "search_web"}}, true
	}}
	id, err := h.store.Create(t.Context(), Automation{
		Name: h.tag + "delegated-harness", AgentID: h.agentID,
		Action: Action{Kind: ActionMission, Mission: &missions.MissionTemplate{
			Goal: h.tag + "delegated-harness goal", Kind: missions.KindCoding, Harness: "codex-cli",
		}},
		Concurrency: ConcurrencySkip, MaxConcurrent: 1, MaxRunsPerHour: 60, Enabled: true,
		Triggers: []Trigger{{Kind: TriggerManual, ToolAllowlist: []string{"search_web"}, Enabled: true}},
	})
	if err != nil {
		t.Fatalf("create automation: %v", err)
	}
	h.fireNow(id)
	h.pass()
	rs := h.wantStatuses(id, "failed/harness_ignores_allowlist")
	if rs[0].MissionID != "" {
		t.Fatalf("rejected run mission_id = %q, want empty (no mission)", rs[0].MissionID)
	}
}
