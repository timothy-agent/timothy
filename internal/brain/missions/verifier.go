package missions

// D-074: verifier holds the harness-evidence slice split out of Driver
// (verifyCurrentUnit, markUnitPassed, checkRegressions, regressed) — a
// pure extraction, no behavior change. This makes the harness-evidence
// invariant (only verifier flips a unit's Passes flag, never model
// output) type-visible: Driver's own runExecute/runReview/trySkipReview
// call into it, but never write Passes any other way.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// verifier runs the harness's own deterministic checks (declared
// artifacts, verify_cmd) for a mission's plan units and is the only
// thing allowed to flip a PlanUnit's Passes flag.
type verifier struct {
	store       driverStore
	sandboxExec sandboxExec
	log         *slog.Logger
}

// verifyFailure is a unit's verify_cmd genuinely running and reporting
// failure — real evidence the reviewer's approval didn't hold up, not
// an infrastructure fault. Kept distinct from a plain error (RunVerify
// itself erroring: timeout, exec failure) so the caller can route each
// to a different StepInput instead of conflating both as infra.
type verifyFailure struct {
	unit    int
	excerpt string
}

func (e *verifyFailure) Error() string {
	return fmt.Sprintf("driver: unit %d verify_cmd failed", e.unit)
}

// verifyCurrentUnit runs the harness's own checks for the plan's
// current (first unverified) unit and marks it passed — only this
// harness-run evidence may flip a unit's Passes flag, never model
// output. Declared artifacts are checked first (exists, non-empty),
// then (general missions only, D-059) that every URL cited in those
// artifacts was actually seen by the worker via fetch_url/search_web
// this turn — a tautological verify_cmd cannot pass a unit whose
// artifact was never written, or whose citations were invented.
// seenURLs is only ever populated by the caller right after the
// worker turn that produced it (runExecute); it is empty on the later
// runReview call, which is fine — for a general mission trySkipReview
// already ran this exact check with the real evidence, and runReview
// only reaches this path for coding missions (out of scope) or the
// rare infra-failure fallback.
func (v *verifier) verifyCurrentUnit(ctx context.Context, m Mission, seenURLs []string) error {
	for i, u := range m.Spec.Units {
		if u.Passes {
			continue
		}
		workRoot := m.WorkRoot()
		if problems := CheckArtifacts(workRoot, u.Artifacts); len(problems) > 0 {
			excerpt := "declared artifacts failed the harness check:\n" + strings.Join(problems, "\n")
			// Show what DOES exist: the dominant failure here is a
			// worker writing a real file under a slightly different
			// name and never spotting the mismatch from "not found"
			// alone.
			if listing := ListWorkspace(workRoot); listing != "" {
				excerpt += "\nfiles currently in the workspace:\n" + listing
			} else {
				excerpt += "\nthe workspace is currently empty"
			}
			if err := v.store.AppendEvent(ctx, m.ID, "mission.unit_verified", map[string]any{
				"unit": i, "passed": false, "check": "artifacts", "problems": problems,
			}); err != nil {
				v.log.Warn("driver: record verify failed", "mission_id", m.ID, "error", err)
			}
			return &verifyFailure{unit: i, excerpt: excerpt}
		}
		if missionPolicyFor(m).checksCitations {
			if problems := CheckCitations(workRoot, u.Artifacts, seenURLs); len(problems) > 0 {
				excerpt := "citation check failed:\n" + strings.Join(problems, "\n")
				if err := v.store.AppendEvent(ctx, m.ID, "mission.unit_verified", map[string]any{
					"unit": i, "passed": false, "check": "citations", "problems": problems,
				}); err != nil {
					v.log.Warn("driver: record verify failed", "mission_id", m.ID, "error", err)
				}
				return &verifyFailure{unit: i, excerpt: excerpt}
			}
		}
		if u.VerifyCmd == "" {
			return v.markUnitPassed(ctx, m, i)
		}
		res, err := v.runVerify(ctx, m.ID, m.Environment, workRoot, u.VerifyCmd)
		if err != nil {
			return fmt.Errorf("driver: verify unit %d: %w", i, err)
		}
		if err := v.store.AppendEvent(ctx, m.ID, "mission.unit_verified", map[string]any{
			"unit": i, "passed": res.Passed, "check": "verify_cmd", "exit_code": res.ExitCode, "output_sha256": res.OutputSHA256,
		}); err != nil {
			v.log.Warn("driver: record verify failed", "mission_id", m.ID, "error", err)
		}
		if !res.Passed {
			return &verifyFailure{unit: i, excerpt: res.Excerpt}
		}
		return v.markUnitPassed(ctx, m, i)
	}
	return nil
}

// markUnitPassed persists unit as passed. It copies Units before
// mutating — m.Spec.Units is a slice header, so writing through it
// in place would silently mutate the caller's own Mission value too
// (same backing array), corrupting whatever that caller does with it
// afterward in the same round (e.g. Advance's toStepState call).
func (v *verifier) markUnitPassed(ctx context.Context, m Mission, unit int) error {
	units := make([]PlanUnit, len(m.Spec.Units))
	copy(units, m.Spec.Units)
	units[unit].Passes = true
	return v.store.SetSpec(ctx, m.ID, Spec{Units: units})
}

// checkRegressions re-verifies every unit that had ALREADY passed
// (excluding whichever unit the caller just verified) after a unit's
// own verify_cmd/CheckArtifacts just succeeded — a later unit's work
// can silently break an earlier unit's artifacts, and passes today
// only ever moves forward, never re-checked. Cheap and deterministic
// (harness-side shell, same as verifyCurrentUnit), capped at the
// spec's own unit count. Re-fetches the mission first: verifyCurrentUnit's
// SetSpec write is not reflected in the caller's in-memory m.
func (v *verifier) checkRegressions(ctx context.Context, m Mission) (StepInput, bool) {
	fresh, err := v.store.Get(ctx, m.ID)
	if err != nil {
		v.log.Warn("driver: regression check reload failed", "mission_id", m.ID, "error", err)
		return StepInput{}, false
	}
	// Deliberately omits verifyCurrentUnit's CheckCitations step: that
	// check validates URLs against seenURLs, which only ever holds the
	// citations from the turn that just produced the current unit's
	// output (runExecute's live evidence). A regression recheck has no
	// such turn — it's re-verifying units OTHER than the one just
	// worked — so seenURLs would be empty here and CheckCitations would
	// false-fail every already-passed unit on every regression pass.
	workRoot := fresh.WorkRoot()
	for i, u := range fresh.Spec.Units {
		if !u.Passes {
			continue
		}
		if problems := CheckArtifacts(workRoot, u.Artifacts); len(problems) > 0 {
			return v.regressed(ctx, fresh, i, "artifacts", strings.Join(problems, "\n"))
		}
		if u.VerifyCmd == "" {
			continue
		}
		res, err := v.runVerify(ctx, fresh.ID, fresh.Environment, workRoot, u.VerifyCmd)
		if err != nil {
			v.log.Warn("driver: regression re-verify errored", "mission_id", fresh.ID, "unit", i, "error", err)
			continue
		}
		if !res.Passed {
			return v.regressed(ctx, fresh, i, "verify_cmd", res.Excerpt)
		}
	}
	return StepInput{}, false
}

// regressed flips a previously-passed unit back to unverified, records
// mission.regression, and returns the same StepInput shape a failed
// current-unit verify produces — the existing worker-retry/stall
// machinery drives the fix with zero statemachine changes.
func (v *verifier) regressed(ctx context.Context, m Mission, unit int, check, excerpt string) (StepInput, bool) {
	units := make([]PlanUnit, len(m.Spec.Units))
	copy(units, m.Spec.Units)
	title := units[unit].Title
	units[unit].Passes = false
	if err := v.store.SetSpec(ctx, m.ID, Spec{Units: units}); err != nil {
		v.log.Warn("driver: record regression spec write failed", "mission_id", m.ID, "error", err)
	}
	if err := v.store.AppendEvent(ctx, m.ID, "mission.regression", map[string]any{"unit": title, "check": check}); err != nil {
		v.log.Warn("driver: record regression event failed", "mission_id", m.ID, "error", err)
	}
	note := fmt.Sprintf("Regression: unit %q, previously verified, now fails its %s check:\n%s", title, check, excerpt)
	if err := v.recordProgress(ctx, m.ID, note); err != nil {
		v.log.Warn("driver: record regression progress note failed", "mission_id", m.ID, "error", err)
	}
	fp := "regression:" + title
	return StepInput{Input: InputWorkerRetry, Reason: truncate(note, 500), GapFingerprint: fp}, true
}

// recordProgress mirrors Driver.recordProgress — duplicated rather than
// shared since it's a two-line wrapper around store.AppendProgress and
// verifier must not depend back on Driver.
func (v *verifier) recordProgress(ctx context.Context, id, text string) error {
	if text == "" {
		return nil
	}
	return v.store.AppendProgress(ctx, id, NeutralizeSlot(truncate(text, 2000)))
}

// runVerify executes verify_cmd via the mission's sandbox container —
// the verify-side counterpart of nativeRunner routing shell/write_file
// through the same backend. environment (D-05x) only matters on the
// mission's first exec, since a container's image is fixed once
// created.
func (v *verifier) runVerify(ctx context.Context, missionID, environment, workRoot, verifyCmd string) (VerifyResult, error) {
	backend := func(ctx context.Context, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		return v.sandboxExec(ctx, missionID, environment, workdir, command, timeout, out)
	}
	return RunVerifyWithBackend(ctx, backend, workRoot, verifyCmd)
}
