package channels

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

func triggerService(list func(ctx context.Context, id string) ([]ChannelTrigger, error)) *Service {
	s := &Service{log: discardLog(), now: time.Now, trigList: map[string]cachedTriggers{}}
	s.SetTriggers(list, func(context.Context, events.Event) (int64, bool, error) { return 1, true, nil }, nil)
	return s
}

func TestMatchTrigger(t *testing.T) {
	ci := func(p string) *regexp.Regexp { return regexp.MustCompile("(?i)" + p) }
	coverage := ChannelTrigger{TriggerID: "t1", AutomationID: "a1", AutomationName: "Coverage", Pattern: ci(`^/run coverage$`)}
	group := ChannelTrigger{TriggerID: "t2", AutomationID: "a2", AutomationName: "Group", Pattern: ci(`^/run`), ChatID: "-100"}
	broad := ChannelTrigger{TriggerID: "t3", AutomationID: "a3", AutomationName: "Broad", Pattern: ci(`run`)}
	tests := []struct {
		name     string
		triggers []ChannelTrigger
		chat     string
		text     string
		want     string
	}{
		{"exact", []ChannelTrigger{coverage}, "77", "/run coverage", "t1"},
		{"case-insensitive and trimmed", []ChannelTrigger{coverage}, "77", "  /RUN Coverage \n", "t1"},
		{"no match", []ChannelTrigger{coverage}, "77", "/run tests", ""},
		{"chat filter hit", []ChannelTrigger{group}, "-100", "/run tests", "t2"},
		{"chat filter miss", []ChannelTrigger{group}, "77", "/run tests", ""},
		{"first of several fires", []ChannelTrigger{group, coverage, broad}, "-100", "/run coverage", "t2"},
		{"nil pattern skipped", []ChannelTrigger{{TriggerID: "tx"}, broad}, "77", "run", "t3"},
		{"no triggers", nil, "77", "/run coverage", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &runner{svc: triggerService(func(context.Context, string) ([]ChannelTrigger, error) { return tt.triggers, nil }), ch: Channel{ID: "c1"}}
			got, ok, err := r.matchTrigger(t.Context(), inbound{ChatID: tt.chat, Text: tt.text})
			if err != nil {
				t.Fatal(err)
			}
			if ok != (tt.want != "") || got.TriggerID != tt.want {
				t.Fatalf("matchTrigger = %q %v, want %q", got.TriggerID, ok, tt.want)
			}
		})
	}
}

func TestMatchTriggerListError(t *testing.T) {
	boom := errors.New("db down")
	r := &runner{svc: triggerService(func(context.Context, string) ([]ChannelTrigger, error) { return nil, boom }), ch: Channel{ID: "c1"}}
	if _, _, err := r.matchTrigger(t.Context(), inbound{Text: "/run"}); !errors.Is(err, boom) {
		t.Fatalf("matchTrigger error = %v, want the list error", err)
	}
	// Unwired triggers never match and never list.
	r.svc = &Service{log: discardLog(), now: time.Now, trigList: map[string]cachedTriggers{}}
	if _, ok, err := r.matchTrigger(t.Context(), inbound{Text: "/run"}); ok || err != nil {
		t.Fatalf("unwired matchTrigger = %v %v", ok, err)
	}
}

func TestChannelTriggersCache(t *testing.T) {
	calls := map[string]int{}
	s := triggerService(func(_ context.Context, id string) ([]ChannelTrigger, error) {
		calls[id]++
		return []ChannelTrigger{{TriggerID: id}}, nil
	})
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for range 3 {
		if ts, _ := s.channelTriggers(t.Context(), "a"); len(ts) != 1 || ts[0].TriggerID != "a" {
			t.Fatalf("triggers = %+v", ts)
		}
	}
	_, _ = s.channelTriggers(t.Context(), "b")
	if calls["a"] != 1 || calls["b"] != 1 {
		t.Fatalf("list calls = %v, want one per channel inside the TTL", calls)
	}
	now = now.Add(triggerTTL)
	_, _ = s.channelTriggers(t.Context(), "a")
	if calls["a"] != 2 {
		t.Fatalf("list calls after the TTL = %d, want 2", calls["a"])
	}
}
