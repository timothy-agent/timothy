package automations

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

// TestTriggerAllowlist pins the trigger_id resolution rules issue #865
// closes the gap on: a run whose trigger_id went NULL between dispatch
// and start (ON DELETE SET NULL) must be told apart from a genuine
// run.now with no manual trigger, since only the latter is safe to
// start unrestricted.
func TestTriggerAllowlist(t *testing.T) {
	t.Parallel()
	manual := Trigger{ID: "t-manual", Kind: TriggerManual, ToolAllowlist: []string{"shell"}}
	cron := Trigger{ID: "t-cron", Kind: TriggerCron, ToolAllowlist: []string{"search_web"}}
	webhook := Trigger{ID: "t-webhook", Kind: TriggerWebhook, ToolAllowlist: []string{"fetch_url"}}
	channel := Trigger{ID: "t-channel", Kind: TriggerChannel, ToolAllowlist: []string{"send_mail"}}
	connector := Trigger{ID: "t-connector", Kind: TriggerConnectorEvent, ToolAllowlist: []string{"github_read_pr"}}
	triggers := []Trigger{manual, cron, webhook, channel, connector}
	idOf := func(id string) *string { return &id }

	cases := []struct {
		name      string
		triggerID *string
		eventKind any
		want      []string
		wantGone  bool
	}{
		{"run.now, nil id, manual trigger present", nil, events.KindRunNow, []string{"shell"}, false},
		{"cron.due, nil id", nil, events.KindCronDue, nil, true},
		{"webhook.received, nil id", nil, events.KindWebhookReceived, nil, true},
		{"channel.message, nil id", nil, events.KindChannelMessage, nil, true},
		{"connector kind, nil id", nil, "github", nil, true},
		{"trigger id found", idOf("t-cron"), events.KindCronDue, []string{"search_web"}, false},
		{"trigger id set but gone", idOf("t-deleted"), events.KindCronDue, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := triggerAllowlist(triggers, tc.triggerID, tc.eventKind)
			if tc.wantGone {
				if !errors.Is(err, errTriggerGone) {
					t.Fatalf("err = %v, want errTriggerGone", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("triggerAllowlist = %v, want %v", got, tc.want)
			}
		})
	}

	// run.now with nil id and no manual trigger at all is still safe to
	// start unrestricted: there is nothing for the trigger to have
	// scoped it to.
	got, err := triggerAllowlist([]Trigger{cron}, nil, events.KindRunNow)
	if err != nil || got != nil {
		t.Fatalf("run.now without a manual trigger = %v, %v; want nil, nil", got, err)
	}

	// A run row with no event kind at all (a crash-recovery row
	// inserted directly rather than through fire()) carries no evidence
	// of a deleted trigger, so it stays legacy-compatible.
	got, err = triggerAllowlist(triggers, nil, nil)
	if err != nil || got != nil {
		t.Fatalf("nil id with no event kind = %v, %v; want nil, nil", got, err)
	}
}

// TestIntersectToolAllowlist pins issue #857's ceiling: a trigger entry
// survives only when the agent's Tools grant it, exactly or by
// connector suffix, or when it names a note tool.
func TestIntersectToolAllowlist(t *testing.T) {
	t.Parallel()
	agent := []string{"shell", "search_web", "gmail_search_mail"}
	cases := []struct {
		name    string
		trigger []string
		want    []string
	}{
		{"empty trigger list", nil, nil},
		{"subset", []string{"shell"}, []string{"shell"}},
		{"superset drops what the agent lacks", []string{"shell", "search_web", "fetch_url"}, []string{"shell", "search_web"}},
		{"unknown names dropped", []string{"no_such_tool"}, nil},
		{"suffix entry kept", []string{"search_mail"}, []string{"search_mail"}},
		{"empty intersection", []string{"write_file"}, nil},
		{"note tools pass the agent ceiling", []string{"read_note", "write_note", "fetch_url"}, []string{"read_note", "write_note"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := intersectToolAllowlist(tc.trigger, agent); !slices.Equal(got, tc.want) {
				t.Fatalf("intersectToolAllowlist(%v) = %v, want %v", tc.trigger, got, tc.want)
			}
		})
	}
	if got := intersectToolAllowlist([]string{"shell"}, nil); got != nil {
		t.Fatalf("agent without tools = %v, want nil", got)
	}
}

func TestEventConversationID(t *testing.T) {
	t.Parallel()
	ev := events.Event{Kind: events.KindChannelMessage, Source: events.SourceChannel}
	raw, _ := json.Marshal(channelRunEvent(ev, events.ChannelMessagePayload{ChannelID: "c1", ConversationID: "conv-1", Text: "/run"}))
	cases := []struct {
		name  string
		event map[string]any
		want  string
	}{
		{"channel run", runEvent(raw), "conv-1"},
		{"channel run without conversation", map[string]any{"kind": events.KindChannelMessage}, ""},
		{"webhook run naming a conversation", map[string]any{"kind": events.KindWebhookReceived, "conversation_id": "conv-2"}, ""},
		{"non-string id", map[string]any{"kind": events.KindChannelMessage, "conversation_id": 7}, ""},
		{"empty event", map[string]any{}, ""},
	}
	for _, tc := range cases {
		if got := eventConversationID(tc.event); got != tc.want {
			t.Errorf("%s: eventConversationID = %q, want %q", tc.name, got, tc.want)
		}
	}
}
