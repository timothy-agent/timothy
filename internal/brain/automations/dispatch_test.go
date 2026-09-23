package automations

import (
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

func TestDecide(t *testing.T) {
	t.Parallel()
	base := decisionInput{Enabled: true, MaxRunsPerHour: 6, Concurrency: ConcurrencySkip, MaxConcurrent: 1}
	with := func(f func(*decisionInput)) decisionInput {
		in := base
		f(&in)
		return in
	}
	cases := []struct {
		name string
		in   decisionInput
		want decision
	}{
		{"disabled", with(func(in *decisionInput) { in.Enabled = false }), decision{Status: RunSkipped, SkipReason: SkipDisabled}},
		{"expired", with(func(in *decisionInput) { in.Expired = true }), decision{Status: RunSkipped, SkipReason: SkipDisabled}},
		{"disabled beats rate limit", with(func(in *decisionInput) { in.Enabled = false; in.RunsLastHour = 9 }), decision{Status: RunSkipped, SkipReason: SkipDisabled}},
		{"rate limited at cap", with(func(in *decisionInput) { in.RunsLastHour = 6 }), decision{Status: RunSkipped, SkipReason: SkipRateLimited}},
		{"rate limit beats policy", with(func(in *decisionInput) { in.RunsLastHour = 7; in.Active = 1 }), decision{Status: RunSkipped, SkipReason: SkipRateLimited}},
		{"under rate limit", with(func(in *decisionInput) { in.RunsLastHour = 5 }), decision{Status: RunStarting}},
		{"skip idle", base, decision{Status: RunStarting}},
		{"skip active", with(func(in *decisionInput) { in.Active = 1 }), decision{Status: RunSkipped, SkipReason: SkipActiveRun}},
		{"unknown policy acts as skip", with(func(in *decisionInput) { in.Concurrency = ""; in.Active = 1 }), decision{Status: RunSkipped, SkipReason: SkipActiveRun}},
		{"queue idle", with(func(in *decisionInput) { in.Concurrency = ConcurrencyQueue }), decision{Status: RunStarting}},
		{"queue active", with(func(in *decisionInput) { in.Concurrency = ConcurrencyQueue; in.Active = 1 }), decision{Status: RunQueued, Supersede: true}},
		{"parallel 0 of 3", with(func(in *decisionInput) { in.Concurrency = ConcurrencyParallel; in.MaxConcurrent = 3 }), decision{Status: RunStarting}},
		{"parallel 1 of 3", with(func(in *decisionInput) { in.Concurrency = ConcurrencyParallel; in.MaxConcurrent = 3; in.Active = 1 }), decision{Status: RunStarting}},
		{"parallel 2 of 3", with(func(in *decisionInput) { in.Concurrency = ConcurrencyParallel; in.MaxConcurrent = 3; in.Active = 2 }), decision{Status: RunStarting}},
		{"parallel 3 of 3", with(func(in *decisionInput) { in.Concurrency = ConcurrencyParallel; in.MaxConcurrent = 3; in.Active = 3 }), decision{Status: RunQueued, Supersede: true}},
		{"parallel 2 of 2", with(func(in *decisionInput) { in.Concurrency = ConcurrencyParallel; in.MaxConcurrent = 2; in.Active = 2 }), decision{Status: RunQueued, Supersede: true}},
	}
	for _, c := range cases {
		if got := decide(c.in); got != c.want {
			t.Errorf("%s: decide = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func mustSchedule(t *testing.T, expr string) cron.Schedule {
	t.Helper()
	s, err := cron.ParseStandard(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	return s
}

func amsterdam(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	return loc
}

func TestWalkDueDST(t *testing.T) {
	t.Parallel()
	ams := amsterdam(t)
	nine := mustSchedule(t, "0 9 * * *")
	cases := []struct {
		name   string
		anchor time.Time
		want   time.Time
		offset int
	}{
		// 2026-03-29: clocks jump 02:00 CET to 03:00 CEST.
		{"spring forward", time.Date(2026, 3, 28, 9, 0, 0, 0, ams), time.Date(2026, 3, 29, 9, 0, 0, 0, ams), 2 * 3600},
		// 2026-10-25: clocks fall back 03:00 CEST to 02:00 CET.
		{"fall back", time.Date(2026, 10, 24, 9, 0, 0, 0, ams), time.Date(2026, 10, 25, 9, 0, 0, 0, ams), 3600},
	}
	for _, c := range cases {
		w := walkDue(nine, c.anchor, c.want.Add(time.Minute))
		if !w.Fire || !w.Newest.Equal(c.want) || w.Skipped != 0 {
			t.Errorf("%s: walk = %+v, want fire at %s", c.name, w, c.want)
		}
		if _, off := w.Newest.Zone(); off != c.offset {
			t.Errorf("%s: boundary offset = %d, want %d", c.name, off, c.offset)
		}
		if got := w.Newest.UTC().Hour(); got != 9-c.offset/3600 {
			t.Errorf("%s: boundary UTC hour = %d, want %d", c.name, got, 9-c.offset/3600)
		}
	}

	// No 02:30 exists on 2026-03-29 in Amsterdam: robfig/cron skips that
	// day and the next boundary is 2026-03-30 02:30 CEST.
	half := mustSchedule(t, "30 2 * * *")
	anchor := time.Date(2026, 3, 28, 2, 30, 0, 0, ams)
	w := walkDue(half, anchor, time.Date(2026, 3, 29, 12, 0, 0, 0, ams))
	if !w.Newest.IsZero() {
		t.Fatalf("spring-forward day walk = %+v, want nothing due", w)
	}
	want := time.Date(2026, 3, 30, 2, 30, 0, 0, ams)
	if next := half.Next(anchor); !next.Equal(want) {
		t.Fatalf("next after 2026-03-28 02:30 = %s, want %s", next, want)
	}
}

func TestWalkDue(t *testing.T) {
	t.Parallel()
	hourly := mustSchedule(t, "0 * * * *")
	at := func(h, m int) time.Time { return time.Date(2026, 9, 23, h, m, 0, 0, time.UTC) }

	if w := walkDue(hourly, at(9, 0), at(9, 30)); !w.Newest.IsZero() || w.Fire || w.Skipped != 0 {
		t.Fatalf("anchor on a boundary = %+v, want nothing due", w)
	}
	if w := walkDue(hourly, at(9, 0), at(10, 0)); !w.Fire || !w.Newest.Equal(at(10, 0)) || w.Skipped != 0 {
		t.Fatalf("exactly at a boundary = %+v, want one fire", w)
	}
	// Three missed boundaries within three hours: the newest fires, two skip.
	if w := walkDue(hourly, at(8, 0), at(11, 0)); !w.Fire || !w.Newest.Equal(at(11, 0)) || w.Skipped != 2 {
		t.Fatalf("three missed = %+v, want fire at 11:00 and 2 skipped", w)
	}
	// The only due boundary is older than grace: skip, never fire.
	daily := mustSchedule(t, "0 9 * * *")
	if w := walkDue(daily, at(8, 0), at(10, 30)); w.Fire || !w.Newest.Equal(at(9, 0)) || w.Skipped != 1 {
		t.Fatalf("stale boundary = %+v, want skip only", w)
	}
	// Grace is inclusive at exactly one hour late.
	if w := walkDue(daily, at(8, 0), at(10, 0)); !w.Fire {
		t.Fatalf("one hour late = %+v, want fire", w)
	}
}

func TestWalkDueCap(t *testing.T) {
	t.Parallel()
	minutely := mustSchedule(t, "* * * * *")
	now := time.Date(2026, 9, 23, 12, 0, 30, 0, time.UTC)
	w := walkDue(minutely, now.AddDate(0, 0, -30), now)
	if !w.Fire || !w.Newest.Equal(time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("30 day backlog = %+v, want fire at 12:00", w)
	}
	if w.Skipped < maxWalk {
		t.Fatalf("skipped = %d, want at least %d", w.Skipped, maxWalk)
	}
}

func TestOriginMissionID(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`{"automation_id":"a","origin_mission_id":"m1"}`: "m1",
		`{"automation_id":"a"}`:                           "",
		`not json`:                                        "",
	}
	for payload, want := range cases {
		if got := originMissionID(json.RawMessage(payload)); got != want {
			t.Errorf("originMissionID(%s) = %q, want %q", payload, got, want)
		}
	}
}

func TestSelfTriggeredWithoutOrigin(t *testing.T) {
	t.Parallel()
	// No origin mission, or a malformed one, never reaches the database.
	for _, payload := range []string{`{"automation_id":"a"}`, `{"origin_mission_id":"not-a-uuid"}`} {
		self, err := selfTriggered(t.Context(), nil, "a", json.RawMessage(payload))
		if err != nil || self {
			t.Errorf("selfTriggered(%s) = %v, %v, want false", payload, self, err)
		}
	}
}

func TestMatchConnectorEvent(t *testing.T) {
	t.Parallel()
	cfg := ConnectorEventConfig{ConnectorID: "c1", Repo: "o/r", Events: []string{events.KindPROpened, events.KindPRLabeled}, Labels: []string{"timothy"}}
	base := events.ConnectorEventPayload{ConnectorID: "c1", Repo: "o/r", Kind: events.KindPROpened, Labels: []string{"bug", "timothy"}}
	for _, tc := range []struct {
		name   string
		cfg    func(*ConnectorEventConfig)
		p      func(*events.ConnectorEventPayload)
		expect bool
	}{
		{"match", nil, nil, true},
		{"repo case", nil, func(p *events.ConnectorEventPayload) { p.Repo = "O/R" }, true},
		{"connector case", func(c *ConnectorEventConfig) { c.ConnectorID = "C1" }, nil, true},
		{"label case", nil, func(p *events.ConnectorEventPayload) { p.Labels = []string{"Timothy"} }, true},
		{"other connector", nil, func(p *events.ConnectorEventPayload) { p.ConnectorID = "c2" }, false},
		{"other repo", nil, func(p *events.ConnectorEventPayload) { p.Repo = "o/other" }, false},
		{"kind not listed", nil, func(p *events.ConnectorEventPayload) { p.Kind = events.KindIssueComment }, false},
		{"no label overlap", nil, func(p *events.ConnectorEventPayload) { p.Labels = []string{"bug"} }, false},
		{"no labels on event", nil, func(p *events.ConnectorEventPayload) { p.Labels = nil }, false},
		{"empty label filter", func(c *ConnectorEventConfig) { c.Labels = nil }, func(p *events.ConnectorEventPayload) { p.Labels = nil }, true},
	} {
		c, p := cfg, base
		c.Labels = append([]string(nil), cfg.Labels...)
		if tc.cfg != nil {
			tc.cfg(&c)
		}
		if tc.p != nil {
			tc.p(&p)
		}
		if got := matchConnectorEvent(c, p); got != tc.expect {
			t.Errorf("%s: matchConnectorEvent = %v, want %v", tc.name, got, tc.expect)
		}
	}
}

func TestDispatcherKindsIncludeConnectorKinds(t *testing.T) {
	t.Parallel()
	kinds := NewDispatcher(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Kinds()
	for _, k := range append(events.ConnectorKinds(), events.KindCronDue, events.KindRunNow, events.KindMissionDone, events.KindMissionFailed) {
		if !slices.Contains(kinds, k) {
			t.Errorf("Kinds() lacks %q", k)
		}
	}
}

// TestHandleSkipsSelfConnectorEvent: a self-authored event returns
// before any query, so a nil tx is never touched.
func TestHandleSkipsSelfConnectorEvent(t *testing.T) {
	t.Parallel()
	ev, err := events.ConnectorEvent(events.ConnectorEventPayload{Provider: "github", ConnectorID: "c1", Repo: "o/r", Kind: events.KindPROpened, ProviderEventID: "9", Self: true})
	if err != nil {
		t.Fatalf("ConnectorEvent: %v", err)
	}
	d := NewDispatcher(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := d.Handle(t.Context(), nil, ev); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}
