package missions

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

// TestIsProviderRejection pins the classifier on the error texts the
// eval produced (issue #718) and on texts that are not the provider's.
func TestIsProviderRejection(t *testing.T) {
	yes := []string{
		"API Error: Request rejected (429) · [1113][Insufficient balance or no resource package. Please recharge.]",
		`404: {"message":"This model is not supported in the v1/chat/completions endpoint. Use the v1/responses endpoint instead.","type":"invalid_request_error"}`,
		"You exceeded your current quota, please check your plan and billing details.",
		"http 503 service unavailable",
		"status 502 from upstream",
	}
	for _, s := range yes {
		if !isProviderRejection(s) {
			t.Errorf("isProviderRejection missed %q", s)
		}
	}
	no := []string{
		"",
		"the executor produced no output for the idle timeout and was killed",
		"tests failed: 3 of 12",
		"please run /login to authenticate",
		"redirect_test.go:41: want 404 got 200",
		"benchmark ran 500 iterations",
	}
	for _, s := range no {
		if isProviderRejection(s) {
			t.Errorf("isProviderRejection false positive on %q", s)
		}
	}
}

// noToolFixture is the claude schema fixture with only the assistant
// tool-call turn and its result dropped: a run that still reports its
// verdict, having called nothing. The init line stays (it carries the
// session id, and its "tools" array is the offered surface, not a
// call), as does the terminal result line.
func noToolFixture(t *testing.T) [][]byte {
	t.Helper()
	var out [][]byte
	for _, l := range loadDelegatedFixture(t, "schema.ndjson") {
		if bytes.Contains(l, []byte(`"type":"tool_use"`)) || bytes.Contains(l, []byte(`"type":"tool_result"`)) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// TestDelegatedRunWorker_ProviderRejectionIsUnavailable (issue #718):
// a result whose error is the provider refusing the run (billing here)
// never becomes a worker verdict. The run is recorded, the entry
// cooled, and the driver gets an ExecutorUnavailableError with until.
func TestDelegatedRunWorker_ProviderRejectionIsUnavailable(t *testing.T) {
	lines := noToolFixture(t)
	for i, l := range lines {
		if bytes.Contains(l, []byte(`"type":"result"`)) {
			l = bytes.Replace(l, []byte(`"is_error":false`), []byte(`"is_error":true`), 1)
			l = bytes.Replace(l, []byte(`"structured_output":{"status":"DONE","note":"Ready for task input."},`), nil, 1)
			l = bytes.Replace(l, []byte(`"result":"{\"status\":\"DONE\",\"note\":\"Ready for task input.\"}"`),
				[]byte(`"result":"API Error: Request rejected (429) · [1113][Insufficient balance or no resource package. Please recharge.]"`), 1)
			lines[i] = l
		}
	}
	sandbox := newFakeSandbox()
	sandbox.seedLines = lines
	events := &fakeEventSink{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}
	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())

	_, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	var unavailable *ExecutorUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want ExecutorUnavailableError", err)
	}
	if unavailable.Until.IsZero() || unavailable.Reason == "" {
		t.Fatalf("unavailable = %+v, want a reason and an until", unavailable)
	}
	if events.count("executor.result") != 1 {
		t.Fatalf("executor.result count = %d, want 1 (the rejected run is still recorded)", events.count("executor.result"))
	}
	if len(r.cooldown) != 1 {
		t.Fatalf("cooldown entries = %d, want the rejecting entry cooled", len(r.cooldown))
	}
}

// TestDelegatedRunWorker_SandboxLaunchErrorIsInfraNotCooldown (issue
// #718): the sandbox failing to start the CLI (image missing) pauses as
// infra with a short until and never cools the provider entry.
func TestDelegatedRunWorker_SandboxLaunchErrorIsInfraNotCooldown(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.launchErr = errors.New("sandboxclient: sandbox: pull image timothy-sandbox-go:latest: Error response from daemon: not found")
	events := &fakeEventSink{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}
	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())

	_, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	var unavailable *ExecutorUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want ExecutorUnavailableError", err)
	}
	if len(r.cooldown) != 0 {
		t.Fatalf("cooldown entries = %d, want none: a missing local image is not a provider signal", len(r.cooldown))
	}
	if events.count("executor.died") != 1 {
		t.Fatalf("executor.died count = %d, want 1", events.count("executor.died"))
	}
}

// TestDelegatedRunWorker_NoToolBlockNamingPlanIsKept (issue #718): a run
// that calls no tool but blocks on a plan defect is a diagnosis, not
// idleness: no relaunch, the verdict reaches the driver as is.
func TestDelegatedRunWorker_NoToolBlockNamingPlanIsKept(t *testing.T) {
	lines := noToolFixture(t)
	for i, l := range lines {
		if bytes.Contains(l, []byte(`"type":"result"`)) {
			l = bytes.Replace(l, []byte(`"structured_output":{"status":"DONE","note":"Ready for task input."}`),
				[]byte(`"structured_output":{"status":"BLOCKED","note":"The check_cmd cannot pass in isolation: it needs permute.go from a later unit."}`), 1)
			lines[i] = l
		}
	}
	sandbox := newFakeSandbox()
	sandbox.seedLines = lines
	events := &fakeEventSink{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}
	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())

	v, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if v.Outcome != "blocked" || !strings.Contains(v.Question, "check_cmd") {
		t.Fatalf("verdict = %+v, want the BLOCKED diagnosis surfaced", v)
	}
	if sandbox.launches != 1 || events.count("executor.relaunched") != 0 {
		t.Fatalf("launches = %d, relaunched = %d, want the diagnosis kept without a relaunch", sandbox.launches, events.count("executor.relaunched"))
	}
}

// TestDelegatedRunWorker_NoToolCallsRelaunchesFresh (issue #718): a run
// that reports a verdict without a single tool call is relaunched once
// with a reset session before its verdict counts; the second run's
// verdict is what RunWorker returns.
func TestDelegatedRunWorker_NoToolCallsRelaunchesFresh(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = noToolFixture(t)
	events := &fakeEventSink{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}
	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())

	v, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if sandbox.launches != 2 {
		t.Fatalf("launches = %d, want 2 (one relaunch after a no-tool run)", sandbox.launches)
	}
	if events.count("executor.relaunched") != 1 {
		t.Fatalf("executor.relaunched count = %d, want 1", events.count("executor.relaunched"))
	}
	if events.count("executor.result") != 2 {
		t.Fatalf("executor.result count = %d, want both runs recorded", events.count("executor.result"))
	}
	if !v.noWork {
		t.Fatal("the surfaced verdict must still carry noWork for the driver's own accounting")
	}
}
