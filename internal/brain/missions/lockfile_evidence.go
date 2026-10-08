package missions

// D-140 (issue #1011): when a coding mission's diff against its base
// changes a dependency lockfile, the harness runs the prepare step's
// baseline test command and osv-scanner audit again (D-130) and judges
// two criteria on that evidence alone: tests no worse than the baseline,
// and no more advisories than the baseline. A failed criterion keeps the
// owning unit's passes false. The result is stored on env_facts for the
// review packet and the PR body, and reused while the tree is unchanged.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// lockfileNames are the dependency lockfile basenames whose change in
// the mission's diff triggers the evidence criteria.
var lockfileNames = map[string]bool{
	"composer.lock": true, "package-lock.json": true, "npm-shrinkwrap.json": true,
	"pnpm-lock.yaml": true, "yarn.lock": true, "bun.lock": true, "bun.lockb": true,
	"uv.lock": true, "poetry.lock": true, "Pipfile.lock": true, "pdm.lock": true,
	"go.sum": true, "Gemfile.lock": true, "Cargo.lock": true, "gradle.lockfile": true,
	"packages.lock.json": true, "mix.lock": true, "pubspec.lock": true, "Podfile.lock": true,
	"Package.resolved": true,
}

// Criterion statuses on LockfileCriterion.
const (
	lockfileMet         = "met"
	lockfileNotMet      = "not_met"
	lockfileNotMeasured = "not_measured"
)

// lockfileCheck is UnitVerification.Check for a failed criterion.
const lockfileCheck = "lockfile_evidence"

// lockfileTailCap bounds the failing test run's output tail kept.
const lockfileTailCap = 1500

// LockfileEvidence is one after-measurement of a lockfile change.
type LockfileEvidence struct {
	Lockfiles   []string            `json:"lockfiles"`
	Fingerprint string              `json:"fingerprint,omitempty"`
	TestCmd     string              `json:"test_cmd,omitempty"`
	TestExit    int                 `json:"test_exit"`
	TestTail    string              `json:"test_tail,omitempty"`
	TestsBefore *TestSummary        `json:"tests_before,omitempty"`
	TestsAfter  *TestSummary        `json:"tests_after,omitempty"`
	VulnsBefore *int                `json:"vulns_before,omitempty"`
	VulnsAfter  *int                `json:"vulns_after,omitempty"`
	AuditError  string              `json:"audit_error,omitempty"`
	Advisories  []string            `json:"advisories,omitempty"`
	Criteria    []LockfileCriterion `json:"criteria"`
}

// LockfileCriterion is one harness criterion and its outcome.
type LockfileCriterion struct {
	Text   string `json:"text"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Met reports whether no criterion is not_met.
func (e *LockfileEvidence) Met() bool {
	for _, c := range e.Criteria {
		if c.Status == lockfileNotMet {
			return false
		}
	}
	return true
}

// BeforeTests and AfterTests describe the test runs for reports.
func (e *LockfileEvidence) BeforeTests() string {
	if e.TestCmd == "" {
		return "not measured"
	}
	return describeTests(e.TestsBefore, 0)
}

func (e *LockfileEvidence) AfterTests() string {
	if e.TestCmd == "" {
		return "not measured"
	}
	return describeTests(e.TestsAfter, e.TestExit)
}

// BeforeVulns and AfterVulns describe the advisory counts for reports.
func (e *LockfileEvidence) BeforeVulns() string { return describeCount(e.VulnsBefore, "") }

func (e *LockfileEvidence) AfterVulns() string { return describeCount(e.VulnsAfter, e.AuditError) }

func describeTests(s *TestSummary, exit int) string {
	out := fmt.Sprintf("exit %d", exit)
	if s != nil && s.Parsed {
		out = fmt.Sprintf("%d passed, %d failed, %d warnings", s.Passed, s.Failed, s.Warnings)
		if exit != 0 {
			out += fmt.Sprintf(" (exit %d)", exit)
		}
	}
	return out
}

func describeCount(n *int, failure string) string {
	switch {
	case failure != "":
		return "audit failed"
	case n == nil:
		return "not measured"
	}
	return fmt.Sprint(*n)
}

// changedLockfiles filters a diff's paths down to lockfiles, sorted.
func changedLockfiles(files []string) []string {
	var out []string
	for _, f := range files {
		if lockfileNames[path.Base(f)] {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// lockfileOwners picks the units the evidence gates: every unit whose
// artifacts or scope cover a changed lockfile, else the current unit,
// else the last one.
func lockfileOwners(units []PlanUnit, lockfiles []string, current int) map[int]bool {
	if len(lockfiles) == 0 {
		return nil
	}
	owners := map[int]bool{}
	for i, u := range units {
		for _, l := range lockfiles {
			if inScope(l, u.Artifacts) || (len(u.Scope) > 0 && inScope(l, u.Scope)) {
				owners[i] = true
			}
		}
	}
	if len(owners) == 0 && len(units) > 0 {
		if current < 0 || current >= len(units) {
			current = len(units) - 1
		}
		owners[current] = true
	}
	return owners
}

// securityGoalWords mark a goal that asks for a security fix.
var securityGoalWords = []string{"security", "vulnerab", "cve", "advisor", "ghsa", "osv"}

// securityGoal reports whether the goal asks for a security fix.
func securityGoal(goal string) bool {
	g := strings.ToLower(goal)
	for _, w := range securityGoalWords {
		if strings.Contains(g, w) {
			return true
		}
	}
	return false
}

// lockfileCriteria judges the two criteria on a measured evidence.
func lockfileCriteria(e *LockfileEvidence, security bool) []LockfileCriterion {
	tests := LockfileCriterion{Text: "The baseline test command still exits 0 with no fewer passing tests and no new failures or warnings"}
	switch {
	case e.TestCmd == "":
		tests.Status, tests.Detail = lockfileNotMeasured, "prepare found no baseline test command"
	case e.TestExit != 0:
		tests.Status = lockfileNotMet
		tests.Detail = fmt.Sprintf("`%s` exited %d; after: %s", e.TestCmd, e.TestExit, e.AfterTests())
	default:
		tests.Status = lockfileMet
		tests.Detail = "before: " + e.BeforeTests() + "; after: " + e.AfterTests()
		if b, a := e.TestsBefore, e.TestsAfter; b != nil && a != nil && b.Parsed && a.Parsed {
			var worse []string
			if a.Passed < b.Passed {
				worse = append(worse, fmt.Sprintf("passing tests fell from %d to %d", b.Passed, a.Passed))
			}
			if a.Failed > b.Failed {
				worse = append(worse, fmt.Sprintf("failures rose from %d to %d", b.Failed, a.Failed))
			}
			if a.Warnings > b.Warnings {
				worse = append(worse, fmt.Sprintf("warnings rose from %d to %d", b.Warnings, a.Warnings))
			}
			if len(worse) > 0 {
				tests.Status = lockfileNotMet
				tests.Detail = strings.Join(worse, ", ") + " (" + tests.Detail + ")"
			}
		}
	}
	audit := LockfileCriterion{Text: "The osv-scanner advisory count is not higher than the baseline"}
	if security {
		audit.Text += "; for this security goal it reaches 0, or the report names each remaining advisory with the reason (such as no fixed version)"
	}
	switch {
	case e.VulnsBefore == nil:
		audit.Status, audit.Detail = lockfileNotMeasured, "prepare recorded no baseline audit"
	case e.AuditError != "":
		audit.Status, audit.Detail = lockfileNotMet, "the audit after the change failed: "+e.AuditError
	case e.VulnsAfter != nil && *e.VulnsAfter > *e.VulnsBefore:
		audit.Status, audit.Detail = lockfileNotMet, fmt.Sprintf("advisories rose from %d to %d", *e.VulnsBefore, *e.VulnsAfter)
	default:
		audit.Status, audit.Detail = lockfileMet, "before: "+e.BeforeVulns()+"; after: "+e.AfterVulns()
	}
	if audit.Status != lockfileNotMeasured && len(e.Advisories) > 0 {
		audit.Detail += "; remaining: " + strings.Join(e.Advisories, ", ")
	}
	return []LockfileCriterion{tests, audit}
}

// renderLockfileEvidence renders the criteria for a worker excerpt or
// the review packet.
func renderLockfileEvidence(e *LockfileEvidence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Lockfiles changed: %s. Harness criteria (measured by the harness, D-140):\n", NeutralizeSlot(strings.Join(e.Lockfiles, ", ")))
	for i, c := range e.Criteria {
		fmt.Fprintf(&b, "%d. %s: %s (%s)\n", i+1, c.Text, c.Status, NeutralizeSlot(c.Detail))
	}
	if e.TestTail != "" {
		fmt.Fprintf(&b, "Test output tail:\n%s\n", NeutralizeSlot(e.TestTail))
	}
	return b.String()
}

// lockfileEvidence measures the evidence for the changed lockfiles, or
// returns the stored one when the tree is unchanged since. nil without a
// prepare baseline. An exec error other than a timeout is returned.
func (v *verifier) lockfileEvidence(ctx context.Context, m Mission, lockfiles []string) (*LockfileEvidence, error) {
	if m.EnvFacts == nil || m.EnvFacts.Prepare == nil {
		return nil, nil
	}
	prep, wt := m.EnvFacts.Prepare, m.WorktreePath()
	fp := worktreeFingerprint(ctx, wt)
	if prev := m.EnvFacts.Lockfile; prev != nil && fp != "" && prev.Fingerprint == fp && slices.Equal(prev.Lockfiles, lockfiles) {
		return prev, nil
	}
	e := &LockfileEvidence{Lockfiles: lockfiles, Fingerprint: fp, TestCmd: prep.TestCmd, TestsBefore: prep.Tests}
	if e.TestCmd != "" {
		code, out, timedOut, err := v.exec(ctx, m, wt, buildTestCmd(testCandidate{Cmd: e.TestCmd}), prepareTestTimeout)
		if err != nil {
			return nil, fmt.Errorf("lockfile evidence: test run: %w", err)
		}
		s := parseTestSummary(out)
		e.TestExit, e.TestsAfter = code, &s
		if timedOut {
			e.TestExit = -1
			out += "\n[timed out after " + prepareTestTimeout.String() + "]"
		}
		if e.TestExit != 0 {
			e.TestTail = tailString(strings.TrimSpace(out), lockfileTailCap)
		}
	}
	if before, ok := baselineVulns(prep, wt); ok {
		e.VulnsBefore = &before
		if err := v.auditAfter(ctx, m, wt, e); err != nil {
			return nil, err
		}
	}
	e.Criteria = lockfileCriteria(e, securityGoal(m.Goal))
	payload := map[string]any{
		"lockfiles": e.Lockfiles, "met": e.Met(), "test_cmd": e.TestCmd, "test_exit": e.TestExit,
		"tests_before": e.TestsBefore, "tests_after": e.TestsAfter, "vulns_before": e.VulnsBefore,
		"vulns_after": e.VulnsAfter, "criteria": e.Criteria,
	}
	if e.AuditError != "" {
		payload["audit_error"] = e.AuditError
	}
	if err := v.store.AppendEvent(ctx, m.ID, "mission.lockfile_evidence", payload); err != nil {
		v.log.Warn("driver: record lockfile evidence failed", "mission_id", m.ID, "error", err)
	}
	v.storeLockfileEvidence(ctx, m, e)
	return e, nil
}

// auditAfter runs the prepare audit over the worktree's lockfiles now
// into <workspace>/prepare/osv-after.json and fills VulnsAfter and
// Advisories, or AuditError.
func (v *verifier) auditAfter(ctx context.Context, m Mission, wt string, e *LockfileEvidence) error {
	zero := 0
	lockfiles := planPrepare(wt, walkManifests(wt)).Lockfiles
	if len(lockfiles) == 0 {
		e.VulnsAfter = &zero
		return nil
	}
	dir := filepath.Join(m.Workspace, prepareDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		e.AuditError = "creating " + prepareDir + ": " + err.Error()
		return nil
	}
	outFile := filepath.Join(dir, "osv-after.json")
	_ = os.Remove(outFile)
	code, out, timedOut, err := v.exec(ctx, m, wt, buildAuditCmd(lockfiles, outFile, v.osvOffline), prepareAuditTimeout)
	switch {
	case err != nil:
		return fmt.Errorf("lockfile evidence: audit: %w", err)
	case timedOut:
		e.AuditError = "timed out after " + prepareAuditTimeout.String()
		return nil
	case code == osvExitNoPackages:
		e.VulnsAfter = &zero
		return nil
	case code != 0 && code != osvExitAdvisories:
		e.AuditError = fmt.Sprintf("osv-scanner exited %d: %s", code, tailString(strings.TrimSpace(out), prepareFactTailCap))
		return nil
	}
	raw, err := os.ReadFile(outFile) //nolint:gosec // harness-owned path under the mission workspace
	if err != nil {
		e.AuditError = "reading the report: " + err.Error()
		return nil
	}
	audit, err := parseOSVReport(raw, wt)
	if err != nil {
		e.AuditError = "parsing the report: " + err.Error()
		return nil
	}
	total := 0
	for _, a := range audit {
		total += a.Vulnerabilities
	}
	e.VulnsAfter, e.Advisories = &total, osvAdvisoryIDs(raw)
	return nil
}

// baselineVulns sums the prepare audit's advisories; ok is false when
// prepare has no successful audit. The harness-generated npm lockfile
// counts only once the repo has a root node lockfile, since the after
// audit never regenerates it.
func baselineVulns(prep *PrepareFacts, wt string) (int, bool) {
	ok := false
	for _, s := range prep.Steps {
		if s.Name == "audit" && s.OK {
			ok = true
		}
	}
	if !ok {
		return 0, false
	}
	nodeLock := anyFileExists(wt, nodeLockfiles)
	total := 0
	for _, a := range prep.Audit {
		if a.Path == npmGeneratedLockLabel && !nodeLock {
			continue
		}
		total += a.Vulnerabilities
	}
	return total, true
}

// exec runs one command in the worktree through the sandbox with its
// output captured; a hit timeout is reported, not returned as an error.
func (v *verifier) exec(ctx context.Context, m Mission, wt, command string, timeout time.Duration) (code int, out string, timedOut bool, err error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var buf bytes.Buffer
	code, err = v.sandboxExec(cctx, m.ID, m.Environment, wt, command, timeout, &buf)
	if errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return code, stripANSI(buf.String()), true, nil
	}
	return code, stripANSI(buf.String()), false, err
}

// storeLockfileEvidence writes e (nil clears it) onto env_facts.
func (v *verifier) storeLockfileEvidence(ctx context.Context, m Mission, e *LockfileEvidence) {
	if m.EnvFacts == nil {
		return
	}
	facts := *m.EnvFacts
	facts.Lockfile = e
	if err := v.store.SetEnvFacts(ctx, m.ID, facts); err != nil {
		v.log.Warn("driver: store lockfile evidence failed", "mission_id", m.ID, "error", err)
	}
}

// worktreeFingerprint identifies the worktree state: HEAD, the status
// and the uncommitted diff. "" when git cannot answer.
func worktreeFingerprint(ctx context.Context, wt string) string {
	cctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	h := sha256.New()
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"status", "--porcelain"}, {"diff", "HEAD"}} {
		out, err := runGit(cctx, wt, args...)
		if err != nil {
			return ""
		}
		h.Write([]byte(out))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// osvAdvisoryIDs lists the distinct advisory ids an osv-scanner report
// names, one per vulnerability group (its first id), sorted.
func osvAdvisoryIDs(raw []byte) []string {
	var rep struct {
		Results []struct {
			Packages []struct {
				Groups []struct {
					IDs []string `json:"ids"`
				} `json:"groups"`
				Vulnerabilities []struct {
					ID string `json:"id"`
				} `json:"vulnerabilities"`
			} `json:"packages"`
		} `json:"results"`
	}
	if json.Unmarshal(raw, &rep) != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, r := range rep.Results {
		for _, p := range r.Packages {
			if len(p.Groups) > 0 {
				for _, g := range p.Groups {
					if len(g.IDs) > 0 {
						seen[g.IDs[0]] = true
					}
				}
				continue
			}
			for _, vuln := range p.Vulnerabilities {
				seen[vuln.ID] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
