package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeMissionStore is an in-memory missionLister + missionEventReader
// stand-in — no Postgres pool, table-tests Execute against scripted
// mission rows.
type fakeMissionStore struct {
	byID      map[string]MissionRecord
	listOrder []string
	events    map[string][]MissionEvent
	listErr   error
	getErr    error
}

func newFakeMissionStore() *fakeMissionStore {
	return &fakeMissionStore{byID: map[string]MissionRecord{}, events: map[string][]MissionEvent{}}
}

func (f *fakeMissionStore) add(m MissionRecord) {
	f.byID[m.ID] = m
	f.listOrder = append(f.listOrder, m.ID)
}

func (f *fakeMissionStore) ListMissions(ctx context.Context, limit int) ([]MissionRecord, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]MissionRecord, 0, len(f.listOrder))
	for _, id := range f.listOrder {
		out = append(out, f.byID[id])
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeMissionStore) GetMission(ctx context.Context, id string) (MissionRecord, error) {
	if f.getErr != nil {
		return MissionRecord{}, f.getErr
	}
	m, ok := f.byID[id]
	if !ok {
		return MissionRecord{}, errors.New("not found")
	}
	return m, nil
}

func (f *fakeMissionStore) MissionEvents(ctx context.Context, id string) ([]MissionEvent, error) {
	return f.events[id], nil
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestGetMissionByID proves an id lookup returns that mission's full
// snapshot, including the latest mission.pr_opened event's url.
func TestGetMissionByID(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store.add(MissionRecord{
		ID: "m1", Name: "Fix login bug", Goal: "fix the login bug", Kind: "coding",
		Phase: "done", Status: "idle", Iteration: 3, RepoURL: "https://github.com/o/r.git",
		Branch: "mission/m1", CreatedAt: now, UpdatedAt: now, UnitsPassed: 2, UnitsTotal: 2,
	})
	store.events["m1"] = []MissionEvent{
		{Kind: "mission.pr_opened", Payload: mustMarshal(t, map[string]any{"url": "https://github.com/o/r/pull/1", "number": 1})},
	}

	tool := GetMission(store, store)
	out, err := tool.Execute(context.Background(), mustMarshal(t, map[string]string{"id": "m1"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var snap missionSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v (out=%s)", err, out)
	}
	if snap.ID != "m1" || snap.Phase != "done" || snap.UnitsPassed != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.PRURL != "https://github.com/o/r/pull/1" {
		t.Fatalf("PRURL = %q, want the pr_opened event's url", snap.PRURL)
	}
}

// TestGetMissionByQueryUnique proves a query substring matching
// exactly one mission's name/goal resolves to that mission.
func TestGetMissionByQueryUnique(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	store.add(MissionRecord{ID: "m1", Name: "Invoice PDF export", Goal: "export invoices as pdf"})
	store.add(MissionRecord{ID: "m2", Name: "Unrelated mission", Goal: "something else"})

	tool := GetMission(store, store)
	out, err := tool.Execute(context.Background(), mustMarshal(t, map[string]string{"query": "invoice"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var snap missionSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("unmarshal: %v (out=%s)", err, out)
	}
	if snap.ID != "m1" {
		t.Fatalf("resolved to %q, want m1", snap.ID)
	}
}

// TestGetMissionByQueryAmbiguous proves a query matching more than
// one mission returns a disambiguation list, not a guess.
func TestGetMissionByQueryAmbiguous(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	store.add(MissionRecord{ID: "m1", Name: "Export invoices", Goal: "export invoices as pdf"})
	store.add(MissionRecord{ID: "m2", Name: "Export contacts", Goal: "export contacts as csv"})

	tool := GetMission(store, store)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]string{"query": "export"}))
	if err == nil {
		t.Fatal("expected an ambiguous-match error")
	}
	if !strings.Contains(err.Error(), "m1") || !strings.Contains(err.Error(), "m2") {
		t.Fatalf("error %q should list both candidate ids", err.Error())
	}
}

// TestGetMissionRequiresIDOrQuery proves neither id nor query is
// rejected before touching the store at all.
func TestGetMissionRequiresIDOrQuery(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	tool := GetMission(store, store)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{}))
	if err == nil {
		t.Fatal("expected an id-or-query-required error")
	}
}

// TestGetMissionUnknownID proves an unknown id returns a clear
// error rather than a zero-value snapshot.
func TestGetMissionUnknownID(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	tool := GetMission(store, store)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]string{"id": "does-not-exist"}))
	if err == nil {
		t.Fatal("expected an error for an unknown mission id")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error %q should name the id", err.Error())
	}
}

// TestListMissions proves the list-mode path lists missions with the
// default limit applied.
func TestListMissions(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	for i := 0; i < 3; i++ {
		store.add(MissionRecord{ID: string(rune('a' + i)), Name: "mission", Phase: "execute", Status: "working"})
	}
	tool := ListMissions(store)
	out, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var items []missionListItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("unmarshal list: %v (out=%s)", err, out)
	}
	if len(items) != 3 {
		t.Fatalf("list = %d items, want 3", len(items))
	}
}

// TestListMissionsCapsLimit proves an over-large limit is capped, not
// passed straight to the store.
func TestListMissionsCapsLimit(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	var gotLimit int
	capping := missionLimitCapture{fakeMissionStore: store, capture: &gotLimit}
	tool := ListMissions(capping)
	if _, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"limit": 500})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotLimit != missionsListMaxLimit {
		t.Fatalf("limit passed to store = %d, want capped to %d", gotLimit, missionsListMaxLimit)
	}
}

// missionLimitCapture wraps fakeMissionStore to record the limit
// ListMissions receives, proving the tool's own cap logic runs before
// the store call.
type missionLimitCapture struct {
	*fakeMissionStore
	capture *int
}

func (c missionLimitCapture) ListMissions(ctx context.Context, limit int) ([]MissionRecord, error) {
	*c.capture = limit
	return c.fakeMissionStore.ListMissions(ctx, limit)
}

// fakeMissionCompleter scripts missionCompleter for
// push_mission_branch's table tests.
type fakeMissionCompleter struct {
	pushHost    string
	pushErr     error
	prURL       string
	prNumber    int
	prErr       error
	pushCalls   int
	openPRCalls int
}

func (f *fakeMissionCompleter) PushMissionBranch(ctx context.Context, id, token string) (string, error) {
	f.pushCalls++
	if f.pushErr != nil {
		return "", f.pushErr
	}
	return f.pushHost, nil
}

func (f *fakeMissionCompleter) OpenMissionPR(ctx context.Context, id, token string) (string, int, error) {
	f.openPRCalls++
	if f.prErr != nil {
		return "", 0, f.prErr
	}
	return f.prURL, f.prNumber, nil
}

func pushableRecord() MissionRecord {
	return MissionRecord{ID: "m1", Goal: "fix the bug", Kind: "coding", Branch: "mission/m1",
		RepoURL: "https://github.com/o/r.git", ConnectorID: "conn1"}
}

// TestPushMissionBranchHappyPath proves a pushable mission pushes through
// the completer and reports the host.
func TestPushMissionBranchHappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	store.add(pushableRecord())
	completer := &fakeMissionCompleter{pushHost: "github.com"}
	resolve := func(ctx context.Context, connectorID string) (string, error) { return "tok", nil }

	tool := PushMissionBranch(store, completer, resolve)
	out, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"id": "m1"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "github.com") {
		t.Fatalf("out = %q, want it to mention the pushed host", out)
	}
	if completer.pushCalls != 1 || completer.openPRCalls != 0 {
		t.Fatalf("pushCalls=%d openPRCalls=%d, want push only", completer.pushCalls, completer.openPRCalls)
	}
}

// TestPushMissionBranchOpenPR proves open_pr:true opens a PR instead of a
// plain push, reporting the PR url.
func TestPushMissionBranchOpenPR(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	store.add(pushableRecord())
	completer := &fakeMissionCompleter{prURL: "https://github.com/o/r/pull/2", prNumber: 2}
	resolve := func(ctx context.Context, connectorID string) (string, error) { return "tok", nil }

	tool := PushMissionBranch(store, completer, resolve)
	out, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"id": "m1", "open_pr": true}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "https://github.com/o/r/pull/2") {
		t.Fatalf("out = %q, want the PR url", out)
	}
	if completer.openPRCalls != 1 || completer.pushCalls != 0 {
		t.Fatalf("openPRCalls=%d pushCalls=%d, want open_pr only", completer.openPRCalls, completer.pushCalls)
	}
}

// TestPushMissionBranchNotPushable proves a mission NotPushable flags
// (precomputed onto the record) is refused before ever calling the
// completer.
func TestPushMissionBranchNotPushable(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	rec := pushableRecord()
	rec.NotPushableReason = "mission has no branch"
	store.add(rec)
	completer := &fakeMissionCompleter{}
	resolve := func(ctx context.Context, connectorID string) (string, error) { return "tok", nil }

	tool := PushMissionBranch(store, completer, resolve)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"id": "m1"}))
	if err == nil {
		t.Fatal("expected a not-pushable error")
	}
	if !strings.Contains(err.Error(), "mission has no branch") {
		t.Fatalf("error %q should carry the NotPushable reason", err.Error())
	}
	if completer.pushCalls != 0 {
		t.Fatalf("completer called %d times, want 0", completer.pushCalls)
	}
}

// TestPushMissionBranchNonGitHubMission proves a mission with no
// connector_id/repo_url (never cloned from GitHub) is refused before
// calling the completer.
func TestPushMissionBranchNonGitHubMission(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	store.add(MissionRecord{ID: "m1", Kind: "coding", Branch: "mission/m1"})
	completer := &fakeMissionCompleter{}
	resolve := func(ctx context.Context, connectorID string) (string, error) { return "tok", nil }

	tool := PushMissionBranch(store, completer, resolve)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"id": "m1"}))
	if err == nil {
		t.Fatal("expected a not-a-github-mission error")
	}
	if completer.pushCalls != 0 {
		t.Fatalf("completer called %d times, want 0", completer.pushCalls)
	}
}

// TestPushMissionBranchCompleterError proves a completer failure surfaces
// as the tool's own error.
func TestPushMissionBranchCompleterError(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	store.add(pushableRecord())
	completer := &fakeMissionCompleter{pushErr: errors.New("push rejected by remote")}
	resolve := func(ctx context.Context, connectorID string) (string, error) { return "tok", nil }

	tool := PushMissionBranch(store, completer, resolve)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"id": "m1"}))
	if err == nil {
		t.Fatal("expected the completer's push error to surface")
	}
	if !strings.Contains(err.Error(), "push rejected by remote") {
		t.Fatalf("error %q should wrap the completer's error", err.Error())
	}
}

// TestPushMissionBranchUnknownID proves an unknown mission id is refused
// before any token resolution or completer call.
func TestPushMissionBranchUnknownID(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	completer := &fakeMissionCompleter{}
	resolveCalls := 0
	resolve := func(ctx context.Context, connectorID string) (string, error) {
		resolveCalls++
		return "tok", nil
	}

	tool := PushMissionBranch(store, completer, resolve)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"id": "nope"}))
	if err == nil {
		t.Fatal("expected an unknown-mission error")
	}
	if resolveCalls != 0 || completer.pushCalls != 0 {
		t.Fatalf("resolveCalls=%d pushCalls=%d, want neither called", resolveCalls, completer.pushCalls)
	}
}

// TestPushMissionBranchRequiresID proves a missing id is rejected before
// touching the store at all.
func TestPushMissionBranchRequiresID(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	completer := &fakeMissionCompleter{}
	tool := PushMissionBranch(store, completer, nil)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{}))
	if err == nil {
		t.Fatal("expected an id-required error")
	}
}

// fakeMissionFollowUpCreator scripts missionFollowUpCreator for
// followup_mission's table tests.
type fakeMissionFollowUpCreator struct {
	childID string
	err     error
	calls   int
	gotGoal string
}

func (f *fakeMissionFollowUpCreator) CreateFollowUpMission(ctx context.Context, parentID, goal string) (string, error) {
	f.calls++
	f.gotGoal = goal
	if f.err != nil {
		return "", f.err
	}
	return f.childID, nil
}

func terminalMissionRecord() MissionRecord {
	return MissionRecord{ID: "m1", Name: "Fix login bug", Goal: "fix the login bug", Phase: "done"}
}

// TestFollowupMissionRequiresGoal proves a missing goal is rejected
// before touching the store at all.
func TestFollowupMissionRequiresGoal(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	creator := &fakeMissionFollowUpCreator{}
	tool := FollowupMission(store, creator)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"id": "m1"}))
	if err == nil {
		t.Fatal("expected a goal-required error")
	}
	if creator.calls != 0 {
		t.Fatalf("creator called %d times, want 0", creator.calls)
	}
}

// TestFollowupMissionRequiresIDOrQuery proves neither id nor query is
// rejected before touching the store at all.
func TestFollowupMissionRequiresIDOrQuery(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	creator := &fakeMissionFollowUpCreator{}
	tool := FollowupMission(store, creator)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"goal": "do more"}))
	if err == nil {
		t.Fatal("expected an id-or-query-required error")
	}
	if creator.calls != 0 {
		t.Fatalf("creator called %d times, want 0", creator.calls)
	}
}

// TestFollowupMissionAmbiguousQuery proves a query matching more than
// one mission is refused before the creator is ever called.
func TestFollowupMissionAmbiguousQuery(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	store.add(MissionRecord{ID: "m1", Name: "Export invoices", Goal: "export invoices as pdf", Phase: "done"})
	store.add(MissionRecord{ID: "m2", Name: "Export contacts", Goal: "export contacts as csv", Phase: "done"})
	creator := &fakeMissionFollowUpCreator{}
	tool := FollowupMission(store, creator)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"goal": "do more", "query": "export"}))
	if err == nil {
		t.Fatal("expected an ambiguous-match error")
	}
	if creator.calls != 0 {
		t.Fatalf("creator called %d times, want 0", creator.calls)
	}
}

// TestFollowupMissionNonTerminalParent proves a parent still mid-flight
// is refused before the creator is ever called.
func TestFollowupMissionNonTerminalParent(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	rec := terminalMissionRecord()
	rec.Phase = "execute"
	store.add(rec)
	creator := &fakeMissionFollowUpCreator{}
	tool := FollowupMission(store, creator)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"goal": "do more", "id": "m1"}))
	if err == nil {
		t.Fatal("expected a not-finished error")
	}
	if !strings.Contains(err.Error(), "not finished") {
		t.Fatalf("error %q should say the parent is not finished", err.Error())
	}
	if creator.calls != 0 {
		t.Fatalf("creator called %d times, want 0", creator.calls)
	}
}

// TestFollowupMissionCreatorError proves a creator failure surfaces as
// the tool's own error.
func TestFollowupMissionCreatorError(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	store.add(terminalMissionRecord())
	creator := &fakeMissionFollowUpCreator{err: errors.New("create failed")}
	tool := FollowupMission(store, creator)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"goal": "do more", "id": "m1"}))
	if err == nil {
		t.Fatal("expected the creator's error to surface")
	}
	if !strings.Contains(err.Error(), "create failed") {
		t.Fatalf("error %q should wrap the creator's error", err.Error())
	}
}

// TestFollowupMissionHappyPath proves a terminal parent resolved by id
// creates a follow-up and reports the new mission id.
func TestFollowupMissionHappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	store.add(terminalMissionRecord())
	creator := &fakeMissionFollowUpCreator{childID: "m2"}
	tool := FollowupMission(store, creator)
	out, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"goal": "add tests", "id": "m1"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if creator.calls != 1 || creator.gotGoal != "add tests" {
		t.Fatalf("creator calls=%d gotGoal=%q, want 1 call with the follow-up goal", creator.calls, creator.gotGoal)
	}
	if !strings.Contains(out, "m2") || !strings.Contains(out, "m1") {
		t.Fatalf("out = %q, want it to mention both the new and parent mission ids", out)
	}
}

// TestFollowupMissionResolvesByQuery proves a query substring matching
// exactly one terminal mission resolves to that mission as parent.
func TestFollowupMissionResolvesByQuery(t *testing.T) {
	t.Parallel()
	store := newFakeMissionStore()
	store.add(terminalMissionRecord())
	creator := &fakeMissionFollowUpCreator{childID: "m2"}
	tool := FollowupMission(store, creator)
	_, err := tool.Execute(context.Background(), mustMarshal(t, map[string]any{"goal": "add tests", "query": "login"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if creator.calls != 1 {
		t.Fatalf("creator calls = %d, want 1", creator.calls)
	}
}
