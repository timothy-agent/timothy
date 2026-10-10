package extract

import (
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/store"
)

func TestMergedMemory(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(48 * time.Hour)
	tests := []struct {
		name      string
		members   []store.Memory
		wantActor string
		wantAt    time.Time
		wantConf  float32
	}{
		{
			name: "user beats agent regardless of order",
			members: []store.Memory{
				{Actor: "agent", Confidence: 0.6, LastConfirmedAt: t2},
				{Actor: store.ActorUser, Confidence: 0.9, LastConfirmedAt: t1},
			},
			wantActor: store.ActorUser, wantAt: t2, wantConf: 0.9,
		},
		{
			name: "all agent stays agent",
			members: []store.Memory{
				{Actor: "agent", Confidence: 0.8, LastConfirmedAt: t1},
				{Actor: "agent", Confidence: 0.7, LastConfirmedAt: t2},
			},
			wantActor: "agent", wantAt: t2, wantConf: 0.8,
		},
		{
			name: "three members take the latest time",
			members: []store.Memory{
				{Actor: "agent", Confidence: 0.5, LastConfirmedAt: t1},
				{Actor: "agent", Confidence: 0.5, LastConfirmedAt: t2},
				{Actor: store.ActorUser, Confidence: 0.4, LastConfirmedAt: t1},
			},
			wantActor: store.ActorUser, wantAt: t2, wantConf: 0.5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mergedMemory(tc.members, "merged", nil)
			if got.Actor != tc.wantActor || !got.LastConfirmedAt.Equal(tc.wantAt) || got.Confidence != tc.wantConf {
				t.Fatalf("got actor=%q at=%v conf=%v, want %q %v %v",
					got.Actor, got.LastConfirmedAt, got.Confidence, tc.wantActor, tc.wantAt, tc.wantConf)
			}
		})
	}
}

func TestMergeGuardDetailLoss(t *testing.T) {
	t.Parallel()
	// 100 runes of a repeated word: any prefix keeps the single token.
	longest := strings.Repeat("word ", 20)
	if got := mergeGuard([]string{longest}, strings.Repeat("word ", 12)); got != "shrink" {
		t.Fatalf("40%% shorter: got %q, want shrink", got)
	}
	if got := mergeGuard([]string{longest}, strings.Repeat("word ", 16)); got != "" {
		t.Fatalf("20%% shorter: got %q, want accepted", got)
	}
}
