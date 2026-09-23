//go:build integration

package missions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// permRow is one pending_permissions row as the D-118 tests read it.
type permRow struct {
	id, decision string
	resolved     bool
	carry        bool
}

func readPermRows(t *testing.T, store *Store, missionID string) []permRow {
	t.Helper()
	db, err := store.db.Get()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	rows, err := db.Query(t.Context(), `SELECT id, COALESCE(decision, ''), resolved_at IS NOT NULL, carry_over
		FROM pending_permissions WHERE mission_id = $1 ORDER BY created_at`, missionID)
	if err != nil {
		t.Fatalf("query pending_permissions: %v", err)
	}
	defer rows.Close()
	var out []permRow
	for rows.Next() {
		var r permRow
		if err := rows.Scan(&r.id, &r.decision, &r.resolved, &r.carry); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// attendedMissionWithSession creates an attended mission bound to a
// real session row, as provisioning would.
func attendedMissionWithSession(t *testing.T, store *Store, tag string) Mission {
	t.Helper()
	ctx := t.Context()
	db, _ := store.db.Get()
	var sessionID string
	if err := db.QueryRow(ctx, `INSERT INTO sessions (title) VALUES ($1) RETURNING id`, marker+tag).Scan(&sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	id, err := store.Create(ctx, Mission{Goal: marker + tag, Kind: KindGeneral, Route: "default", SessionID: sessionID, OriginKind: OriginAPI})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return m
}

func persistedBroker(store *Store) *loop.PermBroker {
	b := loop.NewPermBroker()
	b.SetStore(loop.NewPGPermStore(store.db), nil, nil)
	return b
}

// workerTurn runs one light worker turn that calls "gated" with args
// and then the mission_status sentinel, over a real loop.Agent and
// broker, with the production StorePermissionParker.
func workerTurn(ctx context.Context, store *Store, broker *loop.PermBroker, m Mission, args string) (*gatedExec, <-chan error) {
	gw := &originGateway{scripts: [][]stream.StreamEvent{
		originToolStep("gated", args),
		originToolStep(missionStatusToolName, `{"outcome":"done","evidence":"ok","final_output":"done"}`),
	}}
	exec := &gatedExec{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	agent := loop.NewAgent(gw, exec, askGatedPerms{}, originOutputs{}, originAudit{}, originEvents{}, broker, nil, log)
	agent.SetBaseTools(exec, []provider.ToolDef{{Name: "gated", Description: "gated", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	r := &nativeRunner{agent: agent, parker: NewStorePermissionParker(store, log), log: log}
	done := make(chan error, 1)
	go func() {
		verdict, _, err := r.RunWorker(ctx, m, WorkPacket{Goal: m.Goal, Light: true})
		if err == nil && verdict.Outcome != "done" {
			err = fmt.Errorf("verdict outcome %q, want done", verdict.Outcome)
		}
		done <- err
	}()
	return exec, done
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestMissionPermissionPromptSurvivesRestart covers issue #820 end to
// end short of a live model: an attended worker turn parks on an Ask
// and the prompt row exists while parked; a NEW broker over the same
// DB (the old one discarded, as on restart) resolves the same id with
// once; the mission's pending_permission clears with an answered
// event; the re-driven turn's same ask redeems the answer and runs the
// tool exactly once, with no second prompt.
func TestMissionPermissionPromptSurvivesRestart(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	tag := fmt.Sprintf("perm-restart %d", time.Now().UnixNano())
	m := attendedMissionWithSession(t, store, tag)
	args := fmt.Sprintf(`{"run":%q}`, tag)

	oldCtx, killOld := context.WithCancel(ctx)
	defer killOld()
	oldExec, oldDone := workerTurn(oldCtx, store, persistedBroker(store), m, args)

	var permID string
	waitUntil(t, "the parked prompt row and mission pending_permission", func() bool {
		rows := readPermRows(t, store, m.ID)
		got, err := store.Get(ctx, m.ID)
		if err != nil || len(rows) != 1 || rows[0].resolved || got.PendingPermission != rows[0].id {
			return false
		}
		permID = rows[0].id
		return true
	})

	restarted := persistedBroker(store)
	if !restarted.Resolve(ctx, permID, loop.DecideOnce) {
		t.Fatal("Resolve through the new broker = false, want true for the persisted prompt")
	}
	missionID, ok, err := store.ResolvePendingPermission(ctx, permID, loop.DecideOnce)
	if err != nil || !ok || missionID != m.ID {
		t.Fatalf("ResolvePendingPermission = %q, %v, %v; want %s, true, nil", missionID, ok, err, m.ID)
	}
	if rows := readPermRows(t, store, m.ID); len(rows) != 1 || rows[0].decision != loop.DecideOnce || !rows[0].carry {
		t.Fatalf("rows after restart answer = %+v, want one once row carried over", rows)
	}
	if got, _ := store.Get(ctx, m.ID); got.PendingPermission != "" {
		t.Fatalf("pending_permission = %q, want cleared", got.PendingPermission)
	}
	if again, _, _ := store.ResolvePendingPermission(ctx, permID, loop.DecideOnce); again != "" {
		t.Fatal("second ResolvePendingPermission matched a mission, want none")
	}
	if restarted.Resolve(ctx, permID, loop.DecideDeny) {
		t.Fatal("second Resolve = true, want false")
	}

	killOld()
	if err := <-oldDone; err == nil {
		t.Log("old turn ended without error after cancel")
	}
	if oldExec.calls != 0 {
		t.Fatalf("old turn executed the gated tool %d times, want 0", oldExec.calls)
	}

	newExec, newDone := workerTurn(ctx, store, restarted, m, args)
	select {
	case err := <-newDone:
		if err != nil {
			t.Fatalf("re-driven RunWorker: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("re-driven turn parked instead of redeeming the carried-over answer")
	}
	if newExec.calls != 1 {
		t.Fatalf("re-driven turn executed the gated tool %d times, want exactly 1", newExec.calls)
	}
	rows := readPermRows(t, store, m.ID)
	if len(rows) != 1 || rows[0].decision != loop.DecideOnce || rows[0].carry {
		t.Fatalf("rows after re-drive = %+v, want the one once row, carry-over consumed", rows)
	}

	events, err := store.Events(ctx, m.ID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	answered := 0
	for _, ev := range events {
		if ev.Kind == "mission.permission_answered" {
			answered++
			if !strings.Contains(string(ev.Payload), `"decision": "once"`) && !strings.Contains(string(ev.Payload), `"decision":"once"`) {
				t.Fatalf("permission_answered payload = %s, want decision once", ev.Payload)
			}
		}
	}
	if answered != 1 {
		t.Fatalf("permission_answered events = %d, want 1", answered)
	}
}

// TestPermissionAnsweredAfterRestartWithinWindowDoesNotResolveAsTimeout
// is the #820 regression: before D-118 a restart lost the prompt and
// the sweep denied it as timed out even when the operator answered in
// time. Now the answer lands, and a later sweep leaves it alone.
func TestPermissionAnsweredAfterRestartWithinWindowDoesNotResolveAsTimeout(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	m := attendedMissionWithSession(t, store, fmt.Sprintf("perm-window %d", time.Now().UnixNano()))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	id, _, err := persistedBroker(store).Create(ctx, loop.PendingPermission{
		SessionID: m.SessionID, MissionID: m.ID, Tool: "gated", Args: json.RawMessage(`{}`), OriginKind: loop.PermOriginMission,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.SetPendingPermission(ctx, m.ID, id, "gated", "{}", "safe", "r"); err != nil {
		t.Fatalf("SetPendingPermission: %v", err)
	}

	restarted := persistedBroker(store)
	if !restarted.Resolve(ctx, id, loop.DecideSession) {
		t.Fatal("answer within the window after restart = false (404), want true")
	}
	if _, ok, err := store.ResolvePendingPermission(ctx, id, loop.DecideSession); err != nil || !ok {
		t.Fatalf("ResolvePendingPermission: %v, %v", ok, err)
	}
	driver := &fakePermissionTimeoutDriver{}
	sweepPermissionTimeouts(ctx, driver, store, func(context.Context) int { return 60 }, restarted, nil, log)

	rows := readPermRows(t, store, m.ID)
	if len(rows) != 1 || rows[0].decision != loop.DecideSession {
		t.Fatalf("rows = %+v, want the session answer, not timeout", rows)
	}
	events, _ := store.Events(ctx, m.ID)
	for _, ev := range events {
		if ev.Kind == "mission.permission_answered" && strings.Contains(string(ev.Payload), "timed_out") {
			t.Fatalf("sweep recorded a timeout after the answer: %s", ev.Payload)
		}
	}
}

// TestSweepExpiresStalePendingPermissionRows covers D-118's cleanup: a
// mission prompt its mission no longer references and a chat prompt
// older than the chat timeout expire as timeout; a referenced mission
// prompt, a fresh chat prompt and a live one stay pending.
func TestSweepExpiresStalePendingPermissionRows(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	m := attendedMissionWithSession(t, store, fmt.Sprintf("perm-expire %d", time.Now().UnixNano()))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, _ := store.db.Get()

	old := persistedBroker(store)
	mk := func(missionID, tool string) string {
		t.Helper()
		p := loop.PendingPermission{SessionID: m.SessionID, MissionID: missionID, Tool: tool, Args: json.RawMessage(`{}`), OriginKind: loop.PermOriginChat}
		if missionID != "" {
			p.OriginKind = loop.PermOriginMission
		}
		id, _, err := old.Create(ctx, p)
		if err != nil {
			t.Fatalf("Create(%s): %v", tool, err)
		}
		return id
	}
	orphan := mk(m.ID, "orphan")
	referenced := mk(m.ID, "referenced")
	staleChat := mk("", "stale-chat")
	freshChat := mk("", "fresh-chat")
	if err := store.SetPendingPermission(ctx, m.ID, referenced, "referenced", "{}", "safe", "r"); err != nil {
		t.Fatalf("SetPendingPermission: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE pending_permissions SET created_at = now() - interval '1 hour' WHERE id = $1`, staleChat); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	restarted := persistedBroker(store)
	live, _, err := restarted.Create(ctx, loop.PendingPermission{SessionID: m.SessionID, MissionID: m.ID, Tool: "live", Args: json.RawMessage(`{}`), OriginKind: loop.PermOriginMission})
	if err != nil {
		t.Fatalf("Create(live): %v", err)
	}
	sweepPermissionTimeouts(ctx, &fakePermissionTimeoutDriver{}, store, nil, restarted, nil, log)

	want := map[string]string{orphan: loop.DecideTimeout, staleChat: loop.DecideTimeout, referenced: "", freshChat: "", live: ""}
	for id, decision := range want {
		var got string
		if err := db.QueryRow(ctx, `SELECT COALESCE(decision, '') FROM pending_permissions WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if got != decision {
			t.Fatalf("row %s decision = %q, want %q", id, got, decision)
		}
	}
	if _, err := db.Exec(ctx, `DELETE FROM pending_permissions WHERE id = ANY($1)`, []string{staleChat, freshChat}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
