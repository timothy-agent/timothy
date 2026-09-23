//go:build integration

package gitevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/automations"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

// fakeGitHub serves /user, one repo's event feed with an ETag and
// X-Poll-Interval, and its workflow runs.
type fakeGitHub struct {
	mu          sync.Mutex
	feed        string
	runs        string
	header      http.Header
	status      int
	ifNoneMatch []string
	auth        []string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	for k, v := range f.header {
		w.Header()[k] = v
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	switch r.URL.Path {
	case "/user":
		_, _ = w.Write([]byte(`{"login":"timothy-bot","id":1}`))
	case "/repos/o/r/events":
		f.ifNoneMatch = append(f.ifNoneMatch, r.Header.Get("If-None-Match"))
		w.Header().Set("X-Poll-Interval", "1")
		if r.Header.Get("If-None-Match") == `W/"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"v1"`)
		_, _ = w.Write([]byte(f.feed))
	case "/repos/o/r/actions/runs":
		_, _ = w.Write([]byte(f.runs))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type pollHarness struct {
	t           *testing.T
	pool        *pgpool.Pool
	tag         string
	connectorID string
	automations *automations.Store
	events      *events.Store
	poller      *Poller
	drainer     *events.Drainer
	kicks       int
}

func newPollHarness(t *testing.T, gh http.Handler) *pollHarness {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	poolCtx, poolCancel := context.WithCancel(context.Background())
	t.Cleanup(poolCancel)
	pool := pgpool.New(poolCtx, dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, _ := pool.Get()
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	h := &pollHarness{t: t, pool: pool, tag: fmt.Sprintf("itest-gitevents-%d-", time.Now().UnixNano()),
		automations: automations.NewStore(pool), events: events.NewStore(pool)}
	if err := db.QueryRow(t.Context(), `INSERT INTO connectors (name, kind, credential_ref, enabled) VALUES ($1, 'github', 'GH_PAT', true) RETURNING id`,
		h.tag+"gh").Scan(&h.connectorID); err != nil {
		t.Fatalf("insert connector: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = db.Exec(cctx, `DELETE FROM automations WHERE name LIKE $1 || '%'`, h.tag)
		_, _ = db.Exec(cctx, `DELETE FROM events WHERE source = 'connector' AND dedup_key LIKE 'github:' || $1 || ':%'`, h.connectorID)
		_, _ = db.Exec(cctx, `DELETE FROM connectors WHERE id = $1`, h.connectorID)
	})

	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	connStore := connectors.NewStore(pool, log)
	resolve := func(_ context.Context, ref string) (string, error) {
		if ref != "GH_PAT" {
			return "", errors.New("unknown ref")
		}
		return "fake-token", nil
	}
	reader := func(ctx context.Context, id string) (*connectors.GitHubEventReader, error) {
		if id != h.connectorID {
			return nil, errors.New("not this test's connector")
		}
		c, err := connStore.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		return connectors.NewGitHubEventReader(c, resolve, srv.Client(), srv.URL), nil
	}
	h.poller = NewPoller(reader, h.automations.ConnectorEventWatches, NewStore(pool), h.events, nil,
		func(context.Context) time.Duration { return 30 * time.Second }, func() { h.kicks++ }, nil, log)
	h.drainer = events.NewDrainer(h.events, []events.Consumer{automations.NewDispatcher(nil, log)}, nil, log)
	return h
}

// createAutomation adds an enabled automation with one connector_event
// trigger on pr.opened labeled timothy.
func (h *pollHarness) createAutomation(name string) string {
	h.t.Helper()
	agentID := h.scalar(`SELECT id::text FROM agents WHERE is_default LIMIT 1`)
	cfg := fmt.Sprintf(`{"connector_id":%q,"repo":"o/r","events":["pr.opened"],"labels":["timothy"]}`, h.connectorID)
	id, err := h.automations.Create(h.t.Context(), automations.Automation{
		Name: h.tag + name, AgentID: agentID,
		Action:      automations.Action{Kind: automations.ActionMission, Mission: &missions.MissionTemplate{Goal: h.tag + "goal", Kind: missions.KindGeneral, Light: true}},
		Concurrency: automations.ConcurrencySkip, MaxConcurrent: 1, MaxRunsPerHour: 6, Enabled: true,
		Triggers: []automations.Trigger{{Kind: automations.TriggerConnectorEvent, Config: json.RawMessage(cfg), Enabled: true}},
	})
	if err != nil {
		h.t.Fatalf("create automation: %v", err)
	}
	return id
}

func (h *pollHarness) scalar(sql string, args ...any) string {
	h.t.Helper()
	db, _ := h.pool.Get()
	var s string
	if err := db.QueryRow(h.t.Context(), sql, args...).Scan(&s); err != nil {
		h.t.Fatalf("query %q: %v", sql, err)
	}
	return s
}

func (h *pollHarness) count(sql string, args ...any) int {
	h.t.Helper()
	n, err := strconv.Atoi(h.scalar(sql, args...))
	if err != nil {
		h.t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func (h *pollHarness) tick(at time.Time) {
	h.t.Helper()
	if err := h.poller.Tick(h.t.Context(), at); err != nil {
		h.t.Fatalf("Tick: %v", err)
	}
}

func (h *pollHarness) drain() {
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

func prEvent(id, actor string, number int, labels string, at time.Time) string {
	return fmt.Sprintf(`{"id":%q,"type":"PullRequestEvent","actor":{"login":%q},"created_at":%q,
		"payload":{"action":"opened","number":%d,"pull_request":{"number":%d,"title":"PR %d","labels":%s}}}`,
		id, actor, at.UTC().Format(time.RFC3339), number, number, number, labels)
}

func TestPollerTwoTicksDispatchAndSweep(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second)
	gh := &fakeGitHub{
		feed: "[" + prEvent("1003", "alice", 7, `[{"name":"timothy"}]`, start.Add(time.Minute)) + "," +
			prEvent("1002", "alice", 8, `[]`, start.Add(time.Minute)) + "," +
			prEvent("1001", "Timothy-Bot", 9, `[{"name":"timothy"}]`, start.Add(time.Minute)) + "," +
			prEvent("1000", "alice", 6, `[{"name":"timothy"}]`, start.Add(-time.Hour)) + "]",
		runs: fmt.Sprintf(`{"workflow_runs":[{"id":55,"name":"ci","run_attempt":1,"status":"completed","conclusion":"success","created_at":%q,"actor":{"login":"alice"},"pull_requests":[{"number":7}]},
			{"id":56,"name":"ci","run_attempt":1,"status":"completed","conclusion":"success","created_at":%q,"actor":{"login":"alice"},"pull_requests":[]}]}`,
			start.Add(time.Minute).Format(time.RFC3339), start.Add(time.Minute).Format(time.RFC3339)),
	}
	h := newPollHarness(t, gh)
	automationID := h.createAutomation("watch")
	evCount := `SELECT count(*) FROM events WHERE source = 'connector' AND dedup_key LIKE 'github:' || $1 || ':%'`

	h.tick(start)
	if n := h.count(evCount, h.connectorID); n != 4 {
		t.Fatalf("events after tick 1 = %d, want 4 (three PRs and one linked run; the pre-watch PR is skipped)", n)
	}
	if n := h.count(evCount+` AND dedup_key LIKE '%:1000'`, h.connectorID); n != 0 {
		t.Fatal("event older than the watch was backfilled")
	}
	if n := h.count(evCount+` AND kind = 'check.completed' AND payload->>'conclusion' = 'success'`, h.connectorID); n != 1 {
		t.Fatalf("check.completed events = %d, want 1", n)
	}
	if n := h.count(evCount+` AND (payload->>'self')::boolean`, h.connectorID); n != 1 {
		t.Fatalf("self events = %d, want 1", n)
	}
	if h.kicks != 1 {
		t.Fatalf("kicks after tick 1 = %d, want 1", h.kicks)
	}
	cursor := `SELECT etag || '|' || last_event_id FROM github_poll_cursors WHERE connector_id = $1 AND repo = 'o/r'`
	if got := h.scalar(cursor, h.connectorID); got != `W/"v1"|1003` {
		t.Fatalf("cursor after tick 1 = %s", got)
	}

	h.tick(start.Add(10 * time.Second))
	if len(gh.ifNoneMatch) != 1 {
		t.Fatalf("feed requests before next_poll_at = %d, want 1 (interval respected)", len(gh.ifNoneMatch))
	}
	h.tick(start.Add(2 * time.Minute))
	if len(gh.ifNoneMatch) != 2 || gh.ifNoneMatch[1] != `W/"v1"` {
		t.Fatalf("If-None-Match = %q, want the second tick to send the stored etag", gh.ifNoneMatch)
	}
	if n := h.count(evCount, h.connectorID); n != 4 {
		t.Fatalf("events after tick 2 = %d, want exactly one per provider id", n)
	}
	if got := h.scalar(cursor, h.connectorID); got != `W/"v1"|1003` {
		t.Fatalf("cursor after 304 = %s, want unchanged", got)
	}
	for _, a := range gh.auth {
		if a != "Bearer fake-token" {
			t.Fatalf("Authorization = %q, want the resolved credential", a)
		}
	}

	h.drain()
	runs := `SELECT count(*) FROM automation_runs WHERE automation_id = $1`
	if n := h.count(runs, automationID); n != 1 {
		t.Fatalf("runs = %d, want 1 (labeled PR only; unlabeled and self PRs never match)", n)
	}
	if got := h.scalar(`SELECT status || '|' || (event->>'number') || '|' || (event->>'source') FROM automation_runs WHERE automation_id = $1`, automationID); got != "starting|7|connector" {
		t.Fatalf("run = %s, want a starting run for PR 7", got)
	}

	// Replay: the same delivery is dropped by the events UNIQUE, and a
	// copy under another dedup key collapses on the run dedup key.
	if _, inserted, err := h.events.AddIfNew(t.Context(), mustConnectorEvent(t, h.connectorID, "1003", 7)); err != nil || inserted {
		t.Fatalf("replayed AddIfNew inserted = %v, %v, want no insert", inserted, err)
	}
	replay := mustConnectorEvent(t, h.connectorID, "1003", 7)
	replay.DedupKey += ":replay"
	if _, _, err := h.events.AddIfNew(t.Context(), replay); err != nil {
		t.Fatalf("insert replay: %v", err)
	}
	h.drain()
	if n := h.count(runs, automationID); n != 1 {
		t.Fatalf("runs after replay = %d, want still 1", n)
	}

	if err := h.automations.Delete(t.Context(), automationID); err != nil {
		t.Fatalf("delete automation: %v", err)
	}
	h.tick(start.Add(5 * time.Minute))
	if n := h.count(`SELECT count(*) FROM github_poll_cursors WHERE connector_id = $1`, h.connectorID); n != 0 {
		t.Fatalf("cursors after the watch was deleted = %d, want 0", n)
	}
}

func mustConnectorEvent(t *testing.T, connectorID, id string, number int) events.Event {
	t.Helper()
	ev, err := events.ConnectorEvent(events.ConnectorEventPayload{Provider: "github", ConnectorID: connectorID, Repo: "o/r", Kind: events.KindPROpened,
		Action: "opened", Number: number, Author: "alice", Labels: []string{"timothy"}, ProviderEventID: id})
	if err != nil {
		t.Fatalf("ConnectorEvent: %v", err)
	}
	return ev
}

func TestPollerBacksOffNearRateLimit(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second)
	reset := start.Add(20 * time.Minute)
	gh := &fakeGitHub{feed: `[]`, runs: `{"workflow_runs":[]}`, header: http.Header{
		"X-Ratelimit-Remaining": {"10"}, "X-Ratelimit-Reset": {strconv.FormatInt(reset.Unix(), 10)},
	}}
	h := newPollHarness(t, gh)
	h.createAutomation("rate")
	h.tick(start)
	next := `SELECT to_char(next_poll_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM github_poll_cursors WHERE connector_id = $1`
	if got, want := h.scalar(next, h.connectorID), reset.Add(rateSlack).Format(time.RFC3339); got != want {
		t.Fatalf("next_poll_at = %s, want %s (reset plus slack)", got, want)
	}

	gh.mu.Lock()
	gh.status = http.StatusTooManyRequests
	gh.mu.Unlock()
	h.tick(reset.Add(time.Minute))
	if got, want := h.scalar(next, h.connectorID), reset.Add(time.Minute).Add(30*time.Second).Format(time.RFC3339); got != want {
		t.Fatalf("next_poll_at after 429 with a past reset = %s, want %s", got, want)
	}
}
