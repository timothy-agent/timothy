package missions

import (
	"testing"
	"time"
)

func TestDueDecision(t *testing.T) {
	// "0 9 * * *" = every day at 09:00.
	const dailyAt9 = "0 9 * * *"

	cases := []struct {
		name   string
		cron   string
		anchor time.Time
		now    time.Time
		grace  time.Duration
		want   decision
	}{
		{
			name:   "on-time fire: now is exactly at the next boundary",
			cron:   dailyAt9,
			anchor: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
			grace:  time.Hour,
			want:   decisionFire,
		},
		{
			name:   "not yet due",
			cron:   dailyAt9,
			anchor: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC),
			grace:  time.Hour,
			want:   decisionSkip,
		},
		{
			name:   "misfire within grace still fires",
			cron:   dailyAt9,
			anchor: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC), // 30 min late
			grace:  time.Hour,
			want:   decisionFire,
		},
		{
			name:   "misfire beyond grace skips but caller still advances last_run",
			cron:   dailyAt9,
			anchor: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC), // over a day late
			grace:  time.Hour,
			want:   decisionBackfillSkip,
		},
		{
			name:   "never-run schedule fires on its first eligible boundary",
			cron:   dailyAt9,
			anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), // e.g. schedule created at midnight
			now:    time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			grace:  time.Hour,
			want:   decisionFire,
		},
		{
			name:   "invalid cron expression errors",
			cron:   "not a cron expression",
			anchor: time.Now(),
			now:    time.Now(),
			grace:  time.Hour,
			want:   decisionSkip,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dueDecision(tc.cron, tc.anchor, tc.now, tc.grace)
			if tc.name == "invalid cron expression errors" {
				if err == nil {
					t.Fatal("dueDecision accepted an invalid cron expression")
				}
				return
			}
			if err != nil {
				t.Fatalf("dueDecision: %v", err)
			}
			if got != tc.want {
				t.Fatalf("dueDecision = %v, want %v", got, tc.want)
			}
		})
	}
}
