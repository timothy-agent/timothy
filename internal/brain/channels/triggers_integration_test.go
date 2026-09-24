//go:build integration

package channels

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/automations"
	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// TestChannelTriggerStartsAutomation pins issue #831 end to end: a
// paired sender's "/run coverage" becomes one events row and one run,
// never a chat turn, the reply names the automation, the run's mission
// carries the conversation and its mission.done reaches the fake bot.
// An unpaired sender fires nothing and a redelivery fires once.
func TestChannelTriggerStartsAutomation(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	tag := runTag()
	ms, _ := missionFixtures(t, pool)
	db, _ := pool.Get()
	var agentID string
	if err := db.QueryRow(ctx, `SELECT id FROM agents WHERE is_default LIMIT 1`).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	channelID := createChannel(t, s, tag+"trigger")
	if _, _, err := s.EnsurePairing(ctx, channelID, "77", "Ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, channelID, "77"); err != nil {
		t.Fatal(err)
	}

	as := automations.NewStore(pool)
	name := tag + "coverage"
	automationID, err := as.Create(ctx, automations.Automation{
		Name: name, AgentID: agentID,
		Action:      automations.Action{Kind: automations.ActionMission, Mission: &missions.MissionTemplate{Goal: tag + "{{event.text}}", Kind: missions.KindGeneral, Light: true}},
		Concurrency: automations.ConcurrencyParallel, MaxConcurrent: 3, MaxRunsPerHour: 60, Enabled: true,
		Triggers: []automations.Trigger{{Kind: automations.TriggerChannel, Enabled: true,
			Config: json.RawMessage(`{"channel_id":"` + channelID + `","pattern":"^/run coverage$"}`)}},
	})
	if err != nil {
		t.Fatalf("create automation: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Exec(cctx, `DELETE FROM events WHERE payload->>'channel_id' = $1
			OR payload->>'mission_id' IN (SELECT id::text FROM missions WHERE goal LIKE $2 || '%')`, channelID, tag)
		_, _ = db.Exec(cctx, `DELETE FROM missions WHERE goal LIKE $1 || '%'`, tag)
		_, _ = db.Exec(cctx, `DELETE FROM automations WHERE id = $1`, automationID)
	})

	evStore := events.NewStore(pool)
	f := newFakeBot(t)
	outcomes := NewOutcomes(s, MissionDeps{Get: ms.Get, Events: ms.Events}, fakeResolve, f.srv.Client(), nil, discardLog())
	outcomes.APIBase = f.srv.URL
	drainer := events.NewDrainer(evStore, []events.Consumer{automations.NewDispatcher(nil, discardLog()), outcomes}, nil, discardLog())
	starter := automations.NewStarter(as, ms.Create, missions.ResolveDeps{}, nil, nil, nil, discardLog())
	drain := func() {
		t.Helper()
		for range 10 {
			n, err := drainer.Drain(ctx)
			if err != nil {
				t.Fatalf("Drain: %v", err)
			}
			if n == 0 {
				return
			}
		}
	}

	fc := &fakeChat{}
	var kicks atomic.Int32
	svc := New(s, fc.chat, MissionDeps{}, fakeResolve, f.srv.Client(), discardLog())
	svc.SetTriggers(func(ctx context.Context, id string) ([]ChannelTrigger, error) {
		rows, err := as.ChannelTriggers(ctx, id)
		if err != nil {
			return nil, err
		}
		var out []ChannelTrigger
		for _, r := range rows {
			re, err := automations.CompileChannelPattern(r.Pattern)
			if err != nil {
				continue
			}
			out = append(out, ChannelTrigger{TriggerID: r.TriggerID, AutomationID: r.AutomationID, AutomationName: r.AutomationName, Pattern: re, ChatID: r.ChatID})
		}
		return out, nil
	}, evStore.AddIfNew, func() { kicks.Add(1) })

	channelEvents := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM events WHERE source = 'channel' AND payload->>'channel_id' = $1`, channelID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Unpaired sender with a matching text: a pairing prompt, nothing fires.
	f.script(privateUpdate(1, 88, "/run coverage"))
	stop := runService(t, svc, f)
	defer stop()
	f.waitConsumed(t)
	if got := sendTexts(f); len(got) != 1 || got[0] != msgPairingPrompt {
		t.Fatalf("unpaired replies = %q", got)
	}
	if n := channelEvents(); n != 0 || kicks.Load() != 0 {
		t.Fatalf("unpaired sender fired: events=%d kicks=%d", n, kicks.Load())
	}

	// Paired sender: one event, a Started reply, no chat turn.
	f.script(privateUpdate(2, 77, "/run coverage"))
	f.waitConsumed(t)
	if got := sendTexts(f); len(got) != 2 || got[1] != msgStarted+name {
		t.Fatalf("paired replies = %q", got)
	}
	if n := channelEvents(); n != 1 || kicks.Load() != 1 {
		t.Fatalf("paired sender: events=%d kicks=%d, want 1 and 1", n, kicks.Load())
	}
	if fc.count() != 0 {
		t.Fatalf("a triggered message also ran %d chat turns", fc.count())
	}

	// Redelivery of the same update: nothing new.
	f.script(privateUpdate(2, 77, "/run coverage"))
	f.waitConsumed(t)
	if n := channelEvents(); n != 1 || len(sendTexts(f)) != 2 || fc.count() != 0 {
		t.Fatalf("redelivery: events=%d replies=%d chats=%d", n, len(sendTexts(f)), fc.count())
	}

	// A message that matches nothing is still a chat turn.
	f.script(privateUpdate(3, 77, "hello"))
	waitFor(t, "the chat turn", func() bool { return fc.count() == 1 })

	drain()
	var runs int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND status = 'starting'`, automationID).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("starting runs = %d %v, want 1", runs, err)
	}
	if _, err := starter.Pass(ctx); err != nil {
		t.Fatalf("starter Pass: %v", err)
	}
	rs, err := as.ListRuns(ctx, automationID, 10)
	if err != nil || len(rs) != 1 || rs[0].Status != automations.RunRunning || rs[0].MissionID == "" {
		t.Fatalf("runs = %+v %v", rs, err)
	}
	m, err := ms.Get(ctx, rs[0].MissionID)
	if err != nil {
		t.Fatal(err)
	}
	conv, found, err := s.ConversationFor(ctx, channelID, "77", "")
	if err != nil || !found || m.ChannelConversationID != conv.ID || m.Goal != tag+"/run coverage" {
		t.Fatalf("mission conversation = %q (conversation %q found %v %v), goal %q", m.ChannelConversationID, conv.ID, found, err, m.Goal)
	}

	// The mission's done event reports back to the chat.
	before := len(f.callsOf("sendMessage"))
	done, err := events.MissionTerminal(events.MissionPayload{MissionID: m.ID, Phase: "done", OriginKind: missions.OriginAutomation, Unattended: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evStore.Add(ctx, done); err != nil {
		t.Fatal(err)
	}
	drain()
	sends := f.callsOf("sendMessage")
	if len(sends) != before+1 {
		t.Fatalf("outcome sends = %d, want one more than %d", len(sends), before)
	}
	last := sends[len(sends)-1]
	if text, _ := last.Body["text"].(string); !strings.Contains(text, "is done") || last.Body["chat_id"] != float64(77) {
		t.Fatalf("outcome message = %+v", last.Body)
	}
}
