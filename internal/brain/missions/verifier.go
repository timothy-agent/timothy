package missions

// D-074: verifier holds the harness-evidence slice split out of Driver.
// D-094 (issue #518) reshapes it into one batch pass: verifyAll checks
// every unit after a worker turn (and again on a review approval) and
// returns the outcomes; Step folds them into the plan and ApplyTransition
// persists them. The verifier itself never writes the plan.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// verifier runs the harness's own deterministic checks (declared
// artifacts, citations, verify_cmd) for a mission's plan units.
type verifier struct {
	store       driverStore
	sandboxExec sandboxExec
	log         *slog.Logger
}

// verifyAll runs the harness checks for every plan unit and returns one
// UnitVerification per unit checked:
//   - a unit not yet harness-passed gets CheckArtifacts, then (citations
//     true, general missions only, D-059) CheckCitations against seenURLs,
//     then verify_cmd. A unit after the current one whose artifacts are
//     missing is skipped: not started yet, nothing to record.
//   - an already harness-passed unit gets CheckArtifacts plus verify_cmd
//     as the regression subset.
//
// A hung verify_cmd counts as failed (RunVerifyWithBackend's TimedOut);
// any other exec error aborts the pass as an infrastructure failure.
func (v *verifier) verifyAll(ctx context.Context, m Mission, seenURLs []string, citations bool) ([]UnitVerification, error) {
	workRoot := m.WorkRoot()
	current := firstUnverified(m.Plan)
	checkCitations := citations && missionPolicyFor(m).checksCitations
	var out []UnitVerification
	for i, u := range m.Plan.Units {
		res := UnitVerification{Unit: i}
		if problems := CheckArtifacts(workRoot, u.Artifacts); len(problems) > 0 {
			if !u.verified() && i != current {
				continue
			}
			res.Check, res.Excerpt = "artifacts", artifactsExcerpt(workRoot, problems)
		} else if checkCitations && !u.verified() {
			if problems := CheckCitations(workRoot, u.Artifacts, seenURLs); len(problems) > 0 {
				res.Check, res.Excerpt = "citations", "citation check failed:\n"+strings.Join(problems, "\n")
			}
		}
		payload := map[string]any{"unit": i, "regression": u.verified()}
		if res.Check == "" {
			if u.VerifyCmd == "" {
				res.Passed, res.Check = true, "artifacts"
			} else {
				r, err := v.runVerify(ctx, m.ID, m.Environment, workRoot, u.VerifyCmd)
				if err != nil {
					return nil, fmt.Errorf("driver: verify unit %d: %w", i, err)
				}
				res.Passed, res.Check, res.Excerpt = r.Passed, "verify_cmd", r.Excerpt
				if r.TimedOut {
					res.Check = "timeout"
				}
				payload["exit_code"], payload["output_sha256"] = r.ExitCode, r.OutputSHA256
			}
		}
		payload["passed"], payload["check"] = res.Passed, res.Check
		if !res.Passed {
			payload["excerpt"] = truncate(res.Excerpt, 500)
		}
		if err := v.store.AppendEvent(ctx, m.ID, "mission.unit_verified", payload); err != nil {
			v.log.Warn("driver: record verify failed", "mission_id", m.ID, "error", err)
		}
		out = append(out, res)
	}
	return out, nil
}

// artifactsExcerpt explains a CheckArtifacts failure and shows what
// DOES exist: the dominant failure is a worker writing a real file
// under a slightly different name and never spotting the mismatch from
// "not found" alone.
func artifactsExcerpt(workRoot string, problems []string) string {
	excerpt := "declared artifacts failed the harness check:\n" + strings.Join(problems, "\n")
	if listing := ListWorkspace(workRoot); listing != "" {
		return excerpt + "\nfiles currently in the workspace:\n" + listing
	}
	return excerpt + "\nthe workspace is currently empty"
}

// failedUnits filters a batch pass down to the units that did not pass.
func failedUnits(verified []UnitVerification) []UnitVerification {
	var out []UnitVerification
	for _, v := range verified {
		if !v.Passed {
			out = append(out, v)
		}
	}
	return out
}

// runVerify executes verify_cmd via the mission's sandbox container,
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
