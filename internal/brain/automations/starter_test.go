package automations

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

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
