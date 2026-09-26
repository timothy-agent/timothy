//go:build integration

package automations

import (
	"encoding/json"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// TestDispatchChannelMessage pins issue #831's trigger path: the
// dispatcher fires only the trigger the channel matched, re-checks its
// pattern, fires once per message, and the run's mission reports back
// to the channel conversation.
func TestDispatchChannelMessage(t *testing.T) {
	h := newDispatchHarness(t)
	ctx := t.Context()
	newID := func() string {
		t.Helper()
		db, _ := h.pool.Get()
		var id string
		if err := db.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	channelID, convID := newID(), newID()
	create := func(name, pattern string, enabled bool) (string, string) {
		t.Helper()
		id, err := h.store.Create(ctx, Automation{
			Name: h.tag + name, AgentID: h.agentID,
			Action:      Action{Kind: ActionMission, Mission: &missions.MissionTemplate{Goal: h.tag + "{{event.sender}}: {{event.text}}", Kind: missions.KindGeneral, Light: true}},
			Concurrency: ConcurrencyParallel, MaxConcurrent: 3, MaxRunsPerHour: 60, Enabled: enabled,
			Triggers: []Trigger{{Kind: TriggerChannel, Enabled: true,
				Config: json.RawMessage(`{"channel_id":"` + channelID + `","pattern":"` + pattern + `"}`)}},
		})
		if err != nil {
			t.Fatalf("create automation: %v", err)
		}
		a, err := h.store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return id, a.Triggers[0].ID
	}
	coverage, coverageTrigger := create("coverage", "^/run coverage$", true)
	broad, broadTrigger := create("broad", "^/run", true)
	_, _ = create("disabled", "^/run", false)

	ts, err := h.store.ChannelTriggers(ctx, channelID)
	if err != nil {
		t.Fatalf("ChannelTriggers: %v", err)
	}
	if len(ts) != 2 || ts[0].TriggerID != coverageTrigger || ts[0].AutomationName != h.tag+"coverage" || ts[0].Pattern != "^/run coverage$" || ts[1].TriggerID != broadTrigger {
		t.Fatalf("ChannelTriggers = %+v", ts)
	}

	msg := func(trigger, text, dedup string) events.Event {
		t.Helper()
		ev, err := events.ChannelMessage(events.ChannelMessagePayload{ChannelID: channelID, ConversationID: convID, ChatID: "77",
			UserID: "77", Sender: "Ada", MessageID: dedup, Text: text, TriggerID: trigger}, "telegram:"+dedup+h.tag)
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	ev := msg(coverageTrigger, "/RUN coverage", "1")
	if !h.addEventIfNew(ev) {
		t.Fatal("first delivery was not inserted")
	}
	if h.addEventIfNew(ev) {
		t.Fatal("duplicate delivery inserted a second event")
	}
	h.drain()
	rs := h.wantStatuses(coverage, RunStarting)
	h.wantStatuses(broad)
	var runEv map[string]any
	_ = json.Unmarshal(rs[0].Event, &runEv)
	if runEv["text"] != "/RUN coverage" || runEv["sender"] != "Ada" || runEv["conversation_id"] != convID || runEv["channel_id"] != channelID {
		t.Fatalf("run event = %v", runEv)
	}

	// A stale match (pattern no longer matches) and an unknown trigger fire nothing.
	h.addEvent(msg(coverageTrigger, "/run tests", "2"))
	h.addEvent(msg(newID(), "/run coverage", "3"))
	h.drain()
	h.wantStatuses(coverage, RunStarting)
	h.wantStatuses(broad)

	h.pass()
	rs = h.wantStatuses(coverage, RunRunning)
	m, err := h.missions.Get(ctx, rs[0].MissionID)
	if err != nil {
		t.Fatalf("Get mission: %v", err)
	}
	if m.ChannelConversationID != convID || m.Goal != h.tag+"Ada: /RUN coverage" {
		t.Fatalf("mission conversation = %q goal = %q", m.ChannelConversationID, m.Goal)
	}
	h.exec(`DELETE FROM events WHERE source = 'channel' AND payload->>'channel_id' = $1`, channelID)
}
