package gitevents

import (
	"context"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
)

func TestClampInterval(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		0: minInterval, 5 * time.Second: minInterval, 30 * time.Second: 30 * time.Second, 5 * time.Minute: 5 * time.Minute,
	} {
		if got := clampInterval(in); got != want {
			t.Errorf("clampInterval(%v) = %v, want %v", in, got, want)
		}
	}
	p := &Poller{interval: func(context.Context) time.Duration { return time.Second }}
	if got := p.pollInterval(t.Context()); got != minInterval {
		t.Errorf("pollInterval with 1s setting = %v, want the floor", got)
	}
	if got := (&Poller{}).pollInterval(t.Context()); got != defaultInterval {
		t.Errorf("pollInterval without a setting = %v, want %v", got, defaultInterval)
	}
}

func TestNextPollAtHonorsPollInterval(t *testing.T) {
	if got := nextPollAt(t0, time.Minute, 0); !got.Equal(t0.Add(time.Minute)) {
		t.Errorf("no X-Poll-Interval: %v", got)
	}
	if got := nextPollAt(t0, time.Minute, 5*time.Minute); !got.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("larger X-Poll-Interval: %v, want it honored", got)
	}
	if got := nextPollAt(t0, time.Minute, 10*time.Second); !got.Equal(t0.Add(time.Minute)) {
		t.Errorf("smaller X-Poll-Interval: %v, want the interval", got)
	}
}

func TestRateBackoff(t *testing.T) {
	reset := t0.Add(20 * time.Minute)
	for _, tc := range []struct {
		name string
		rate connectors.GitHubRate
		low  bool
		want time.Time
	}{
		{"unknown", connectors.GitHubRate{Remaining: -1}, false, t0.Add(time.Minute)},
		{"plenty", connectors.GitHubRate{Remaining: 4000, Reset: reset}, false, reset.Add(rateSlack)},
		{"at floor", connectors.GitHubRate{Remaining: rateFloor, Reset: reset}, false, reset.Add(rateSlack)},
		{"under floor", connectors.GitHubRate{Remaining: rateFloor - 1, Reset: reset}, true, reset.Add(rateSlack)},
		{"exhausted, reset past", connectors.GitHubRate{Remaining: 0, Reset: t0.Add(-time.Second)}, true, t0.Add(time.Minute)},
	} {
		if got := lowRate(tc.rate); got != tc.low {
			t.Errorf("%s: lowRate = %v, want %v", tc.name, got, tc.low)
		}
		if got := backoffUntil(t0, time.Minute, tc.rate); !got.Equal(tc.want) {
			t.Errorf("%s: backoffUntil = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestApplyFeedKeepsCursorOn304(t *testing.T) {
	c := Cursor{ETag: `W/"v1"`, LastEventID: "10"}
	applyFeed(&c, `W/"v1"`, "")
	if c.ETag != `W/"v1"` || c.LastEventID != "10" {
		t.Fatalf("304 changed the cursor: %+v", c)
	}
	applyFeed(&c, "", "")
	if c.ETag != `W/"v1"` {
		t.Fatalf("missing etag cleared the cursor: %+v", c)
	}
	applyFeed(&c, `W/"v2"`, "12")
	if c.ETag != `W/"v2"` || c.LastEventID != "12" {
		t.Fatalf("new page not recorded: %+v", c)
	}
}

func TestAdvanceRunsSince(t *testing.T) {
	if got := advanceRunsSince(t0, t0.Add(time.Minute)); !got.Equal(t0) {
		t.Errorf("within lookback: %v, want the first-sight start kept", got)
	}
	now := t0.Add(5 * time.Hour)
	if got := advanceRunsSince(t0, now); !got.Equal(now.Add(-runsLookback)) {
		t.Errorf("past lookback: %v, want now minus lookback", got)
	}
	later := now.Add(-time.Minute)
	if got := advanceRunsSince(later, now); !got.Equal(later) {
		t.Errorf("never moves back: %v", got)
	}
}
