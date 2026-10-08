package missions

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

// repoMission is a coding mission over a repo source whose worktree is
// a temp dir holding CHANGELOG.md.
func repoMission(t *testing.T, goal string) Mission {
	t.Helper()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "wt"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "wt", "CHANGELOG.md"), []byte("# Changelog\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Mission{
		ID: "m1", Kind: KindCoding, Goal: goal, Workspace: ws,
		Sources: []SourceEntry{{Source: SourceKindGitHub, RepoURL: "https://github.com/o/r.git", ConnectorID: "c1"}},
	}
}

// TestCheckReportArtifacts (D-134, issue #1039).
func TestCheckReportArtifacts(t *testing.T) {
	const analysisGoal = "Do security analysis for the dependencies. Based on the analysis, upgrade the dependencies and create a PR."
	cases := []struct {
		name      string
		goal      string
		kind      string
		noRepo    bool
		artifacts []string
		reject    string
	}{
		{name: "unrequested report at root", goal: analysisGoal, artifacts: []string{"SECURITY_ANALYSIS.md"}, reject: "SECURITY_ANALYSIS.md"},
		{name: "unrequested scratch text file", goal: analysisGoal, artifacts: []string{"composer.lock", "test-results.txt"}, reject: "test-results.txt"},
		{name: "unrequested nested report", goal: analysisGoal, artifacts: []string{"reports/security.md"}, reject: "reports/security.md"},
		{name: "goal names the file", goal: "Audit the dependencies and write docs/security.md with the findings.", artifacts: []string{"docs/security.md"}},
		{name: "goal names the base name in another case", goal: "Write SECURITY.md summarising the audit.", artifacts: []string{"security.md"}},
		{name: "goal names the directory", goal: "Document the new API under docs.", artifacts: []string{"docs/api.md"}},
		{name: "existing file edited", goal: analysisGoal, artifacts: []string{"composer.json", "CHANGELOG.md"}},
		{name: "non-report artifacts", goal: analysisGoal, artifacts: []string{"composer.json", "composer.lock"}},
		{name: "no repo source", goal: analysisGoal, noRepo: true, artifacts: []string{"SECURITY_ANALYSIS.md"}},
		{name: "general mission report is the deliverable", goal: analysisGoal, kind: "general", artifacts: []string{"SECURITY_ANALYSIS.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := repoMission(t, tc.goal)
			if tc.kind != "" {
				m.Kind = tc.kind
			}
			if tc.noRepo {
				m.Sources = nil
			}
			plan := Plan{Units: []PlanUnit{{Title: "u", Artifacts: tc.artifacts}}}
			err := checkReportArtifacts(plan, m)
			if tc.reject == "" {
				if err != nil {
					t.Fatalf("checkReportArtifacts = %v, want accepted", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "lists "+tc.reject+" as an artifact") || !strings.Contains(err.Error(), "final_output") {
				t.Fatalf("checkReportArtifacts = %v, want a rejection naming %s and final_output", err, tc.reject)
			}
		})
	}
}

// TestCheckUntrackedAssumptions (D-134, issue #1039).
func TestCheckUntrackedAssumptions(t *testing.T) {
	cases := []struct {
		name       string
		assumption PlanAssumption
		unit       PlanUnit
		where      string
	}{
		{
			name:       "artifact",
			assumption: PlanAssumption{Assumption: "lockfile handling", Default: "package-lock.json stays untracked"},
			unit:       PlanUnit{Title: "npm", Artifacts: []string{"package.json", "package-lock.json"}},
			where:      "artifacts",
		},
		{
			name:       "scope",
			assumption: PlanAssumption{Assumption: "package-lock.json is not committed", Default: "generate it locally"},
			unit:       PlanUnit{Title: "npm", Artifacts: []string{"package.json"}, Scope: []string{"package.json", "./package-lock.json"}},
			where:      "scope",
		},
		{
			name:       "check_cmd",
			assumption: PlanAssumption{Assumption: "npm lockfile", Default: "keep package-lock.json uncommitted"},
			unit:       PlanUnit{Title: "npm", Artifacts: []string{"package.json"}, CheckCmd: "test -f package-lock.json && npm audit --audit-level=high"},
			where:      "check_cmd",
		},
		{
			name:       "nested path",
			assumption: PlanAssumption{Assumption: "build output", Default: "public/build/manifest.json is never committed"},
			unit:       PlanUnit{Title: "assets", Artifacts: []string{"vite.config.js"}, CheckCmd: "grep -q x public/build/manifest.json"},
			where:      "check_cmd",
		},
		{
			name:       "claim names a file no unit uses",
			assumption: PlanAssumption{Assumption: "yarn.lock stays untracked", Default: "leave it"},
			unit:       PlanUnit{Title: "npm", Artifacts: []string{"package.json"}, CheckCmd: "npm audit"},
		},
		{
			name:       "no untracked claim",
			assumption: PlanAssumption{Assumption: "lockfile", Default: "package-lock.json is committed with the PR"},
			unit:       PlanUnit{Title: "npm", Artifacts: []string{"package-lock.json"}},
		},
		{
			name:       "version number is not a path",
			assumption: PlanAssumption{Assumption: "target Laravel 11.0", Default: "the cache stays untracked"},
			unit:       PlanUnit{Title: "upgrade", Artifacts: []string{"composer.json"}, CheckCmd: "grep -q 11.0 composer.json"},
		},
		{
			name:       "pathless lockfile claim, untracked lockfile in scope",
			assumption: m048LockAssumption,
			unit:       PlanUnit{Title: "npm", Artifacts: []string{"package.json"}, Scope: []string{"package.json", "package-lock.json"}},
			where:      "scope",
		},
		{
			name:       "pathless lockfile claim, nested lockfile in check_cmd",
			assumption: PlanAssumption{Assumption: "lock files", Default: "never committed"},
			unit:       PlanUnit{Title: "web", Artifacts: []string{"web/package.json"}, CheckCmd: "cd web && test -f yarn.lock || test -s web/yarn.lock"},
			where:      "check_cmd",
		},
		{
			name:       "pathless lockfile claim, tracked lockfile allowed",
			assumption: m048LockAssumption,
			unit:       PlanUnit{Title: "composer", Artifacts: []string{"composer.json", "composer.lock"}},
		},
		{
			name:       "path in another clause is not the claim subject",
			assumption: m048LockAssumption,
			unit:       PlanUnit{Title: "npm", Artifacts: []string{"package.json"}, CheckCmd: "grep -q vite package.json"},
		},
		{
			name:       "file name inside a longer word",
			assumption: PlanAssumption{Assumption: "lock.json stays untracked", Default: "skip"},
			unit:       PlanUnit{Title: "npm", Artifacts: []string{"package-lock.json"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := Plan{Units: []PlanUnit{tc.unit}, Assumptions: []PlanAssumption{tc.assumption}}
			err := checkUntrackedAssumptions(plan, func(p string) bool { return p == "composer.lock" })
			if tc.where == "" {
				if err != nil {
					t.Fatalf("checkUntrackedAssumptions = %v, want accepted", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "in its "+tc.where) {
				t.Fatalf("checkUntrackedAssumptions = %v, want a rejection via %s", err, tc.where)
			}
		})
	}
}

// The 048d389e units are the plan shape production mission 048d389e
// (Laravel dependency upgrade) ran with (issue #1039): a report unit
// whose artifact the goal never named, and an npm unit that scopes and
// checks a lockfile the assumptions say stays untracked.
const (
	m048Composer = `{"title":"Upgrade composer dependencies","artifacts":["composer.json","composer.lock"],"criteria":["advisories fixed","lock matches manifest"],"check_cmd":"composer audit --locked"}`
	m048NPM      = `{"title":"Upgrade npm dependencies","artifacts":["package.json"],"scope":["package.json","package-lock.json"],"criteria":["no high advisories","build still works"],"check_cmd":"test -f package-lock.json && npm audit --audit-level=high"}`
	m048NPMFixed = `{"title":"Upgrade npm dependencies","artifacts":["package.json"],"criteria":["no high advisories","build still works"],"check_cmd":"grep -q '\"vite\": \"^6' package.json"}`
	m048Report   = `{"title":"Write security analysis report","artifacts":["SECURITY_ANALYSIS.md"],"criteria":["lists advisories","names fixes"],"check_cmd":"grep -qi 'CVE' SECURITY_ANALYSIS.md"}`
	m048Assume   = `[{"assumption":"Report format and location not specified","default":"Markdown report at repo root, SECURITY_ANALYSIS.md, committed with the PR"},{"assumption":"npm lockfile is not committed in this repo","default":"Lockfile is regenerated locally only to run npm audit / osv-scanner and stays untracked; the durable fix must land in package.json"}]`
)

// m048LockAssumption is 048d389e's lockfile assumption, verbatim.
var m048LockAssumption = PlanAssumption{
	Assumption: "npm lockfile is not committed in this repo",
	Default:    "Lockfile is regenerated locally only to run npm audit / osv-scanner and stays untracked; the durable fix must land in package.json",
}

// gitTrack makes dir a git repository with files committed.
func gitTrack(t *testing.T, dir string, files ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, append([]string{"add", "--"}, files...), {"commit", "-q", "-m", "base"}} {
		cmd := exec.Command("git", args...) //nolint:gosec // fixed test-fixture git subcommands
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestPlan048d389eShape is the regression replay: the production plan
// is rejected for the lockfile contradiction and, once that is fixed,
// for the report file; the plan with both fixed is accepted.
func TestPlan048d389eShape(t *testing.T) {
	m := repoMission(t, "Do security analysis for the dependencies of this Laravel app. Based on the analysis, upgrade the dependencies and create a PR.")
	m.ID = "048d389e"
	m.PlanGate = PlanGateState{RepoDestination: true}
	gitTrack(t, m.WorkRoot(), "composer.json", "composer.lock", "package.json")
	r := &nativeRunner{log: slog.Default(), sandbox: func(_ context.Context, _, _, _ string, _ time.Duration, out io.Writer) (int, error) {
		_, _ = fmt.Fprint(out, "not done yet\n")
		return 1, nil
	}}
	plan := func(units ...string) string {
		return `{"units":[` + strings.Join(units, ",") + `],"assumptions":` + m048Assume + `}`
	}

	_, err := r.acceptPlan(context.Background(), m, plan(m048Composer, m048NPM, m048Report))
	if err == nil || !strings.Contains(err.Error(), `says the lockfile stays untracked, but unit "Upgrade npm dependencies" uses package-lock.json in its scope`) {
		t.Fatalf("acceptPlan(production plan) = %v, want the untracked-lockfile rejection", err)
	}
	_, err = r.acceptPlan(context.Background(), m, plan(m048Composer, m048NPMFixed, m048Report))
	if err == nil || !strings.Contains(err.Error(), `unit "Write security analysis report" lists SECURITY_ANALYSIS.md as an artifact`) {
		t.Fatalf("acceptPlan(lockfile fixed) = %v, want the report-file rejection", err)
	}
	if _, err := r.acceptPlan(context.Background(), m, plan(m048Composer, m048NPMFixed)); err != nil {
		t.Fatalf("acceptPlan(both fixed) = %v, want accepted", err)
	}
}

// TestDriverPlannedDoneKeepsReport (D-134): a planned mission's worker
// report in final_output is stored as the mission's FinalOutput; a
// later done with none keeps it.
func TestDriverPlannedDoneKeepsReport(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseBuild, Status: StatusWorking, MaxIterations: 8,
		Plan: Plan{Units: []PlanUnit{{Title: "a"}, {Title: "b"}}},
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{
		{Outcome: "done", Evidence: "did a", FinalOutput: "## Analysis\nthe report"},
		{Outcome: "done", Evidence: "did b"},
	}}
	d := testDriver(store, runner)
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.FinalOutput != "## Analysis\nthe report" {
		t.Fatalf("FinalOutput = %q, want the worker's report", m.FinalOutput)
	}

	m.Phase, m.Status = PhaseBuild, StatusWorking
	store.put("m1", m)
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ = store.Get(context.Background(), "m1")
	if m.FinalOutput != "## Analysis\nthe report" {
		t.Fatalf("FinalOutput = %q, want the earlier report kept", m.FinalOutput)
	}
}

// TestDelegatedRunWorkerCarriesFinalOutput (D-134): a delegated
// worker's structured final_output reaches the verdict, which the
// driver stores like a native one.
func TestDelegatedRunWorkerCarriesFinalOutput(t *testing.T) {
	lines := loadDelegatedFixture(t, "schema.ndjson")
	for i, l := range lines {
		lines[i] = bytes.Replace(l, []byte(`"structured_output":{"status":"DONE","note":"Ready for task input."}`),
			[]byte(`"structured_output":{"status":"DONE","note":"upgraded","final_output":"## Analysis\nCVE-2025-1 fixed"}`), 1)
	}
	sandbox := newFakeSandbox()
	sandbox.seedLines = lines
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}
	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, &fakeEventSink{}, nil, &fakeLedger{})

	verdict, _, err := r.RunWorker(testCtx(t), testMission("m1", t.TempDir()), WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Outcome != "done" || verdict.FinalOutput != "## Analysis\nCVE-2025-1 fixed" {
		t.Fatalf("verdict = %+v, want done with the report in FinalOutput", verdict)
	}
}
