package missions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newFakeVerifier() *verifier {
	return &verifier{store: newFakeStore(), sandboxExec: fakeSandboxExec, log: slog.Default()}
}

// TestVerifyAllNoVerifyCmdPasses covers the simplest case: a unit with
// no declared artifacts and no verify_cmd passes outright.
func TestVerifyAllNoVerifyCmdPasses(t *testing.T) {
	v := newFakeVerifier()
	m := Mission{ID: "m1", Plan: Plan{Units: []PlanUnit{{Title: "only unit"}}}}

	got, err := v.verifyAll(context.Background(), m, nil, true)
	if err != nil {
		t.Fatalf("verifyAll: %v", err)
	}
	if len(got) != 1 || !got[0].Passed || got[0].Unit != 0 {
		t.Fatalf("verifyAll = %+v, want unit 0 passed", got)
	}
}

// TestVerifyAllMissingArtifactFailsCurrentSkipsLater confirms the
// current unit's missing artifact is a recorded failure while a later,
// not yet started unit with a missing artifact is skipped: nothing to
// record about work nobody attempted.
func TestVerifyAllMissingArtifactFailsCurrentSkipsLater(t *testing.T) {
	v := newFakeVerifier()
	workRoot := t.TempDir()
	m := Mission{
		ID: "m1", Workspace: workRoot,
		Plan: Plan{Units: []PlanUnit{
			{Title: "writes a file", Artifacts: []string{"out.txt"}},
			{Title: "later", Artifacts: []string{"later.txt"}},
		}},
	}

	got, err := v.verifyAll(context.Background(), m, nil, true)
	if err != nil {
		t.Fatalf("verifyAll: %v", err)
	}
	if len(got) != 1 || got[0].Unit != 0 || got[0].Passed || got[0].Check != "artifacts" {
		t.Fatalf("verifyAll = %+v, want only unit 0, failed on artifacts", got)
	}
	if !strings.Contains(got[0].Excerpt, "out.txt: not found") {
		t.Fatalf("excerpt = %q, want the missing path named", got[0].Excerpt)
	}
}

// TestVerifyAllVerifyCmdGatesPass confirms verify_cmd's exit code, not
// any model claim, is what passes a unit, and that a later unit whose
// artifacts already exist is verified in the same pass.
func TestVerifyAllVerifyCmdGatesPass(t *testing.T) {
	v := newFakeVerifier()
	workRoot := t.TempDir()
	for _, f := range []string{"out.txt", "later.txt"} {
		if err := os.WriteFile(filepath.Join(workRoot, f), []byte("data"), 0o600); err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
	}
	m := Mission{
		ID: "m1", Workspace: workRoot,
		Plan: Plan{Units: []PlanUnit{
			{Title: "writes and checks", Artifacts: []string{"out.txt"}, VerifyCmd: "echo boom; exit 1"},
			{Title: "later", Artifacts: []string{"later.txt"}, VerifyCmd: "exit 0"},
		}},
	}

	got, err := v.verifyAll(context.Background(), m, nil, true)
	if err != nil {
		t.Fatalf("verifyAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("verifyAll = %+v, want both units checked", got)
	}
	if got[0].Passed || got[0].Check != "verify_cmd" || !strings.Contains(got[0].Excerpt, "boom") {
		t.Fatalf("unit 0 = %+v, want a verify_cmd failure carrying the output", got[0])
	}
	if !got[1].Passed {
		t.Fatalf("unit 1 = %+v, want passed (its artifact exists and verify_cmd exits 0)", got[1])
	}
}

// TestVerifyAllRegressionSubsetRerunsPassedUnits confirms an already
// harness-passed unit is re-checked (artifacts and verify_cmd) and
// reported failed when its artifact vanished, without a citations
// check that would false-fail it for want of this turn's seenURLs.
func TestVerifyAllRegressionSubsetRerunsPassedUnits(t *testing.T) {
	v := newFakeVerifier()
	workRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workRoot, "b.md"), []byte("see https://example.com/x"), 0o600); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	m := Mission{
		ID: "m1", Kind: "general", Workspace: workRoot,
		Plan: Plan{Units: []PlanUnit{
			{Title: "gone", Artifacts: []string{"a.md"}, HarnessPassed: true, Passes: true},
			{Title: "intact", Artifacts: []string{"b.md"}, VerifyCmd: "exit 0", HarnessPassed: true, Passes: true},
		}},
	}

	got, err := v.verifyAll(context.Background(), m, nil, true)
	if err != nil {
		t.Fatalf("verifyAll: %v", err)
	}
	if len(got) != 2 || got[0].Passed || got[0].Check != "artifacts" || !got[1].Passed {
		t.Fatalf("verifyAll = %+v, want unit 0 regressed on artifacts and unit 1 still passing", got)
	}
	events, _ := v.store.Events(context.Background(), m.ID)
	if len(events) != 2 || !strings.Contains(string(events[0].Payload), `"regression":true`) {
		t.Fatalf("events = %+v, want two mission.unit_verified events flagged as regression runs", events)
	}
}

// TestVerifyAllCitationsOnlyForUnverifiedUnits confirms the citation
// check (general missions) runs for the current unit against this
// turn's seenURLs and fails an invented citation.
func TestVerifyAllCitationsOnlyForUnverifiedUnits(t *testing.T) {
	v := newFakeVerifier()
	workRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workRoot, "r.md"), []byte("[x](https://docs.acme.dev/invented)"), 0o600); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	m := Mission{
		ID: "m1", Kind: "general", Workspace: workRoot,
		Plan: Plan{Units: []PlanUnit{{Title: "report", Artifacts: []string{"r.md"}}}},
	}

	got, err := v.verifyAll(context.Background(), m, []string{"https://docs.acme.dev/other"}, true)
	if err != nil {
		t.Fatalf("verifyAll: %v", err)
	}
	if len(got) != 1 || got[0].Passed || got[0].Check != "citations" {
		t.Fatalf("verifyAll = %+v, want a citations failure", got)
	}
	// The review-approval pass has no seenURLs and must not re-run it.
	got, err = v.verifyAll(context.Background(), m, nil, false)
	if err != nil {
		t.Fatalf("verifyAll: %v", err)
	}
	if len(got) != 1 || !got[0].Passed {
		t.Fatalf("verifyAll without citations = %+v, want passed", got)
	}
}

// TestRunVerifyTimedHungCommandCountsAsFailed confirms a verify_cmd that
// outlives its timeout is reported as a failed result naming the
// timeout, never as an infrastructure error (the driver would otherwise
// route it to worker_failed and retry the same hang).
func TestRunVerifyTimedHungCommandCountsAsFailed(t *testing.T) {
	hang := func(ctx context.Context, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		_, _ = io.WriteString(out, "still running")
		<-ctx.Done()
		return 0, ctx.Err()
	}
	res, err := runVerifyTimed(context.Background(), hang, t.TempDir(), "sleep 60", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("runVerifyTimed: %v, want a failed result instead of an error", err)
	}
	if res.Passed || !res.TimedOut || !strings.Contains(res.Excerpt, "timed out after") || !strings.Contains(res.Excerpt, "still running") {
		t.Fatalf("result = %+v, want failed, TimedOut, excerpt with output and the timeout", res)
	}
}

// TestRunVerifyTimedCallerCancelIsAnError confirms the caller's own
// cancellation (a mission cancel mid-verify) stays an error: it is not
// evidence about the unit.
func TestRunVerifyTimedCallerCancelIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hang := func(ctx context.Context, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	if _, err := runVerifyTimed(ctx, hang, t.TempDir(), "true", time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("runVerifyTimed = %v, want context.Canceled", err)
	}
}
