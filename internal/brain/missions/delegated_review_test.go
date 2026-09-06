package missions

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

// Delegated reviewer tests (issue #582): the fakes live in
// delegated_test.go.

// reviewTestMission is a pi-reviewed mission: the worker stays native
// (Harness empty) so RunReview's dispatch is the only delegated path
// under test.
func reviewTestMission(id, workRoot string) Mission {
	return Mission{ID: id, Kind: "coding", Workspace: workRoot, Route: "default", ReviewRoute: "careful", ReviewHarness: "pi"}
}

func TestDelegatedRunReview_HappyPath_Approve(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadPiDelegatedFixture(t, "review-approve.ndjson")
	sandbox.seedChunk = 40
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	led := &fakeLedger{}
	native := &fakeNative{}
	entry := piHarnessEntry("cred-ref-pi")
	var resolvedRoute, resolvedHarness string
	resolve := func(ctx context.Context, name, harness string) (*gwclient.ResolvedRoute, error) {
		resolvedRoute, resolvedHarness = name, harness
		return &gwclient.ResolvedRoute{Route: name, Entries: []gwclient.ResolvedRouteEntry{entry}}, nil
	}

	r := newTestDelegatedRunner(native, resolve, scriptedCred("sk-test-key", nil), sandbox, events, nil, led)
	m := reviewTestMission("m1", t.TempDir())

	verdict, err := r.RunReview(testCtx(t), m, ReviewPacket{Goal: "write a README"})
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !verdict.Approved {
		t.Fatal("Approved = false, want true from the approve fixture")
	}
	if len(verdict.Resolved) != 1 || verdict.Resolved[0] != "F1" {
		t.Fatalf("Resolved = %v, want [F1]", verdict.Resolved)
	}
	if verdict.Provider != entry.ProviderName || verdict.Model != entry.Model {
		t.Fatalf("verdict provider/model = %q/%q, want %q/%q", verdict.Provider, verdict.Model, entry.ProviderName, entry.Model)
	}
	if resolvedRoute != "careful" || resolvedHarness != "pi" {
		t.Fatalf("resolved route/harness = %q/%q, want careful/pi (reviewRoute on the review harness axis)", resolvedRoute, resolvedHarness)
	}
	if native.reviewCount() != 0 {
		t.Fatalf("native RunReview called %d times, want 0", native.reviewCount())
	}
	if events.count("review.delegated_fallback") != 0 {
		t.Fatal("review.delegated_fallback recorded on a successful delegated review")
	}

	spawned := spawnedPayload(t, events)
	if spawned["phase"] != "prove" {
		t.Fatalf("executor.spawned phase = %v, want prove", spawned["phase"])
	}
	runDirValue, _ := spawned["run_dir"].(string)
	if !strings.Contains(runDirValue, filepath.Join("runs", "review-")) {
		t.Fatalf("executor.spawned run_dir = %v, want a runs/review-<id> directory", spawned["run_dir"])
	}
	result, _ := events.last("executor.result")
	var resultPayload map[string]any
	_ = json.Unmarshal(result.Payload, &resultPayload)
	if resultPayload["status"] != "APPROVE" || resultPayload["phase"] != "prove" || resultPayload["parse"] != "schema" {
		t.Fatalf("executor.result payload = %v, want status APPROVE, phase prove, parse schema", resultPayload)
	}
	if !strings.Contains(sandbox.lastLaunchCmd(), "read,grep,find,ls") {
		t.Fatalf("launch command %q does not carry pi's read-only tool list", sandbox.lastLaunchCmd())
	}
	if len(led.entries) != 1 || led.entries[0].Agent != reviewerAgent || led.entries[0].Route != "careful" {
		t.Fatalf("ledger entries = %+v, want one row tagged agent %q on route careful", led.entries, reviewerAgent)
	}
}

func TestDelegatedRunReview_Rework_CarriesFindings(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadPiDelegatedFixture(t, "review-rework.ndjson")
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	entry := piHarnessEntry("cred-ref-pi")
	route := &gwclient.ResolvedRoute{Route: "careful", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("sk-test-key", nil), sandbox, events, nil, &fakeLedger{})
	m := reviewTestMission("m1", t.TempDir())

	verdict, err := r.RunReview(testCtx(t), m, ReviewPacket{Goal: "write a README"})
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if verdict.Approved {
		t.Fatal("Approved = true, want false from the rework fixture")
	}
	if len(verdict.Findings) != 1 {
		t.Fatalf("Findings = %+v, want exactly 1", verdict.Findings)
	}
	f := verdict.Findings[0]
	if f.File != "README.md" || f.Evidence != "+# hello" || f.Severity != SeverityBlocking || f.Title == "" {
		t.Fatalf("finding = %+v, want file README.md, evidence quoted, blocking severity and a title", f)
	}
	result, _ := events.last("executor.result")
	var resultPayload map[string]any
	_ = json.Unmarshal(result.Payload, &resultPayload)
	if resultPayload["status"] != "REWORK" {
		t.Fatalf("executor.result status = %v, want REWORK", resultPayload["status"])
	}
}

func TestDelegatedRunReview_EmptyHarness_NativeWithoutResolve(t *testing.T) {
	native := &fakeNative{reviewVerdict: ReviewVerdict{Approved: true}}
	resolve, calls := countingResolver(scriptedResolver(nil, nil))
	r := newTestDelegatedRunner(native, resolve, scriptedCred("", nil), newFakeSandbox(), &fakeEventSink{}, nil, nil)
	m := reviewTestMission("m1", t.TempDir())
	m.ReviewHarness = ""

	verdict, err := r.RunReview(testCtx(t), m, ReviewPacket{})
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !verdict.Approved || native.reviewCount() != 1 {
		t.Fatalf("native verdict not returned: approved=%v native calls=%d", verdict.Approved, native.reviewCount())
	}
	if *calls != 0 {
		t.Fatalf("route resolved %d times for an empty review_harness, want 0", *calls)
	}
}

// TestDelegatedRunReview_FallsBackToNative covers every fallback rung:
// each scenario must record review.delegated_fallback with its reason
// and return the native reviewer's verdict.
func TestDelegatedRunReview_FallsBackToNative(t *testing.T) {
	piEntry := piHarnessEntry("cred-ref-pi")
	cursorEntry := gwclient.ResolvedRouteEntry{ //nolint:gosec // G101: credential_ref NAME, not a credential value.
		ProviderID: "prov-c", ProviderName: "Cursor", Driver: "cursor-cli", Model: "default",
		CredentialRef: "cred-ref-cursor", Usable: true, Wire: "anthropic",
	}
	unusable := piEntry
	unusable.Usable, unusable.SkipReason = false, "disabled"

	tests := []struct {
		name       string
		harness    string
		route      *gwclient.ResolvedRoute
		resolveErr error
		fixture    string // pi fixture the sandbox replays; "" launches nothing
		truncate   bool   // drop the fixture's terminal line
		wantReason string
		wantDied   bool
	}{
		{name: "unknown harness", harness: "not-a-real-harness", wantReason: "unknown_harness"},
		{name: "resolve failed", harness: "pi", resolveErr: errors.New("boom"), wantReason: "resolve_failed"},
		{name: "no usable entry", harness: "pi", route: &gwclient.ResolvedRoute{Route: "careful", Entries: []gwclient.ResolvedRouteEntry{unusable}}, wantReason: "no_usable_entry"},
		{name: "adapter refuses read-only", harness: "cursor-cli", route: &gwclient.ResolvedRoute{Route: "careful", Entries: []gwclient.ResolvedRouteEntry{cursorEntry}}, wantReason: "refused"},
		{name: "run without a result", harness: "pi", route: &gwclient.ResolvedRoute{Route: "careful", Entries: []gwclient.ResolvedRouteEntry{piEntry}}, fixture: "review-approve.ndjson", truncate: true, wantReason: "no_result", wantDied: true},
		{name: "worker-shaped verdict is unparseable", harness: "pi", route: &gwclient.ResolvedRoute{Route: "careful", Entries: []gwclient.ResolvedRouteEntry{piEntry}}, fixture: "happy.ndjson", wantReason: "unparseable_verdict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sandbox := newFakeSandbox()
			if tt.fixture != "" {
				lines := loadPiDelegatedFixture(t, tt.fixture)
				if tt.truncate {
					lines = lines[:len(lines)-1]
				}
				sandbox.seedLines = lines
			}
			sandbox.seedExitCode = 0
			events := &fakeEventSink{}
			native := &fakeNative{reviewVerdict: ReviewVerdict{Approved: true, Provider: "native"}}
			r := newTestDelegatedRunner(native, scriptedResolver(tt.route, tt.resolveErr), scriptedCred("sk-test-key", nil), sandbox, events, nil, &fakeLedger{})
			m := reviewTestMission("m1", t.TempDir())
			m.ReviewHarness = tt.harness

			verdict, err := r.RunReview(testCtx(t), m, ReviewPacket{Goal: "g"})
			if err != nil {
				t.Fatalf("RunReview: %v", err)
			}
			if !verdict.Approved || verdict.Provider != "native" || native.reviewCount() != 1 {
				t.Fatalf("native verdict not returned: %+v (native calls %d)", verdict, native.reviewCount())
			}
			fb, ok := events.last("review.delegated_fallback")
			if !ok {
				t.Fatal("no review.delegated_fallback event recorded")
			}
			var payload map[string]any
			_ = json.Unmarshal(fb.Payload, &payload)
			if payload["harness"] != tt.harness || payload["reason"] != tt.wantReason {
				t.Fatalf("fallback payload = %v, want harness %q reason %q", payload, tt.harness, tt.wantReason)
			}
			if tt.wantDied {
				died, ok := events.last("executor.died")
				if !ok {
					t.Fatal("no executor.died recorded for the result-less run")
				}
				var diedPayload map[string]any
				_ = json.Unmarshal(died.Payload, &diedPayload)
				if diedPayload["phase"] != "prove" {
					t.Fatalf("executor.died phase = %v, want prove", diedPayload["phase"])
				}
			}
			if tt.wantReason == "refused" && events.count("executor.spawned") != 0 {
				t.Fatal("a refused adapter must never reach executor.spawned")
			}
		})
	}
}

// TestDelegatedRunWorker_EventsCarryPhaseGenerate pins the worker side
// of issue #582's phase field: every worker run's executor events say
// generate, so the timeline can tell them from review runs.
func TestDelegatedRunWorker_EventsCarryPhaseGenerate(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	if _, _, err := r.RunWorker(testCtx(t), testMission("m1", t.TempDir()), WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	for _, kind := range []string{"executor.spawned", "executor.result"} {
		ev, ok := events.last(kind)
		if !ok {
			t.Fatalf("no %s recorded", kind)
		}
		var payload map[string]any
		_ = json.Unmarshal(ev.Payload, &payload)
		if payload["phase"] != "generate" {
			t.Fatalf("%s phase = %v, want generate", kind, payload["phase"])
		}
	}
}

// TestParseDelegatedReviewVerdict pins the strict decision check: a
// payload without an explicit approve/rework never becomes a verdict,
// since a decision-less rework with no findings would read as approval
// downstream.
func TestParseDelegatedReviewVerdict(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantErr  bool
		wantDec  string
		approved bool
	}{
		{"approve", `{"decision":"approve"}`, false, "APPROVE", true},
		{"rework with finding", `{"decision":"rework","findings":[{"title":"gap","file":"a.md","evidence":"x"}]}`, false, "REWORK", false},
		{"empty payload", ``, true, "UNKNOWN", false},
		{"worker status shape", `{"status":"DONE","note":"n"}`, true, "UNKNOWN", false},
		{"unknown decision", `{"decision":"maybe"}`, true, "UNKNOWN", false},
		{"not json", `approve`, true, "UNKNOWN", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict, dec, err := parseDelegatedReviewVerdict(json.RawMessage(tt.raw))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if dec != tt.wantDec {
				t.Errorf("decision = %q, want %q", dec, tt.wantDec)
			}
			if !tt.wantErr && verdict.Approved != tt.approved {
				t.Errorf("Approved = %v, want %v", verdict.Approved, tt.approved)
			}
		})
	}
}
