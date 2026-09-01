package missions

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func newFakeVerifier() *verifier {
	return &verifier{store: newFakeStore(), sandboxExec: fakeSandboxExec, log: slog.Default()}
}

// TestVerifierVerifyCurrentUnitNoVerifyCmdPasses covers the simplest
// case directly (previously only reachable through the whole Driver):
// a unit with no declared artifacts and no verify_cmd passes outright
// and marks itself passed in the store.
func TestVerifierVerifyCurrentUnitNoVerifyCmdPasses(t *testing.T) {
	v := newFakeVerifier()
	store := v.store.(*fakeStore)
	m := Mission{ID: "m1", Plan: Plan{Units: []PlanUnit{{Title: "only unit"}}}}
	store.put(m.ID, m)

	if err := v.verifyCurrentUnit(context.Background(), m, nil); err != nil {
		t.Fatalf("verifyCurrentUnit: %v", err)
	}
	if !store.missions[m.ID].Plan.Units[0].Passes {
		t.Fatal("verifyCurrentUnit did not mark the unit passed in the store")
	}
}

// TestVerifierVerifyCurrentUnitMissingArtifactFails confirms a unit
// declaring an artifact that was never written fails as a
// *verifyFailure (never a plain error) and never gets marked passed —
// this is the harness-evidence gate the whole verifier type exists to
// make type-visible.
func TestVerifierVerifyCurrentUnitMissingArtifactFails(t *testing.T) {
	v := newFakeVerifier()
	store := v.store.(*fakeStore)
	workRoot := t.TempDir()
	m := Mission{
		ID: "m1", Workspace: workRoot,
		Plan: Plan{Units: []PlanUnit{{Title: "writes a file", Artifacts: []string{"out.txt"}}}},
	}
	store.put(m.ID, m)

	err := v.verifyCurrentUnit(context.Background(), m, nil)
	var vf *verifyFailure
	if !errors.As(err, &vf) {
		t.Fatalf("verifyCurrentUnit error = %v, want a *verifyFailure", err)
	}
	if vf.unit != 0 {
		t.Fatalf("verifyFailure.unit = %d, want 0", vf.unit)
	}
	if store.missions[m.ID].Plan.Units[0].Passes {
		t.Fatal("verifyCurrentUnit must not mark a unit passed when its artifact is missing")
	}
}

// TestVerifierVerifyCurrentUnitVerifyCmdGatesPass confirms verify_cmd's
// exit code — not any model claim — is what flips Passes: a failing
// command fails the unit, a passing one (after the artifact exists)
// marks it passed.
func TestVerifierVerifyCurrentUnitVerifyCmdGatesPass(t *testing.T) {
	v := newFakeVerifier()
	store := v.store.(*fakeStore)
	workRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workRoot, "out.txt"), []byte("data"), 0o600); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	m := Mission{
		ID: "m1", Workspace: workRoot,
		Plan: Plan{Units: []PlanUnit{{Title: "writes and checks", Artifacts: []string{"out.txt"}, VerifyCmd: "exit 1"}}},
	}
	store.put(m.ID, m)

	err := v.verifyCurrentUnit(context.Background(), m, nil)
	var vf *verifyFailure
	if !errors.As(err, &vf) {
		t.Fatalf("verifyCurrentUnit error = %v, want a *verifyFailure for a failing verify_cmd", err)
	}
	if store.missions[m.ID].Plan.Units[0].Passes {
		t.Fatal("verifyCurrentUnit must not mark a unit passed when verify_cmd fails")
	}

	// Same unit, verify_cmd now passes: the unit flips to passed.
	m2 := store.missions[m.ID]
	m2.Plan.Units[0].VerifyCmd = "exit 0"
	store.put(m.ID, m2)
	if err := v.verifyCurrentUnit(context.Background(), m2, nil); err != nil {
		t.Fatalf("verifyCurrentUnit: %v", err)
	}
	if !store.missions[m.ID].Plan.Units[0].Passes {
		t.Fatal("verifyCurrentUnit did not mark the unit passed once verify_cmd exited 0")
	}
}

// TestVerifierCheckRegressionsDetectsBrokenArtifact confirms a
// previously-passed unit whose declared artifact later disappears is
// caught and flipped back to unverified via regressed — passes must
// never move only forward, silently, once a later unit breaks earlier
// evidence.
func TestVerifierCheckRegressionsDetectsBrokenArtifact(t *testing.T) {
	v := newFakeVerifier()
	store := v.store.(*fakeStore)
	workRoot := t.TempDir()
	m := Mission{
		ID: "m1", Workspace: workRoot,
		Plan: Plan{Units: []PlanUnit{{Title: "earlier unit", Artifacts: []string{"out.txt"}, Passes: true}}},
	}
	store.put(m.ID, m)
	// out.txt was never (re)written in workRoot: CheckArtifacts must
	// report it missing, simulating a later unit having deleted it.

	in, regressed := v.checkRegressions(context.Background(), m)
	if !regressed {
		t.Fatal("checkRegressions = false, want true for a unit whose artifact vanished")
	}
	if in.Input != InputWorkerRetry {
		t.Fatalf("checkRegressions StepInput.Input = %q, want %q", in.Input, InputWorkerRetry)
	}
	if store.missions[m.ID].Plan.Units[0].Passes {
		t.Fatal("checkRegressions must flip the regressed unit's Passes back to false")
	}
}

// TestVerifierCheckRegressionsNoRegressionLeavesPassesAlone confirms a
// still-intact previously-passed unit is left untouched.
func TestVerifierCheckRegressionsNoRegressionLeavesPassesAlone(t *testing.T) {
	v := newFakeVerifier()
	store := v.store.(*fakeStore)
	workRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workRoot, "out.txt"), []byte("data"), 0o600); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	m := Mission{
		ID: "m1", Workspace: workRoot,
		Plan: Plan{Units: []PlanUnit{{Title: "earlier unit", Artifacts: []string{"out.txt"}, Passes: true}}},
	}
	store.put(m.ID, m)

	if _, regressed := v.checkRegressions(context.Background(), m); regressed {
		t.Fatal("checkRegressions = true, want false when the artifact is still intact")
	}
	if !store.missions[m.ID].Plan.Units[0].Passes {
		t.Fatal("checkRegressions must not touch a unit that did not regress")
	}
}
