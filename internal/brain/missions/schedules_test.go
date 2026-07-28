package missions

import (
	"errors"
	"testing"
	"time"
)

func TestValidateCron(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		cron    string
		wantErr bool
	}{
		{name: "daily at 9", cron: "0 9 * * *"},
		{name: "every 5 minutes", cron: "*/5 * * * *"},
		{name: "weekdays at 8", cron: "0 8 * * 1-5"},
		{name: "empty string", cron: "", wantErr: true},
		{name: "garbage", cron: "not a cron expression", wantErr: true},
		{name: "too few fields", cron: "* * *", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCron(tc.cron)
			if tc.wantErr {
				if err == nil || !errors.Is(err, ErrBadCron) {
					t.Fatalf("ValidateCron(%q) = %v, want ErrBadCron", tc.cron, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateCron(%q): %v", tc.cron, err)
			}
		})
	}
}

func TestNextRun(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)

	next := NextRun("0 9 * * *", now)
	want := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("NextRun = %v, want %v", next, want)
	}

	if got := NextRun("garbage", now); !got.IsZero() {
		t.Fatalf("NextRun with invalid cron = %v, want zero time", got)
	}
}
