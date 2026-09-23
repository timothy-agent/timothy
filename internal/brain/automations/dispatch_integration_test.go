//go:build integration

package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// dispatchHarness wires the ticker, dispatcher, drainer and starter
// over the real database with one fake clock. create defaults to
// inserting the mission row without driving it.
type dispatchHarness struct {
	t        *testing.T
	pool     *pgpool.Pool
	store    *Store
	events   *events.Store
	missions *missions.Store
	drainer  *events.Drainer
	starter  *Starter
	ticker   *Ticker
	tag      string
	agentID  string
	clock    time.Time
	loc      *time.Location
	createFn func(ctx context.Context, m missions.Mission) (string, error)
	// startedAt records each created mission's clock time.
	startedAt map[string]time.Time
}

// dispatchBase is a fixed clock origin on an hour boundary.
var dispatchBase = time.Date(2030, 1, 7, 10, 0, 0, 0, time.UTC)

func newDispatchHarness(t *testing.T) *dispatchHarness {
	t.Helper()
	s, pool := testStore(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &dispatchHarness{
		t: t, pool: pool, store: s, events: events.NewStore(pool), missions: missions.NewStore(pool, log),
		tag: runTag(), agentID: defaultAgentID(t, pool), clock: dispatchBase, loc: time.UTC, startedAt: map[string]time.Time{},
	}
	h.createFn = func(ctx context.Context, m missions.Mission) (string, error) { return h.missions.Create(ctx, m) }
	notifier := missions.NewNotifier(pool, "", nil, log)
	disp := NewDispatcher(notifier.NotifyMessage, log)
	disp.now = func() time.Time { return h.clock }
	h.drainer = events.NewDrainer(h.events, []events.Consumer{disp}, nil, log)
	h.starter = NewStarter(s, func(ctx context.Context, m missions.Mission) (string, error) {
		id, err := h.createFn(ctx, m)
		if err == nil {
			h.startedAt[id] = h.clock
		}
		return id, err
	}, missions.ResolveDeps{}, h.missions.ParentLineage, nil, log)
	h.starter.now = func() time.Time { return h.clock }
	h.ticker = NewTicker(s, h.events, nil, func(context.Context) *time.Location { return h.loc }, nil, log)
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		db, err := pool.Get()
		if err != nil {
			return
		}
		_, _ = db.Exec(cctx, `DELETE FROM events WHERE payload->>'automation_id' IN (SELECT id::text FROM automations WHERE name LIKE $1 || '%')
			OR payload->>'mission_id' IN (SELECT id::text FROM missions WHERE goal LIKE $1 || '%')`, h.tag)
	})
	return h
}

// create inserts an automation named tag+name with one trigger of
// kind/expr and the given concurrency ceilings.
func (h *dispatchHarness) create(name, cronExpr, concurrency string, maxConcurrent, maxRunsPerHour int) string {
	h.t.Helper()
	trigger := Trigger{Kind: TriggerManual, Enabled: true}
	if cronExpr != "" {
		trigger = Trigger{Kind: TriggerCron, Config: json.RawMessage(fmt.Sprintf(`{"expr":%q}`, cronExpr)), Enabled: true}
	}
	id, err := h.store.Create(h.t.Context(), Automation{
		Name: h.tag + name, AgentID: h.agentID,
		Action:      Action{Kind: ActionMission, Mission: &missions.MissionTemplate{Goal: h.tag + name + " goal", Kind: missions.KindGeneral, Light: true}},
		Concurrency: concurrency, MaxConcurrent: maxConcurrent, MaxRunsPerHour: maxRunsPerHour, Enabled: true,
		Triggers: []Trigger{trigger},
	})
	if err != nil {
		h.t.Fatalf("create automation: %v", err)
	}
	return id
}

func (h *dispatchHarness) exec(sql string, args ...any) {
	h.t.Helper()
	db, err := h.pool.Get()
	if err != nil {
		h.t.Fatalf("pool: %v", err)
	}
	if _, err := db.Exec(h.t.Context(), sql, args...); err != nil {
		h.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (h *dispatchHarness) count(sql string, args ...any) int {
	h.t.Helper()
	db, err := h.pool.Get()
	if err != nil {
		h.t.Fatalf("pool: %v", err)
	}
	var n int
	if err := db.QueryRow(h.t.Context(), sql, args...).Scan(&n); err != nil {
		h.t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// anchor sets every cron trigger of automationID to last fired at.
func (h *dispatchHarness) anchor(automationID string, at time.Time) {
	h.exec(`UPDATE automation_triggers SET state = jsonb_build_object('last_fired_at', $2::text) WHERE automation_id = $1`,
		automationID, at.UTC().Format(time.RFC3339))
}

func (h *dispatchHarness) tick() {
	h.t.Helper()
	if err := h.ticker.Tick(h.t.Context(), h.clock); err != nil {
		h.t.Fatalf("Tick: %v", err)
	}
}

func (h *dispatchHarness) drain() {
	h.t.Helper()
	for range 10 {
		n, err := h.drainer.Drain(h.t.Context())
		if err != nil {
			h.t.Fatalf("Drain: %v", err)
		}
		if n == 0 {
			return
		}
	}
}

func (h *dispatchHarness) pass() int {
	h.t.Helper()
	n, err := h.starter.Pass(h.t.Context())
	if err != nil {
		h.t.Fatalf("starter Pass: %v", err)
	}
	return n
}

func (h *dispatchHarness) addEvent(ev events.Event) {
	h.t.Helper()
	if _, err := h.events.Add(h.t.Context(), ev); err != nil {
		h.t.Fatalf("add event: %v", err)
	}
}

// fireNow records a run.now for automationID and drains it; the clock
// advances a second so run order is deterministic.
func (h *dispatchHarness) fireNow(automationID string) {
	h.t.Helper()
	h.clock = h.clock.Add(time.Second)
	ev, err := events.RunNow(automationID, h.clock)
	if err != nil {
		h.t.Fatalf("RunNow: %v", err)
	}
	h.addEvent(ev)
	h.drain()
}

// finish records missionID's terminal event and drains it.
func (h *dispatchHarness) finish(missionID string, failed bool) {
	h.t.Helper()
	phase := "done"
	if failed {
		phase = "failed"
	}
	ev, err := events.MissionTerminal(events.MissionPayload{MissionID: missionID, Phase: phase, OriginKind: missions.OriginAutomation, Unattended: true})
	if err != nil {
		h.t.Fatalf("MissionTerminal: %v", err)
	}
	h.addEvent(ev)
	h.drain()
}

// runs returns automationID's runs, oldest first.
func (h *dispatchHarness) runs(automationID string) []Run {
	h.t.Helper()
	rs, err := h.store.ListRuns(h.t.Context(), automationID, 200)
	if err != nil {
		h.t.Fatalf("ListRuns: %v", err)
	}
	for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
		rs[i], rs[j] = rs[j], rs[i]
	}
	return rs
}

func statuses(rs []Run) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Status
		if r.SkipReason != "" {
			out[i] += "/" + r.SkipReason
		}
	}
	return out
}

func (h *dispatchHarness) wantStatuses(automationID string, want ...string) []Run {
	h.t.Helper()
	rs := h.runs(automationID)
	got := statuses(rs)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		h.t.Fatalf("run statuses = %v, want %v", got, want)
	}
	return rs
}

func (h *dispatchHarness) backlog() int {
	return h.count(`SELECT count(*) FROM events WHERE processed_at IS NULL`)
}

func TestDispatchCronEndToEnd(t *testing.T) {
	h := newDispatchHarness(t)
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	h.loc = ams
	id := h.create("cron-e2e", "* * * * *", ConcurrencySkip, 1, 6)
	h.anchor(id, dispatchBase.Add(-time.Minute))

	h.clock = dispatchBase.Add(5 * time.Second)
	h.tick()
	var dedup, payload string
	db, _ := h.pool.Get()
	if err := db.QueryRow(t.Context(), `SELECT dedup_key, payload::text FROM events WHERE source = 'cron' AND payload->>'automation_id' = $1`, id).
		Scan(&dedup, &payload); err != nil {
		t.Fatalf("cron event: %v", err)
	}
	a, _ := h.store.Get(t.Context(), id)
	if dedup != a.Triggers[0].ID+"|2030-01-07T10:00:00Z" {
		t.Fatalf("event dedup_key = %q", dedup)
	}
	var p events.CronDuePayload
	_ = json.Unmarshal([]byte(payload), &p)
	if p.Boundary.Format(time.RFC3339) != "2030-01-07T11:00:00+01:00" || p.TriggerID != a.Triggers[0].ID {
		t.Fatalf("event payload = %s", payload)
	}

	h.drain()
	rs := h.wantStatuses(id, RunStarting)
	if rs[0].DedupKey != "2030-01-07T11:00:00+01:00" || rs[0].TriggerID == nil || *rs[0].TriggerID != a.Triggers[0].ID || rs[0].EventID == nil {
		t.Fatalf("run = %+v", rs[0])
	}
	if n := h.pass(); n != 1 {
		t.Fatalf("starter pass handled %d runs, want 1", n)
	}
	rs = h.wantStatuses(id, RunRunning)
	m, err := h.missions.Get(t.Context(), rs[0].MissionID)
	if err != nil {
		t.Fatalf("mission: %v", err)
	}
	if m.OriginKind != missions.OriginAutomation || !m.Unattended || m.AutomationRunID != rs[0].ID || m.AgentID != h.agentID || m.Name != h.tag+"cron-e2e" {
		t.Fatalf("mission = origin %q unattended %v run %q agent %q name %q", m.OriginKind, m.Unattended, m.AutomationRunID, m.AgentID, m.Name)
	}

	// Same minute again: nothing is due.
	h.clock = dispatchBase.Add(30 * time.Second)
	h.tick()
	if n := h.count(`SELECT count(*) FROM events WHERE source = 'cron' AND payload->>'automation_id' = $1`, id); n != 1 {
		t.Fatalf("cron events after a second tick = %d, want 1", n)
	}
	// Redelivering the same event never makes a second run.
	h.exec(`UPDATE events SET processed_at = NULL WHERE source = 'cron' AND payload->>'automation_id' = $1`, id)
	h.drain()
	h.wantStatuses(id, RunRunning)
	if n := h.pass(); n != 0 {
		t.Fatalf("starter pass after redelivery handled %d runs, want 0", n)
	}
	if n := h.count(`SELECT count(*) FROM missions WHERE automation_run_id = $1`, rs[0].ID); n != 1 {
		t.Fatalf("missions for the run = %d, want 1", n)
	}
	var lastFired string
	_ = db.QueryRow(t.Context(), `SELECT state->>'last_fired_at' FROM automation_triggers WHERE automation_id = $1`, id).Scan(&lastFired)
	if lastFired != "2030-01-07T10:00:00Z" {
		t.Fatalf("last_fired_at = %q", lastFired)
	}
}

func TestDispatchCronBackfill(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.create("backfill", "0 * * * *", ConcurrencyParallel, 3, 6)
	h.anchor(id, dispatchBase.Add(-3*time.Hour))
	h.clock = dispatchBase.Add(time.Minute)
	h.tick()
	if n := h.count(`SELECT count(*) FROM events WHERE source = 'cron' AND payload->>'automation_id' = $1`, id); n != 1 {
		t.Fatalf("cron events after an outage = %d, want exactly one backfill", n)
	}
	var reason, lastFired string
	db, _ := h.pool.Get()
	_ = db.QueryRow(t.Context(), `SELECT state->>'skip_reason', state->>'last_fired_at' FROM automation_triggers WHERE automation_id = $1`, id).Scan(&reason, &lastFired)
	if reason != "backfill_grace" || lastFired != "2030-01-07T10:00:00Z" {
		t.Fatalf("state skip_reason %q last_fired_at %q", reason, lastFired)
	}
	h.drain()
	h.wantStatuses(id, RunStarting)
}

func TestDispatchManual(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.create("manual", "", ConcurrencySkip, 1, 6)
	h.fireNow(id)
	rs := h.wantStatuses(id, RunStarting)
	var ev map[string]string
	_ = json.Unmarshal(rs[0].Event, &ev)
	if rs[0].TriggerID != nil || ev["kind"] != events.KindRunNow || ev["source"] != events.SourceManual || ev["requested_at"] == "" {
		t.Fatalf("manual run = %+v event %v", rs[0], ev)
	}
	h.pass()
	h.wantStatuses(id, RunRunning)
}

func TestDispatchQueueCollapse(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.create("queue", "", ConcurrencyQueue, 1, 10)
	h.fireNow(id)
	h.pass()
	for range 3 {
		h.fireNow(id)
	}
	rs := h.wantStatuses(id, RunRunning, "skipped/superseded", "skipped/superseded", RunQueued)
	h.finish(rs[0].MissionID, false)
	rs = h.wantStatuses(id, RunDone, "skipped/superseded", "skipped/superseded", RunStarting)
	if rs[3].StartedAt == nil {
		t.Fatal("promoted run has no started_at")
	}
	h.pass()
	h.wantStatuses(id, RunDone, "skipped/superseded", "skipped/superseded", RunRunning)
	// Redelivered finalize is a no-op.
	h.exec(`UPDATE events SET processed_at = NULL WHERE source = 'mission' AND dedup_key = $1`, rs[0].MissionID)
	h.drain()
	h.wantStatuses(id, RunDone, "skipped/superseded", "skipped/superseded", RunRunning)
}

func TestDispatchParallelCap(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.create("parallel", "", ConcurrencyParallel, 2, 10)
	for range 3 {
		h.fireNow(id)
	}
	h.wantStatuses(id, RunStarting, RunStarting, RunQueued)
	h.pass()
	rs := h.wantStatuses(id, RunRunning, RunRunning, RunQueued)
	h.finish(rs[1].MissionID, true)
	h.wantStatuses(id, RunRunning, RunFailed, RunStarting)
}

func TestDispatchSkipAcrossTicks(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.create("skip", "0 * * * *", ConcurrencySkip, 1, 60)
	h.anchor(id, dispatchBase.Add(-time.Hour))
	h.clock = dispatchBase.Add(time.Minute)
	h.tick()
	h.drain()
	h.pass()
	for i := 1; i <= 5; i++ {
		h.clock = dispatchBase.Add(time.Duration(i)*time.Hour + time.Minute)
		h.tick()
		h.drain()
		h.pass()
	}
	h.wantStatuses(id, RunRunning, "skipped/active_run", "skipped/active_run", "skipped/active_run", "skipped/active_run", "skipped/active_run")
	if n := h.count(`SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND status = 'queued'`, id); n != 0 {
		t.Fatalf("queued runs = %d, want 0", n)
	}
	if n := h.backlog(); n != 0 {
		t.Fatalf("unprocessed events = %d, want 0", n)
	}
}

func TestDispatchRateLimit(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.create("rate", "", ConcurrencyParallel, 3, 2)
	for range 3 {
		h.fireNow(id)
	}
	h.wantStatuses(id, RunStarting, RunStarting, "skipped/rate_limited")
	// Skipped rows never count: the window reopens after an hour.
	h.clock = h.clock.Add(time.Hour)
	h.fireNow(id)
	h.wantStatuses(id, RunStarting, RunStarting, "skipped/rate_limited", RunStarting)
}

func TestDispatchCircuitBreaker(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.create("breaker", "", ConcurrencySkip, 1, 60)
	run := func(failed bool) string {
		h.fireNow(id)
		h.pass()
		rs := h.runs(id)
		last := rs[len(rs)-1]
		if last.Status != RunRunning {
			t.Fatalf("run status = %s/%s, want running", last.Status, last.SkipReason)
		}
		h.finish(last.MissionID, failed)
		return last.MissionID
	}
	run(true)
	run(false)
	a, _ := h.store.Get(t.Context(), id)
	if a.ConsecutiveFailures != 0 || !a.Enabled {
		t.Fatalf("after a done run: failures %d enabled %v, want reset", a.ConsecutiveFailures, a.Enabled)
	}
	run(true)
	run(true)
	third := run(true)
	a, _ = h.store.Get(t.Context(), id)
	if a.Enabled || a.ConsecutiveFailures != 3 || a.DisabledReason != "3 consecutive failed runs" {
		t.Fatalf("after 3 failures: enabled %v failures %d reason %q", a.Enabled, a.ConsecutiveFailures, a.DisabledReason)
	}
	if n := h.count(`SELECT count(*) FROM notifications WHERE kind = 'automation_disabled' AND mission_id IN
		(SELECT m.id FROM missions m JOIN automation_runs r ON r.id = m.automation_run_id WHERE r.automation_id = $1)`, id); n != 1 {
		t.Fatalf("automation_disabled notifications = %d, want 1", n)
	}
	var msg string
	db, _ := h.pool.Get()
	_ = db.QueryRow(t.Context(), `SELECT message FROM notifications WHERE kind = 'automation_disabled' AND mission_id = $1`, third).Scan(&msg)
	if msg != h.tag+"breaker was disabled after 3 consecutive failed runs" {
		t.Fatalf("notification message = %q", msg)
	}
	h.fireNow(id)
	rs := h.runs(id)
	if last := rs[len(rs)-1]; last.Status != RunSkipped || last.SkipReason != SkipDisabled {
		t.Fatalf("fourth fire = %s/%s, want skipped/disabled", last.Status, last.SkipReason)
	}
}

func TestStarterRecovery(t *testing.T) {
	h := newDispatchHarness(t)
	ctx := t.Context()
	id := h.create("recovery", "", ConcurrencySkip, 1, 60)
	now := h.clock
	runID, _, err := h.store.CreateRun(ctx, nil, Run{AutomationID: id, DedupKey: h.tag + "crash", Status: RunStarting, CreatedAt: now, StartedAt: &now})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if n := h.pass(); n != 1 {
		t.Fatalf("boot pass handled %d, want 1", n)
	}
	h.pass()
	if n := h.count(`SELECT count(*) FROM missions WHERE automation_run_id = $1`, runID); n != 1 {
		t.Fatalf("missions for the crashed run = %d, want exactly 1", n)
	}
	h.wantStatuses(id, RunRunning)

	// A mission created before the crash is adopted, never duplicated.
	adopted, _, _ := h.store.CreateRun(ctx, nil, Run{AutomationID: id, DedupKey: h.tag + "adopt", Status: RunStarting, CreatedAt: now.Add(time.Second), StartedAt: &now})
	existing, err := h.missions.Create(ctx, missions.Mission{Goal: h.tag + "adopted", Kind: missions.KindGeneral, AgentID: h.agentID, Flow: missions.FlowLight, AutomationRunID: adopted})
	if err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	h.pass()
	rs := h.runs(id)
	if rs[1].Status != RunRunning || rs[1].MissionID != existing {
		t.Fatalf("adopted run = %s mission %s, want running on %s", rs[1].Status, rs[1].MissionID, existing)
	}
	if n := h.count(`SELECT count(*) FROM missions WHERE automation_run_id = $1`, adopted); n != 1 {
		t.Fatalf("missions for the adopted run = %d, want 1", n)
	}
}

func TestStarterRouteUnusable(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.create("route", "", ConcurrencySkip, 1, 60)
	h.createFn = func(context.Context, missions.Mission) (string, error) {
		return "", &missions.RouteUnusableError{Route: "default", Reason: "provider cooling down"}
	}
	h.fireNow(id)
	h.pass()
	rs := h.wantStatuses(id, RunStarting)
	attempts := func() (n int, errText string) {
		var ev struct {
			StartAttempts int    `json:"start_attempts"`
			StartError    string `json:"start_error"`
		}
		_ = json.Unmarshal(h.runs(id)[0].Event, &ev)
		return ev.StartAttempts, ev.StartError
	}
	if n, e := attempts(); n != 1 || e == "" {
		t.Fatalf("start_attempts = %d error %q, want 1 with the error", n, e)
	}
	// Retries are spaced: an immediate pass does not count an attempt.
	h.pass()
	if n, _ := attempts(); n != 1 {
		t.Fatalf("start_attempts after an early pass = %d, want 1", n)
	}
	h.clock = h.clock.Add(startRetryEvery)
	h.pass()
	if n, _ := attempts(); n != 2 {
		t.Fatalf("start_attempts after the retry delay = %d, want 2", n)
	}
	// The last attempt fails the run and counts for the breaker.
	h.exec(`UPDATE automation_runs SET event = event || '{"start_attempts": 59}' WHERE id = $1`, rs[0].ID)
	h.clock = h.clock.Add(startRetryEvery)
	h.pass()
	h.wantStatuses(id, "failed/route_unusable")
	if a, _ := h.store.Get(t.Context(), id); a.ConsecutiveFailures != 1 {
		t.Fatalf("consecutive_failures = %d, want 1", a.ConsecutiveFailures)
	}

	// Any other create error fails at once.
	h.createFn = func(context.Context, missions.Mission) (string, error) { return "", errors.New("boom") }
	h.fireNow(id)
	h.pass()
	h.wantStatuses(id, "failed/route_unusable", "failed/create_failed")
	if a, _ := h.store.Get(t.Context(), id); a.ConsecutiveFailures != 2 {
		t.Fatalf("consecutive_failures = %d, want 2", a.ConsecutiveFailures)
	}
}

func TestDispatchDeletedAutomation(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.create("deleted", "", ConcurrencySkip, 1, 6)
	ev, _ := events.RunNow(id, h.clock)
	h.addEvent(ev)
	if err := h.store.Delete(t.Context(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	h.drain()
	if n := h.count(`SELECT count(*) FROM automation_runs WHERE automation_id = $1`, id); n != 0 {
		t.Fatalf("runs for a deleted automation = %d", n)
	}
	if n := h.count(`SELECT count(*) FROM events WHERE source = 'manual' AND dedup_key = $1 AND processed_at IS NOT NULL AND last_error IS NULL`, ev.DedupKey); n != 1 {
		t.Fatal("event for a deleted automation was not processed cleanly")
	}
}

func TestDispatchSelfTriggerGuard(t *testing.T) {
	h := newDispatchHarness(t)
	own := h.create("self", "", ConcurrencyParallel, 3, 60)
	other := h.create("other", "", ConcurrencyParallel, 3, 60)
	h.fireNow(own)
	h.pass()
	origin := h.runs(own)[0].MissionID
	for _, target := range []string{own, other} {
		raw, _ := json.Marshal(map[string]any{"automation_id": target, "requested_at": h.clock, "origin_mission_id": origin})
		h.addEvent(events.Event{Source: events.SourceManual, Kind: events.KindRunNow, DedupKey: h.tag + "self-" + target, Payload: raw})
	}
	h.drain()
	if n := len(h.runs(own)); n != 1 {
		t.Fatalf("own automation runs = %d, want 1: its own mission must not trigger it", n)
	}
	if n := len(h.runs(other)); n != 1 {
		t.Fatalf("other automation runs = %d, want 1", n)
	}
}

// TestDispatchNoBacklogGrowth drives an hourly cron through six
// simulated hours with 90 minute missions under skip and queue.
func TestDispatchNoBacklogGrowth(t *testing.T) {
	for _, policy := range []string{ConcurrencySkip, ConcurrencyQueue} {
		t.Run(policy, func(t *testing.T) {
			h := newDispatchHarness(t)
			id := h.create("backlog-"+policy, "0 * * * *", policy, 1, 6)
			h.anchor(id, dispatchBase.Add(-time.Hour))
			for step := 0; step < 24; step++ {
				h.clock = dispatchBase.Add(time.Duration(step) * 15 * time.Minute)
				for _, r := range h.runs(id) {
					if r.Status == RunRunning && !h.clock.Before(h.startedAt[r.MissionID].Add(90*time.Minute)) {
						h.finish(r.MissionID, false)
					}
				}
				h.tick()
				h.drain()
				h.pass()
				if n := h.backlog(); n != 0 {
					t.Fatalf("step %d: unprocessed events = %d", step, n)
				}
				active := h.count(`SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND status IN ('starting', 'running')`, id)
				queued := h.count(`SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND status = 'queued'`, id)
				if active > 1 || queued > 1 || (policy == ConcurrencySkip && queued != 0) {
					t.Fatalf("step %d: active %d queued %d", step, active, queued)
				}
			}
			rs := h.runs(id)
			if len(rs) != 6 {
				t.Fatalf("runs = %v, want one per hourly boundary", statuses(rs))
			}
			if policy == ConcurrencySkip {
				want := []string{RunDone, "skipped/active_run", RunDone, "skipped/active_run", RunDone, "skipped/active_run"}
				if fmt.Sprint(statuses(rs)) != fmt.Sprint(want) {
					t.Fatalf("skip runs = %v, want %v", statuses(rs), want)
				}
			}
		})
	}
}
