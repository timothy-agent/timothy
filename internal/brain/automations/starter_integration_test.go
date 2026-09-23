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
