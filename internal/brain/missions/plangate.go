package missions

// Plan gate checks (issue #718): run at plan acceptance, before any
// worker turn, and reject a plan whose check_cmd cannot exit 0, always
// exits 0, or never exercises the code it guards. The planner's
// recovery turn sees the exact defect.

import (
	"context"
	"fmt"
	"io"
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
	if err := checkOwnArtifacts(plan); err != nil {
		return err
	}
	if m.Kind != KindCoding {
		return nil
	}
	if err := checkUnitGranularity(plan, m); err != nil {
		return err
	}
	return checkCodeFloor(plan, m.Environment)
}

// smallPlanArtifactCap is the plan size below which a single-directory
// coding plan is one unit: each extra unit costs a CLI session
// bootstrap and a reviewed evidence set (issue #720).
const smallPlanArtifactCap = 8

// checkUnitGranularity rejects a coding plan that splits a small
// single-directory change into several units. Every extra unit pays
// for another session bootstrap and another evidence set, and the files
// share a package, so the work is one unit. Applies in transcribe mode
// too (D-102): an operator's file list is not a unit split.
func checkUnitGranularity(plan Plan, m Mission) error {
	if len(plan.Units) < 2 {
		return nil
	}
	dir := ""
	count := 0
	titles := make([]string, 0, len(plan.Units))
	for _, u := range plan.Units {
		for _, a := range u.Artifacts {
			count++
			d := path.Dir(cleanArtifact(a))
			if dir == "" {
				dir = d
			} else if d != dir {
				return nil
			}
		}
		titles = append(titles, strconv.Quote(u.Title))
	}
	if count == 0 || count > smallPlanArtifactCap {
		return nil
	}
	if dir == "." {
		dir = "the workspace root"
	}
	return fmt.Errorf("mission runner: units %s all produce files in %s and the plan has only %d artifacts, so they are one unit of work; merge them into a single unit with the combined artifacts, criteria and one check_cmd covering all of them", strings.Join(titles, ", "), dir, count)
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
func (r *nativeRunner) probeCheckCmds(ctx context.Context, m Mission, plan Plan) error {
	if r.sandbox == nil {
		return nil
	}
	workRoot := m.WorkRoot()
	backend := func(ctx context.Context, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		return r.sandbox(ctx, m.ID, m.Environment, workdir, command, timeout, out)
	}
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
			env := m.Environment
			if env == "" {
				env = "sandbox"
			}
			return fmt.Errorf("mission runner: unit %q check_cmd `%s` uses a command the %s environment does not have: %s", u.Title, u.CheckCmd, env, strings.TrimSpace(lastLine(res.Excerpt)))
		}
	}
	return nil
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
	return plan, nil
}
