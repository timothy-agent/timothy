package missions

// Plan gate checks (issue #718): run at plan acceptance, before any
// worker turn, and reject a plan whose check_cmd cannot exit 0, always
// exits 0, or never exercises the code it guards. The planner's
// recovery turn sees the exact defect.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// emptyOutputIdiom matches `| grep -q '^$'` in any quoting: the wrong
// spelling of "prints nothing". grep finds no empty line in empty
// input, so the pipeline fails on clean output too.
var emptyOutputIdiom = regexp.MustCompile(`\|\s*grep(\s+-\w+)*\s+(['"]?)\^\$(['"]?)(\s|$)`)

// emptyOutputHint is the sanctioned spelling, matching the awk form the
// planner rules already use for line counts (command substitution is
// banned, so `[ -z "$(...)" ]` is not available to it).
const emptyOutputHint = "awk 'END{exit NR>0}'"

// codeExtensions marks artifacts a check_cmd must build, test or run
// rather than only grep.
var codeExtensions = map[string]bool{
	".go": true, ".py": true,
	".js": true, ".mjs": true, ".cjs": true, ".jsx": true, ".ts": true, ".tsx": true,
}

// toolchainByEnv recognises an invocation of the environment's own
// toolchain (sandboxd's go/node/python variants). The alternation is a
// floor, not a full grammar: any one match satisfies checkCodeFloor.
var toolchainByEnv = map[string]*regexp.Regexp{
	"go":     regexp.MustCompile(`(^|[\s;&|(])go\s+(test|vet|build|run)\b`),
	"python": regexp.MustCompile(`(^|[\s;&|(])(pytest\b|python3?\s+(-m\s+(pytest|unittest)\b|\S+\.py\b))`),
	"node":   regexp.MustCompile(`(^|[\s;&|(])(npm\s+(run\s+)?test\b|npx\s+(vitest|jest|mocha|tsc)\b|node\s+(--test\b|\S+\.[cm]?js\b)|(yarn|pnpm)\s+test\b|tsc\b)`),
}

// toolchainExample is what the rejection tells the planner to reach for.
var toolchainExample = map[string]string{
	"go":     "go test ./<package>/...",
	"python": "python3 -m pytest <tests>",
	"node":   "npm test",
}

// anchorExample is the "fails before, passes after" shape for a unit
// that adds tests: the toolchain call alone exits 0 while the test file
// is still missing (`go test -run X` matches nothing, pytest collects
// nothing), so a grep for a symbol the unit adds goes first.
var anchorExample = map[string]string{
	"go":     "grep -q 'func TestXxx' <pkg>/xxx_test.go && go test ./<pkg>/ -run TestXxx",
	"python": "grep -q 'def test_xxx' tests/test_xxx.py && python3 -m pytest tests/test_xxx.py -q",
	"node":   "grep -q 'test(' src/xxx.test.ts && npx vitest run src/xxx.test.ts",
}

// checkPlanGates runs the static gate checks parsePlan cannot: they
// need the mission's kind and environment.
func checkPlanGates(plan Plan, m Mission) error {
	for _, u := range plan.Units {
		if emptyOutputIdiom.MatchString(u.CheckCmd) {
			return fmt.Errorf("mission runner: unit %q check_cmd pipes into grep -q '^$', which exits 1 on empty output as well as on filenames, so it can never pass; to assert a command prints nothing pipe it into %s instead", u.Title, emptyOutputHint)
		}
	}
	if err := checkBootstrap(plan, m); err != nil {
		return err
	}
	if err := checkOwnArtifacts(plan); err != nil {
		return err
	}
	if err := checkUntrackedAssumptions(plan); err != nil {
		return err
	}
	if err := checkReportArtifacts(plan, m); err != nil {
		return err
	}
	if m.PlanGate.RepoDestination {
		if err := checkHarnessDelivery(plan); err != nil {
			return err
		}
	}
	if m.Kind != KindCoding {
		return nil
	}
	if err := checkUnitGranularity(plan, m); err != nil && !m.PlanGate.granularityWaivable() {
		return err
	}
	return checkCodeFloor(plan, m.Environment)
}

// deliveryPattern matches a unit that pushes a branch or opens a pull
// request. Kept narrow: a false match drops a real unit.
var deliveryPattern = regexp.MustCompile(`(?i)\bgit\s+push\b|\bgh\s+pr\b|\b(open|create|raise|submit)(s|ing)?\s+(a\s+|the\s+)?(prs?|pull[ -]requests?)\b|\bpush(es|ing)?\s+(the\s+|a\s+)?(mission\s+|feature\s+)?branch\b`)

// pushesOrOpensPR reports whether a unit's title or check_cmd delivers
// the branch itself.
func pushesOrOpensPR(title, checkCmd string) bool {
	return deliveryPattern.MatchString(title) || deliveryPattern.MatchString(checkCmd)
}

// checkHarnessDelivery rejects a push or PR unit on a mission with a
// repo destination (issue #1007): the result phase pushes the mission
// branch and opens the PR, so such a unit can never pass a gate.
func checkHarnessDelivery(plan Plan) error {
	for _, u := range plan.Units {
		if pushesOrOpensPR(u.Title, u.CheckCmd) {
			return fmt.Errorf("mission runner: unit %q pushes a branch or opens a pull request, but this mission has a repository destination: the harness pushes the mission branch and opens the PR itself after all units pass, so drop that unit from the plan", u.Title)
		}
	}
	return nil
}

// checkBootstrap enforces the bootstrap-unit contract (D-124, issue
// #980): coding missions only (issue #996), at most one, first in the
// plan, and still gated by a check_cmd.
func checkBootstrap(plan Plan, m Mission) error {
	first := -1
	for i, u := range plan.Units {
		if !u.Bootstrap {
			continue
		}
		if m.Kind != KindCoding {
			return fmt.Errorf("mission runner: unit %q is marked bootstrap, but only coding missions install toolchains; drop the bootstrap unit and plan the deliverable with the tools the sandbox has", u.Title)
		}
		if first >= 0 {
			return fmt.Errorf("mission runner: units %q and %q are both marked bootstrap; only one unit may install the toolchain, so merge them into a single first unit", plan.Units[first].Title, u.Title)
		}
		first = i
		if i != 0 {
			return fmt.Errorf("mission runner: bootstrap unit %q must be the first unit of the plan, since later units depend on the toolchain it installs; move it first", u.Title)
		}
		if strings.TrimSpace(u.CheckCmd) == "" {
			return fmt.Errorf("mission runner: bootstrap unit %q needs a check_cmd that runs the installed binary by its workspace-relative path; it is still a gate", u.Title)
		}
	}
	return nil
}

// smallPlanArtifactCap is the plan size below which a single-directory
// coding plan is one unit: each extra unit costs a CLI session
// bootstrap and a reviewed evidence set (issue #720).
const smallPlanArtifactCap = 8

// granularityMarker is in every checkUnitGranularity rejection; the
// driver finds an earlier rejection in the event log by it.
const granularityMarker = "they are one unit of work"

// checkUnitGranularity rejects a coding plan that splits a small
// single-directory change into several units. Every extra unit pays
// for another session bootstrap and another evidence set, and the files
// share a package, so the work is one unit. Applies in transcribe mode
// too (D-102): an operator's file list is not a unit split. A bootstrap
// unit (D-124) is never merged: it must stay first and separate. Only
// code artifacts count (issue #1007): units with none (manifests,
// lockfiles, reports, evidence-only units) are left out of the merge.
func checkUnitGranularity(plan Plan, m Mission) error {
	dir := ""
	count := 0
	var titles []string
	for _, u := range plan.Units {
		if u.Bootstrap {
			continue
		}
		code := 0
		for _, a := range u.Artifacts {
			if !codeExtensions[strings.ToLower(filepath.Ext(strings.TrimSpace(a)))] {
				continue
			}
			code++
			d := path.Dir(cleanArtifact(a))
			if dir == "" {
				dir = d
			} else if d != dir {
				return nil
			}
		}
		if code > 0 {
			count += code
			titles = append(titles, strconv.Quote(u.Title))
		}
	}
	if len(titles) < 2 || count > smallPlanArtifactCap {
		return nil
	}
	if dir == "." {
		dir = "the workspace root"
	}
	return fmt.Errorf("mission runner: units %s all produce code files in %s and the plan has only %d of them, so %s; merge them into a single unit with the combined artifacts, criteria and one check_cmd covering all of them", strings.Join(titles, ", "), dir, count, granularityMarker)
}

// checkOwnArtifacts rejects a unit whose every artifact is already
// produced by an earlier unit: a trailing "format and verify" unit.
// Such a unit adds nothing the gate can key on, so its check_cmd
// either already passes (tautology) or waits on work the earlier units
// own; its commands belong in those units' check_cmds. Five eval arms
// produced one, four died on it.
func checkOwnArtifacts(plan Plan) error {
	seen := map[string]bool{}
	for _, u := range plan.Units {
		if len(u.Artifacts) > 0 {
			own := false
			for _, a := range u.Artifacts {
				if !seen[cleanArtifact(a)] {
					own = true
					break
				}
			}
			if !own {
				return fmt.Errorf("mission runner: unit %q adds no artifact of its own (every file it lists is produced by an earlier unit), so no check_cmd can tell whether it is done; remove the unit and put its commands into the check_cmds of the units that produce those files", u.Title)
			}
		}
		for _, a := range u.Artifacts {
			seen[cleanArtifact(a)] = true
		}
	}
	return nil
}

// reportExtensions are the file types an analysis or report lands in.
var reportExtensions = map[string]bool{".md": true, ".markdown": true, ".txt": true, ".rst": true}

// checkReportArtifacts keeps unrequested reports out of someone's
// repository (D-134, issue #1039): on a coding mission over a repo
// source, a unit may not list a new markdown or text file the goal does
// not name. Existing files (README, CHANGELOG) and files whose base
// name or top directory the goal names stay allowed.
func checkReportArtifacts(plan Plan, m Mission) error {
	if m.Kind != KindCoding || m.RepoURL() == "" {
		return nil
	}
	goal := strings.ToLower(m.Goal)
	for _, u := range plan.Units {
		for _, a := range u.Artifacts {
			p := cleanArtifact(a)
			if !reportExtensions[strings.ToLower(path.Ext(p))] || !filepath.IsLocal(p) {
				continue
			}
			if strings.Contains(goal, strings.ToLower(path.Base(p))) || goalNamesDir(goal, p) {
				continue
			}
			if _, err := os.Lstat(filepath.Join(m.WorkRoot(), filepath.FromSlash(p))); err == nil {
				continue
			}
			return fmt.Errorf("mission runner: unit %q lists %s as an artifact, but the goal names no such file and the repository does not have it; an analysis or report the goal did not ask to save as a file stays out of the repository: remove %s from the plan (drop the unit if that file is all it produces) and have the worker put the report text in mission_status's final_output on done, which the harness carries in the mission result and the pull request body", u.Title, p, p)
		}
	}
	return nil
}

// goalNamesDir reports whether a nested artifact's top directory is a
// word in the goal ("put the notes under docs/", "... under docs.").
func goalNamesDir(goal, p string) bool {
	top, _, nested := strings.Cut(p, "/")
	if !nested {
		return false
	}
	return regexp.MustCompile(`(^|[^\w.-])` + regexp.QuoteMeta(strings.ToLower(top)) + `($|[^\w.-]|\.(\s|$))`).MatchString(goal)
}

// untrackedClaim matches an assumption saying a file stays out of git.
var untrackedClaim = regexp.MustCompile(`(?i)\buntracked\b|\bnot\s+(be\s+)?(tracked|committed)\b|\bnever\s+(be\s+)?committed\b|\buncommitted\b`)

// pathToken matches a file-like token: a name with a letter-led
// extension (never a version like 11.0), or a slash-separated path.
var pathToken = regexp.MustCompile(`[\w.-]*[\w-]\.[A-Za-z][A-Za-z0-9]*\b|[\w.-]+(/[\w.-]+)+`)

// isShellWordSep splits a check_cmd into words for path matching.
func isShellWordSep(r rune) bool {
	return strings.ContainsRune(" \t\n;&|()<>'\"=`", r)
}

// checkUntrackedAssumptions rejects a plan that assumes a file stays
// untracked while a unit lists it in artifacts or scope or uses it in
// check_cmd (D-134, issue #1039): CommitUnit stages untracked files
// under a unit's artifacts and scope, so the assumption cannot hold.
func checkUntrackedAssumptions(plan Plan) error {
	for _, as := range plan.Assumptions {
		text := strings.TrimSpace(as.Assumption + " " + as.Default)
		if !untrackedClaim.MatchString(text) {
			continue
		}
		for _, tok := range pathToken.FindAllString(text, -1) {
			p := cleanArtifact(tok)
			for _, u := range plan.Units {
				where := ""
				switch {
				case listsPath(u.Artifacts, p):
					where = "artifacts"
				case listsPath(u.Scope, p):
					where = "scope"
				case listsPath(strings.FieldsFunc(u.CheckCmd, isShellWordSep), p):
					where = "check_cmd"
				}
				if where != "" {
					return fmt.Errorf("mission runner: plan assumption %q says %s stays untracked, but unit %q uses it in its %s, and the harness commits the files a unit declares; either keep %s out of that unit's artifacts, scope and check_cmd, or drop the assumption", text, p, u.Title, where, p)
				}
			}
		}
	}
	return nil
}

// listsPath reports whether any non-blank entry of list cleans to p.
func listsPath(list []string, p string) bool {
	for _, s := range list {
		if strings.TrimSpace(s) != "" && cleanArtifact(s) == p {
			return true
		}
	}
	return false
}

func cleanArtifact(a string) string {
	return path.Clean(filepath.ToSlash(strings.TrimSpace(a)))
}

// checkCodeFloor requires every unit that produces source files to
// build, test or run them in its check_cmd. Grep against source proves
// text is present, never that code works; a worker can satisfy it by
// adding a comment, and one did.
func checkCodeFloor(plan Plan, environment string) error {
	for _, u := range plan.Units {
		if !producesCode(u.Artifacts) {
			continue
		}
		if invokesToolchain(u.CheckCmd, environment) {
			continue
		}
		example := toolchainExample[environment]
		if example == "" {
			example = "the environment's test command"
		}
		return fmt.Errorf("mission runner: unit %q produces source files but its check_cmd never builds, tests or runs them; start it with %s (greps may follow, they never stand alone)", u.Title, example)
	}
	return nil
}

func producesCode(artifacts []string) bool {
	for _, a := range artifacts {
		if codeExtensions[strings.ToLower(filepath.Ext(strings.TrimSpace(a)))] {
			return true
		}
	}
	return false
}

// invokesToolchain accepts the environment's own toolchain, or any
// known one when the environment is unset or unknown.
func invokesToolchain(cmd, environment string) bool {
	if re, ok := toolchainByEnv[environment]; ok {
		return re.MatchString(cmd)
	}
	for _, re := range toolchainByEnv {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// probeTimeout bounds one plan-time probe. Far below verifyTimeout: a
// probe that runs this long is inconclusive, never a rejection.
const probeTimeout = 60 * time.Second

// probeCheckCmds runs every unit's check_cmd once against the tree as
// it stands before any work, in the mission sandbox. Two outcomes
// reject the plan: exit 0 (the gate cannot tell done from not done) and
// a missing command (exit 127, or the shell's "not found" line). Any
// other failure is the expected state of a gate whose artifacts do not
// exist yet; a timeout or an exec error is inconclusive and accepted.
// When the first unit is a bootstrap unit (D-124), a missing command is
// that pre-state for every unit and is accepted.
func (r *nativeRunner) probeCheckCmds(ctx context.Context, m Mission, plan Plan) error {
	if r.sandbox == nil {
		return nil
	}
	workRoot := m.WorkRoot()
	backend := func(ctx context.Context, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		return r.sandbox(ctx, m.ID, m.Environment, workdir, command, timeout, out)
	}
	bootstrapFirst := len(plan.Units) > 0 && plan.Units[0].Bootstrap
	for _, u := range plan.Units {
		if strings.TrimSpace(u.CheckCmd) == "" {
			continue
		}
		res, err := runVerifyTimed(ctx, backend, workRoot, u.CheckCmd, probeTimeout)
		if err != nil {
			r.log.Warn("mission planner: check_cmd probe failed to run", "mission_id", m.ID, "unit", u.Title, "error", err)
			continue
		}
		if res.TimedOut {
			r.log.Warn("mission planner: check_cmd probe timed out", "mission_id", m.ID, "unit", u.Title)
			continue
		}
		switch {
		case res.Passed:
			anchor := anchorExample[m.Environment]
			if anchor == "" {
				anchor = "grep -q '<symbol the unit adds>' <artifact> && <toolchain call>"
			}
			return fmt.Errorf("mission runner: unit %q check_cmd `%s` already exits 0 before any work was done, so it cannot tell done from not done. A toolchain call alone passes while the files are still missing (go test -run X matches nothing, gofmt -l prints nothing for absent files), so anchor the gate on content the unit adds, e.g. %s. A unit that adds no new content (a final format/verify unit) can have no such gate: remove it and put its commands into the code units' check_cmds", u.Title, u.CheckCmd, anchor)
		case res.ExitCode == 127 || shellNotFound.MatchString(lastLine(res.Excerpt)):
			if bootstrapFirst {
				r.log.Info("plan probe: missing command accepted, bootstrap unit precedes it", "mission_id", m.ID, "unit", u.Title, "not_found", strings.TrimSpace(lastLine(res.Excerpt)))
				continue
			}
			if installedByWork(u.CheckCmd) {
				r.log.Info("plan probe: missing command accepted, it lives in a dependency dir the work installs", "mission_id", m.ID, "unit", u.Title, "not_found", strings.TrimSpace(lastLine(res.Excerpt)))
				continue
			}
			env := m.Environment
			if env == "" {
				env = "sandbox"
			}
			return fmt.Errorf("mission runner: unit %q check_cmd `%s` uses a command the %s environment does not have: %s", u.Title, u.CheckCmd, env, strings.TrimSpace(lastLine(res.Excerpt)))
		}
	}
	return nil
}

// dependencyDirs hold binaries a dependency install puts in the
// workspace (vendor/bin/phpunit, node_modules/.bin/vitest), absent in a
// fresh clone.
var dependencyDirs = []string{"vendor/", "node_modules/", ".venv/", "bin/"}

// installedByWork reports whether cmd's first token is a
// workspace-relative path under a dependency dir (issue #1007).
func installedByWork(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 || strings.HasPrefix(fields[0], "/") {
		return false
	}
	p := path.Clean(fields[0])
	for _, d := range dependencyDirs {
		if strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

// shellNotFound matches the shell's own report of a missing command
// ("sh: 1: pytest: not found", "bash: pytest: command not found"),
// never a program's (go prints "directory not found" with exit 1).
var shellNotFound = regexp.MustCompile(`^(?:/bin/)?(?:sh|bash|dash): (?:\d+: )?[^:]+: (?:command )?not found$`)

// lastLine returns the final non-empty line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// acceptPlan is parsePlan plus the gate checks that need the mission
// (kind, environment, sandbox). PlanSession's recovery turn quotes the
// returned error to the planner, so every message names the unit and
// the fix.
func (r *nativeRunner) acceptPlan(ctx context.Context, m Mission, raw string) (Plan, error) {
	plan, err := parsePlan(raw)
	if err != nil {
		return Plan{}, err
	}
	if plan.Infeasible {
		return plan, nil
	}
	if err := checkPlanGates(plan, m); err != nil {
		return Plan{}, err
	}
	if err := r.probeCheckCmds(ctx, m, plan); err != nil {
		return Plan{}, err
	}
	if m.Kind == KindCoding && m.PlanGate.granularityWaivable() && checkUnitGranularity(plan, m) != nil {
		plan.GranularityWaived = true
	}
	return plan, nil
}
