package channels

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

const (
	testPermID    = "0123456789abcdef0123456789abcdef"
	testMissionID = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

func TestParseCallback(t *testing.T) {
	tests := []struct {
		name string
		data string
		ok   bool
		want callback
	}{
		{"permission once", permCallback(testPermID, "once"), true, callback{Kind: cbPermission, ID: testPermID, Action: "once"}},
		{"permission session", permCallback(testPermID, "session"), true, callback{Kind: cbPermission, ID: testPermID, Action: "session"}},
		{"permission deny", permCallback(testPermID, "deny"), true, callback{Kind: cbPermission, ID: testPermID, Action: "deny"}},
		{"permission timeout is not a button", permCallback(testPermID, "timeout"), false, callback{}},
		{"permission extra part", permCallback(testPermID, "once") + ":x", false, callback{}},
		{"plan approve", missionCallback(testMissionID, actPlanApprove), true, callback{Kind: cbMission, ID: testMissionID, Action: actPlanApprove}},
		{"plan replan", missionCallback(testMissionID, actPlanReplan), true, callback{Kind: cbMission, ID: testMissionID, Action: actPlanReplan}},
		{"resume", missionCallback(testMissionID, actResume), true, callback{Kind: cbMission, ID: testMissionID, Action: actResume}},
		{"cancel", missionCallback(testMissionID, actCancel), true, callback{Kind: cbMission, ID: testMissionID, Action: actCancel}},
		{"ask index", askCallback(testMissionID, 3), true, callback{Kind: cbMission, ID: testMissionID, Action: actAsk, Index: 3}},
		{"ask index past the button cap", askCallback(testMissionID, maxAskButtons), false, callback{}},
		{"ask negative index", missionCallback(testMissionID, "ask:-1"), false, callback{}},
		{"ask padded index", missionCallback(testMissionID, "ask:01"), false, callback{}},
		{"ask without index", missionCallback(testMissionID, "ask"), false, callback{}},
		{"unknown action", missionCallback(testMissionID, "delete"), false, callback{}},
		{"unknown kind", "x:" + testMissionID + ":resume", false, callback{}},
		{"id with bad characters", "p:../etc:once", false, callback{}},
		{"empty", "", false, callback{}},
		{"oversize", "m:" + strings.Repeat("a", 60) + ":resume", false, callback{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCallback(tt.data)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("parseCallback(%q) = %+v %v, want %+v %v", tt.data, got, ok, tt.want, tt.ok)
			}
		})
	}
	for _, d := range []string{permCallback(testPermID, "session"), missionCallback(testMissionID, actPlanApprove), askCallback(testMissionID, 7)} {
		if len(d) > callbackDataLimit {
			t.Fatalf("%q is %d bytes, over Telegram's 64", d, len(d))
		}
	}
}

func TestAuthorizeCallback(t *testing.T) {
	approved := Pairing{Status: StatusApproved}
	conv := Conversation{ID: "conv-a", SessionID: "sess-a"}
	tests := []struct {
		name         string
		p            Pairing
		found        bool
		ownerSession string
		ownerConv    string
		want         string
	}{
		{"unpaired presser", Pairing{Status: StatusPending}, true, "sess-a", "", msgNotPaired},
		{"revoked presser", Pairing{Status: StatusRevoked}, true, "sess-a", "", msgNotPaired},
		{"paired presser in a chat without a conversation", approved, false, "sess-a", "", msgNotYours},
		{"permission of another session", approved, true, "sess-b", "", msgNotYours},
		{"mission of another conversation", approved, true, "", "conv-b", msgNotYours},
		{"no owner at all", approved, true, "", "", msgNotYours},
		{"chat prompt of this session", approved, true, "sess-a", "", ""},
		{"mission prompt of this conversation", approved, true, "mission-session", "conv-a", ""},
		{"mission action of this conversation", approved, true, "", "conv-a", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authorizeCallback(tt.p, conv, tt.found, tt.ownerSession, tt.ownerConv); got != tt.want {
				t.Fatalf("authorizeCallback = %q, want %q", got, tt.want)
			}
		})
	}
}

func buttonData(kb [][]tgButton) []string {
	var out []string
	for _, row := range kb {
		for _, b := range row {
			out = append(out, b.CallbackData)
		}
	}
	return out
}

func TestRenderPark(t *testing.T) {
	asked := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)
	base := missions.Mission{ID: testMissionID, Name: "Brief", Phase: missions.PhaseBuild, Status: missions.StatusWorking, UpdatedAt: updated}

	perm := base
	perm.PendingPermission, perm.PendingPermissionTool, perm.PendingPermissionRationale = testPermID, "shell", strings.Repeat("r", 400)
	p, ok := renderPark(perm)
	if !ok || p.Key != "perm:"+testPermID || p.AskKind != "" ||
		p.Text != "Mission Brief wants to run shell: "+strings.Repeat("r", rationaleCap) ||
		strings.Join(buttonData(p.Keyboard), ",") != strings.Join(buttonData(permKeyboard(testPermID)), ",") {
		t.Fatalf("permission park = %+v %v", p, ok)
	}

	options := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	ask := base
	ask.Status = missions.StatusWaitingForInput
	ask.PendingInput = &missions.PendingInput{Question: "Which one?", Kind: "mcq", Options: options, ProposedDefault: "b", AskedAt: asked}
	p, ok = renderPark(ask)
	if !ok || p.Key != "ask:"+asked.Format(time.RFC3339Nano) || p.AskKind != "" || p.Text != "Mission Brief asks: Which one?\nProposed: b" {
		t.Fatalf("ask with options park = %+v %v", p, ok)
	}
	if data := buttonData(p.Keyboard); len(data) != maxAskButtons || data[1] != askCallback(testMissionID, 1) || p.Keyboard[1][0].Text != "b" {
		t.Fatalf("ask buttons = %v, want %d capped option buttons", data, maxAskButtons)
	}

	open := ask
	open.PendingInput = &missions.PendingInput{Question: "What name?", Kind: "open", AskedAt: asked}
	p, ok = renderPark(open)
	if !ok || len(p.Keyboard) != 0 || p.AskKind != AskUser || p.Text != "Mission Brief asks: What name?\n\n"+msgReplyHint {
		t.Fatalf("ask without options park = %+v %v", p, ok)
	}

	plan := base
	plan.Phase, plan.Status, plan.PauseReason = missions.PhasePlan, missions.StatusPaused, missions.PauseApproval
	plan.Plan = missions.Plan{Units: []missions.PlanUnit{{Title: "Draft"}, {Title: "Review"}}}
	p, ok = renderPark(plan)
	if !ok || p.Key != "plan:"+updated.Format(time.RFC3339Nano) || p.AskKind != AskPlan ||
		p.Text != "Mission Brief has a plan ready\n\n1. Draft\n2. Review\n\n"+msgReplanHint ||
		strings.Join(buttonData(p.Keyboard), ",") != missionCallback(testMissionID, actPlanApprove)+","+missionCallback(testMissionID, actPlanReplan) {
		t.Fatalf("plan park = %+v %v", p, ok)
	}
	plan.Plan = missions.Plan{Units: []missions.PlanUnit{{Title: strings.Repeat("u", 2000)}}}
	if p, _ := renderPark(plan); !strings.Contains(p.Text, "1. "+strings.Repeat("u", digestCap-3)+"\n\n") {
		t.Fatal("plan summary not capped at digestCap")
	}

	paused := base
	paused.Status, paused.PauseReason, paused.PauseMessage = missions.StatusPaused, missions.PauseInfra, "sandbox down"
	p, ok = renderPark(paused)
	if !ok || p.Key != "paused:"+updated.Format(time.RFC3339Nano) || p.Text != "Mission Brief paused: sandbox down" ||
		strings.Join(buttonData(p.Keyboard), ",") != missionCallback(testMissionID, actResume)+","+missionCallback(testMissionID, actCancel) {
		t.Fatalf("paused park = %+v %v", p, ok)
	}
	paused.PauseMessage = ""
	if p, _ := renderPark(paused); p.Text != "Mission Brief paused: infra" {
		t.Fatalf("paused without message = %q", p.Text)
	}

	if _, ok := renderPark(base); ok {
		t.Fatal("a working mission has a park")
	}
	done := ask
	done.Phase = missions.PhaseDone
	if _, ok := renderPark(done); ok {
		t.Fatal("a terminal mission has a park")
	}
}

func TestPushParkDedupsByKey(t *testing.T) {
	store := NewStore(pgpool.New(context.Background(), "", nil))
	svc := New(store, nil, MissionDeps{}, fakeResolve, nil, discardLog())
	m := missions.Mission{ID: testMissionID, Name: "Brief", Phase: missions.PhaseBuild, ChannelConversationID: "conv",
		PendingPermission: testPermID, PendingPermissionTool: "shell"}
	p, _ := renderPark(m)
	svc.parked[m.ID] = p.Key
	svc.pushPark(context.Background(), m)
	if svc.skipLogged[m.ID] {
		t.Fatal("an already pushed key reached the store")
	}
	m.PendingPermission = "fedcba9876543210fedcba9876543210"
	svc.pushPark(context.Background(), m)
	if !svc.skipLogged[m.ID] {
		t.Fatal("a new key was not attempted")
	}
	m.PendingPermission = ""
	svc.pushPark(context.Background(), m)
	if _, ok := svc.parked[m.ID]; ok || svc.skipLogged[m.ID] {
		t.Fatal("an unparked mission kept its dedup state")
	}
}

func TestOutcomeText(t *testing.T) {
	m := missions.Mission{ID: testMissionID, Name: "Brief"}
	long := strings.Repeat("d", 2000)
	tests := []struct {
		name     string
		terminal missions.Phase
		reason   string
		digest   string
		base     string
		want     string
	}{
		{"done with link", missions.PhaseDone, "", "digest", "https://t.example/", "Mission Brief is done\n\ndigest\n\nhttps://t.example/missions/" + testMissionID},
		{"done without link", missions.PhaseDone, "", "digest", "", "Mission Brief is done\n\ndigest"},
		{"failed with reason", missions.PhaseFailed, "max_iterations", "digest", "https://t.example", "Mission Brief failed: max_iterations\n\ndigest\n\nhttps://t.example/missions/" + testMissionID},
		{"failed without reason", missions.PhaseFailed, "", "", "", "Mission Brief failed"},
		{"cancelled", missions.PhaseFailed, "cancelled", "digest", "", "Mission Brief is cancelled\n\ndigest"},
		{"digest capped", missions.PhaseDone, "", long, "", "Mission Brief is done\n\n" + strings.Repeat("d", digestCap)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outcomeText(m, tt.terminal, tt.reason, tt.digest, tt.base); got != tt.want {
				t.Fatalf("outcomeText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConvStateAsks(t *testing.T) {
	var st convState
	for i := int64(1); i <= maxAsks+5; i++ {
		st.remember(i*10, pendingAsk{MissionID: "m", Kind: AskUser})
	}
	if len(st.Asks) != maxAsks {
		t.Fatalf("remembered %d asks, want the cap %d", len(st.Asks), maxAsks)
	}
	if _, ok := st.take(50); ok {
		t.Fatal("an ask past the cap (oldest) survived")
	}
	a, ok := st.take(60)
	if !ok || a.MissionID != "m" || a.Kind != AskUser {
		t.Fatalf("take = %+v %v", a, ok)
	}
	if _, ok := st.take(60); ok {
		t.Fatal("an ask was taken twice")
	}
	raw, _ := json.Marshal(st)
	var back convState
	if err := json.Unmarshal(raw, &back); err != nil || len(back.Asks) != maxAsks-1 {
		t.Fatalf("round trip = %d asks %v", len(back.Asks), err)
	}
}

func TestDrainSendsAndClosesPermissionButtons(t *testing.T) {
	f := newFakeBot(t)
	r := testRunner(f)
	r.svc.missions.ResolvePermission = func(context.Context, string, string) bool { return true }
	events := make(chan stream.StreamEvent, 4)
	events <- stream.StreamEvent{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{ID: testPermID, Tool: "shell", Rationale: "list files"}}
	events <- stream.StreamEvent{Type: stream.EventPermissionResolved, Resolved: &stream.PermissionResolvedEvent{ID: testPermID, Decision: "session"}}
	events <- stream.StreamEvent{Type: stream.EventChunk, Text: "ok"}
	events <- stream.StreamEvent{Type: stream.EventDone}
	r.drain(context.Background(), turnJob{chatID: 7, threadID: 3}, 55, events, nil)
	sends := f.callsOf("sendMessage")
	if len(sends) != 1 || sends[0].Body["text"] != "Timothy wants to run shell: list files" || sends[0].Body["message_thread_id"].(float64) != 3 {
		t.Fatalf("buttons message = %+v", sends)
	}
	kb := sends[0].Body["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)
	if len(kb) != 3 || kb[0].(map[string]any)["callback_data"] != permCallback(testPermID, "once") {
		t.Fatalf("keyboard = %v", kb)
	}
	var closed bool
	for _, c := range f.callsOf("editMessageText") {
		if c.Body["message_id"].(float64) == float64(sends[0].ResultID) {
			closed = c.Body["text"] == "Allowed for this session" && c.Body["reply_markup"] != nil
		}
	}
	if !closed {
		t.Fatalf("buttons message not closed: %+v", f.callsOf("editMessageText"))
	}
}

func TestDrainSkipsButtonsWithoutResolver(t *testing.T) {
	f := newFakeBot(t)
	r := testRunner(f)
	events := make(chan stream.StreamEvent, 2)
	events <- stream.StreamEvent{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{ID: testPermID, Tool: "shell"}}
	events <- stream.StreamEvent{Type: stream.EventDone}
	r.drain(context.Background(), turnJob{chatID: 7}, 55, events, nil)
	if n := len(f.callsOf("sendMessage")); n != 0 {
		t.Fatalf("sent %d messages without a permission resolver", n)
	}
}

func TestDecisionText(t *testing.T) {
	for decision, want := range map[string]string{"once": "Allowed once", "session": "Allowed for this session", "deny": "Denied", "timeout": "Resolved", "": "Resolved"} {
		if got := decisionText(decision); got != want {
			t.Fatalf("decisionText(%q) = %q, want %q", decision, got, want)
		}
	}
}
