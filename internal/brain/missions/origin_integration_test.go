//go:build integration

package missions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// TestStoreOriginRoundTrip covers issue #817: origin_kind and
// unattended persist through Create and read back through Get, List
// and the origin_kind list filter.
func TestStoreOriginRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	cases := []struct {
		origin     string
		unattended bool
	}{
		{OriginAPI, false},
		{OriginAutomation, true},
		{OriginWorkflow, true},
		{OriginChat, false},
		{OriginFollowup, false},
		{OriginAPI, true},
	}
	ids := map[string]string{}
	for _, tc := range cases {
		id, err := s.Create(ctx, Mission{Goal: marker + "origin " + tc.origin, Kind: KindGeneral, Route: "default", OriginKind: tc.origin, Unattended: tc.unattended})
		if err != nil {
			t.Fatalf("Create(%s): %v", tc.origin, err)
		}
		m, err := s.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if m.OriginKind != tc.origin || m.Unattended != tc.unattended {
			t.Fatalf("round trip origin=%q unattended=%v, want %q %v", m.OriginKind, m.Unattended, tc.origin, tc.unattended)
		}
		ids[id] = tc.origin
	}

	// A caller that skipped ResolveDefaults records api.
	id, err := s.Create(ctx, Mission{Goal: marker + "origin empty", Kind: KindGeneral, Route: "default"})
	if err != nil {
		t.Fatalf("Create(empty origin): %v", err)
	}
	if m, _ := s.Get(ctx, id); m.OriginKind != OriginAPI || m.Unattended {
		t.Fatalf("empty origin read back origin=%q unattended=%v, want api false", m.OriginKind, m.Unattended)
	}

	// The CHECK constraint rejects an unknown origin.
	if _, err := s.Create(ctx, Mission{Goal: marker + "origin bogus", Kind: KindGeneral, Route: "default", OriginKind: "cron"}); err == nil {
		t.Fatal("Create with origin_kind cron succeeded, want CHECK violation")
	}

	rows, err := s.List(ctx, ListFilter{OriginKind: OriginWorkflow})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, m := range rows {
		if m.OriginKind != OriginWorkflow {
			t.Fatalf("List(origin_kind=workflow) returned origin %q", m.OriginKind)
		}
		if ids[m.ID] == OriginWorkflow {
			found = true
			if !m.Unattended {
				t.Fatal("List row unattended = false, want true")
			}
		}
	}
	if !found {
		t.Fatal("List(origin_kind=workflow) missed the workflow fixture")
	}
}

// TestSchedulerFireRecordsAutomationOrigin covers issue #817: a
// scheduler-fired mission row has origin_kind=automation, unattended
// true and the 1800s permission timeout default.
func TestSchedulerFireRecordsAutomationOrigin(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	id := createTestSchedule(t, store, marker+"automation-origin", "* * * * *")
	db, _ := store.db.Get()
	if _, err := db.Exec(ctx, "UPDATE schedules SET created_at = $2 WHERE id = $1", id, time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatalf("backdate schedule: %v", err)
	}
	if err := testScheduler(store, nil).tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var missionID string
	if err := db.QueryRow(ctx, `SELECT id FROM missions WHERE schedule_id = $1`, id).Scan(&missionID); err != nil {
		t.Fatalf("query fired mission: %v", err)
	}
	m, err := store.Get(ctx, missionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.OriginKind != OriginAutomation || !m.Unattended {
		t.Fatalf("fired mission origin_kind=%q unattended=%v, want automation true", m.OriginKind, m.Unattended)
	}
	if m.PermissionTimeoutSeconds == nil || *m.PermissionTimeoutSeconds != 1800 {
		t.Fatalf("fired mission permission_timeout_seconds = %v, want 1800", m.PermissionTimeoutSeconds)
	}
}

// originGateway scripts a worker turn: one call to the permission-gated
// tool, then the mission_status sentinel.
type originGateway struct {
	mu       sync.Mutex
	scripts  [][]stream.StreamEvent
	requests []gwclient.StreamRequest
}

func (g *originGateway) RouteForRole(_ context.Context, role string) (string, bool, error) {
	return role, true, nil
}

func (g *originGateway) Stream(_ context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, req)
	if len(g.scripts) == 0 {
		return nil, errors.New("origin gateway exhausted")
	}
	script := g.scripts[0]
	g.scripts = g.scripts[1:]
	ch := make(chan stream.StreamEvent, len(script))
	for _, ev := range script {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func originToolStep(name, args string) []stream.StreamEvent {
	return []stream.StreamEvent{
		{Type: stream.EventToolStart, ToolCall: &stream.ToolCallEvent{ID: "call_" + name, Name: name}},
		{Type: stream.EventToolEnd, ToolCall: &stream.ToolCallEvent{ID: "call_" + name, Name: name, Input: json.RawMessage(args)}},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "fake", Model: "fake-1"}},
	}
}

// gatedExec is the builtin surface: one tool, "gated", that must never
// execute on an unattended turn.
type gatedExec struct{ calls int }

func (e *gatedExec) Execute(context.Context, string, json.RawMessage) (string, error) {
	e.calls++
	return "ran", nil
}
func (e *gatedExec) Trusted(string) bool { return true }

// askGatedPerms asks for "gated" and allows everything else (the
// mission_status sentinel).
type askGatedPerms struct{}

func (askGatedPerms) Resolve(_ context.Context, _, tool string, _ json.RawMessage) (tools.Resolution, error) {
	if tool == "gated" {
		return tools.Resolution{Decision: tools.DecisionAsk, Subject: tool, Rationale: "no standing grant"}, nil
	}
	return tools.Resolution{Decision: tools.DecisionAllow, Subject: tool}, nil
}
func (askGatedPerms) Grant(context.Context, string, string, string, time.Duration) error { return nil }

type originOutputs struct{}

func (originOutputs) Put(context.Context, string, string, string) (string, error) { return "out", nil }

type originAudit struct{}

func (originAudit) Record(context.Context, tools.AuditEntry) error { return nil }

type originEvents struct{}

func (originEvents) Append(context.Context, string, string, any) (int64, error) { return 1, nil }

// TestWorkflowMissionPermissionAskDeniedInTurn covers issue #817's
// workflow gap end to end short of a live model: a workflow-origin
// mission resolved and stored through the real create path reads back
// unattended, and a real loop.Agent worker turn over it denies a
// permission Ask immediately with feedback (no park, no permission
// request event, the tool never runs) instead of waiting forever.
// The workflows engine itself is covered by its own integration test;
// importing it here would be an import cycle.
func TestWorkflowMissionPermissionAskDeniedInTurn(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()

	m, err := ResolveDefaults(ctx, CreateRequest{Goal: marker + "workflow ask", Kind: KindGeneral, Light: true, Route: "default", OriginKind: OriginWorkflow}, ResolveDeps{})
	if err != nil {
		t.Fatalf("ResolveDefaults: %v", err)
	}
	if err := ValidateCreate(ctx, m, ValidateDeps{}); err != nil {
		t.Fatalf("ValidateCreate: %v", err)
	}
	id, err := store.Create(ctx, m)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.OriginKind != OriginWorkflow || !stored.Unattended {
		t.Fatalf("stored origin_kind=%q unattended=%v, want workflow true", stored.OriginKind, stored.Unattended)
	}
	stored.SessionID = "s-" + id

	gw := &originGateway{scripts: [][]stream.StreamEvent{
		originToolStep("gated", `{}`),
		originToolStep(missionStatusToolName, `{"outcome":"done","evidence":"ok","final_output":"done"}`),
	}}
	exec := &gatedExec{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	agent := loop.NewAgent(gw, exec, askGatedPerms{}, originOutputs{}, originAudit{}, originEvents{}, loop.NewPermBroker(), nil, log)
	agent.SetBaseTools(exec, []provider.ToolDef{{Name: "gated", Description: "gated", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	parker := &fakeParker{}
	r := &nativeRunner{agent: agent, parker: parker, log: log}

	done := make(chan struct{})
	var verdict WorkerVerdict
	var runErr error
	go func() {
		defer close(done)
		verdict, _, runErr = r.RunWorker(ctx, stored, WorkPacket{Goal: stored.Goal, Light: true})
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("worker turn did not finish: the permission Ask parked instead of denying")
	}
	if runErr != nil {
		t.Fatalf("RunWorker: %v", runErr)
	}
	if verdict.Outcome != "done" {
		t.Fatalf("verdict outcome = %q, want done after the denial", verdict.Outcome)
	}
	if exec.calls != 0 {
		t.Fatalf("gated tool executed %d times, want 0 on an unattended turn", exec.calls)
	}
	if len(parker.parked) != 0 {
		t.Fatalf("parked = %v, want no permission park", parker.parked)
	}
	if len(parker.denied) != 1 || !strings.HasPrefix(parker.denied[0], id+":gated:") {
		t.Fatalf("denied = %v, want one denial of gated", parker.denied)
	}
	var feedback string
	for _, msg := range gw.requests[1].Messages {
		if msg.ToolResult != nil {
			feedback = msg.ToolResult.Content
		}
	}
	if !strings.Contains(feedback, "unattended mission") {
		t.Fatalf("tool feedback = %q, want the unattended denial", feedback)
	}
}
