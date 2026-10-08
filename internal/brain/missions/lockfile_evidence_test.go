package missions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestChangedLockfiles(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  []string
	}{
		{"none", []string{"src/app.php", "composer.json"}, nil},
		{"composer and npm", []string{"package-lock.json", "app/x.php", "composer.lock"}, []string{"composer.lock", "package-lock.json"}},
		{"nested and other ecosystems", []string{"web/pnpm-lock.yaml", "go.sum", "Cargo.lock", "uv.lock", "poetry.lock", "Gemfile.lock", "yarn.lock", "bun.lock"},
			[]string{"Cargo.lock", "Gemfile.lock", "bun.lock", "go.sum", "poetry.lock", "uv.lock", "web/pnpm-lock.yaml", "yarn.lock"}},
		{"name only matches the basename", []string{"docs/composer.lock.md", "go.sum.bak"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := changedLockfiles(tt.files); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("changedLockfiles = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLockfileOwners(t *testing.T) {
	units := []PlanUnit{
		{Title: "composer", Artifacts: []string{"composer.json", "composer.lock"}},
		{Title: "npm", Scope: []string{"web"}},
		{Title: "docs", Artifacts: []string{"README.md"}},
	}
	tests := []struct {
		name      string
		lockfiles []string
		current   int
		want      map[int]bool
	}{
		{"no lockfile", nil, 0, nil},
		{"artifact owner", []string{"composer.lock"}, 2, map[int]bool{0: true}},
		{"scope owner", []string{"web/package-lock.json"}, 0, map[int]bool{1: true}},
		{"both owners", []string{"composer.lock", "web/package-lock.json"}, 0, map[int]bool{0: true, 1: true}},
		{"no owner falls back to current", []string{"yarn.lock"}, 2, map[int]bool{2: true}},
		{"no owner and none current falls back to last", []string{"yarn.lock"}, -1, map[int]bool{2: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lockfileOwners(units, tt.lockfiles, tt.current); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("lockfileOwners = %v, want %v", got, tt.want)
			}
		})
	}
}

func intp(n int) *int { return &n }

func TestLockfileCriteria(t *testing.T) {
	base := TestSummary{Passed: 36, Parsed: true}
	tests := []struct {
		name       string
		e          LockfileEvidence
		security   bool
		wantTests  string
		wantAudit  string
		wantDetail string
	}{
		{"both met", LockfileEvidence{TestCmd: "php artisan test", TestsBefore: &base, TestsAfter: &TestSummary{Passed: 37, Parsed: true}, VulnsBefore: intp(3), VulnsAfter: intp(0)},
			false, lockfileMet, lockfileMet, "before: 3; after: 0"},
		{"test exit fails", LockfileEvidence{TestCmd: "php artisan test", TestExit: 1, TestsBefore: &base, TestsAfter: &TestSummary{Passed: 35, Failed: 1, Parsed: true}, VulnsBefore: intp(0), VulnsAfter: intp(0)},
			false, lockfileNotMet, lockfileMet, "exited 1"},
		{"be8a2860: 36 warnings after the upgrade", LockfileEvidence{TestCmd: "php artisan test", TestsBefore: &base, TestsAfter: &TestSummary{Passed: 36, Warnings: 36, Parsed: true}, VulnsBefore: intp(0), VulnsAfter: intp(0)},
			false, lockfileNotMet, lockfileMet, "warnings rose from 0 to 36"},
		{"fewer passing tests", LockfileEvidence{TestCmd: "npm test", TestsBefore: &base, TestsAfter: &TestSummary{Passed: 30, Parsed: true}},
			false, lockfileNotMet, lockfileNotMeasured, "passing tests fell from 36 to 30"},
		{"unparsed after summary leans on the exit code", LockfileEvidence{TestCmd: "make test", TestsBefore: &base, TestsAfter: &TestSummary{}},
			false, lockfileMet, lockfileNotMeasured, ""},
		{"no baseline test command", LockfileEvidence{VulnsBefore: intp(1), VulnsAfter: intp(1)},
			false, lockfileNotMeasured, lockfileMet, ""},
		{"advisories rose", LockfileEvidence{VulnsBefore: intp(1), VulnsAfter: intp(2), Advisories: []string{"GHSA-a", "GHSA-b"}},
			false, lockfileNotMeasured, lockfileNotMet, "advisories rose from 1 to 2; remaining: GHSA-a, GHSA-b"},
		{"audit after failed", LockfileEvidence{VulnsBefore: intp(1), AuditError: "osv-scanner exited 127"},
			false, lockfileNotMeasured, lockfileNotMet, "the audit after the change failed"},
		{"security goal names remaining advisories", LockfileEvidence{VulnsBefore: intp(2), VulnsAfter: intp(1), Advisories: []string{"GHSA-x"}},
			true, lockfileNotMeasured, lockfileMet, "remaining: GHSA-x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lockfileCriteria(&tt.e, tt.security)
			if len(got) != 2 {
				t.Fatalf("criteria = %+v, want two", got)
			}
			if got[0].Status != tt.wantTests || got[1].Status != tt.wantAudit {
				t.Fatalf("statuses = %s/%s, want %s/%s (%+v)", got[0].Status, got[1].Status, tt.wantTests, tt.wantAudit, got)
			}
			if tt.wantDetail != "" && !strings.Contains(got[0].Detail+" | "+got[1].Detail, tt.wantDetail) {
				t.Fatalf("details %q / %q lack %q", got[0].Detail, got[1].Detail, tt.wantDetail)
			}
			if strings.Contains(got[1].Text, "security goal") != tt.security {
				t.Fatalf("audit criterion text %q, security=%v", got[1].Text, tt.security)
			}
		})
	}
}

func TestSecurityGoal(t *testing.T) {
	for goal, want := range map[string]bool{
		"Fix the security advisories in composer": true,
		"Patch CVE-2025-1234":                    true,
		"Upgrade laravel to 11":                   false,
	} {
		if got := securityGoal(goal); got != want {
			t.Errorf("securityGoal(%q) = %v, want %v", goal, got, want)
		}
	}
}

func TestOSVAdvisoryIDs(t *testing.T) {
	raw := []byte(`{"results":[{"packages":[
		{"groups":[{"ids":["GHSA-b","CVE-2"]},{"ids":["GHSA-a"]}]},
		{"vulnerabilities":[{"id":"PYSEC-1"}]},
		{"groups":[{"ids":["GHSA-a"]}]}]}]}`)
	want := []string{"GHSA-a", "GHSA-b", "PYSEC-1"}
	if got := osvAdvisoryIDs(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("osvAdvisoryIDs = %v, want %v", got, want)
	}
}

// lockfileFixture is a coding mission whose worktree has a base commit
// with base files and a second commit applying change; it carries a
// prepare baseline with the given tests and audit total.
func lockfileFixture(t *testing.T, base, change map[string]string, tests *TestSummary, vulns int) Mission {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ws := t.TempDir()
	wt := filepath.Join(ws, "wt")
	if err := os.MkdirAll(wt, 0o750); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", args...) //nolint:gosec // fixed test-fixture git subcommands
		cmd.Dir = wt
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(files map[string]string) {
		for p, c := range files {
			full := filepath.Join(wt, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	git("init", "-q")
	write(base)
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	baseCommit := git("rev-parse", "HEAD")
	write(change)
	git("add", "-A")
	git("commit", "-q", "-m", "change")
	prep := &PrepareFacts{
		TestCmd: "sh run-tests.sh", TestSource: "test fixture", Tests: tests,
		Steps: []PrepareStepFact{{Name: "test", OK: true}, {Name: "audit", OK: true}},
		Audit: []AuditFact{{Path: "composer.lock", Packages: 2, Vulnerabilities: vulns}},
	}
	return Mission{
		ID: "m1", Kind: KindCoding, Goal: "upgrade the dependencies", Workspace: ws, BaseCommit: baseCommit,
		EnvFacts: &EnvFacts{Prepare: prep},
		Plan:     Plan{Units: []PlanUnit{{Title: "upgrade composer", Artifacts: []string{"composer.lock"}}}},
	}
}

// miseShellExec runs the harness command through /bin/sh with the mise
// exec prefix dropped (no mise in the test image): a real-shell round
// trip of buildTestCmd and buildAuditCmd's quoting.
func miseShellExec(ctx context.Context, missionID, environment, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
	return fakeSandboxExec(ctx, missionID, environment, workdir, strings.TrimPrefix(command, "mise exec -- "), timeout, out)
}

// fakeOSV puts an osv-scanner on PATH that copies report to its
// --output-file, logs its arguments and exits with code.
func fakeOSV(t *testing.T, report string, code int) (argsLog string) {
	t.Helper()
	dir := t.TempDir()
	reportFile := filepath.Join(dir, "report.json")
	argsLog = filepath.Join(dir, "args.log")
	if err := os.WriteFile(reportFile, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$@\" >> '" + argsLog + "'\nout=\"\"\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = --output-file ]; then out=\"$2\"; shift; fi\n  shift\ndone\ncp '" + reportFile + "' \"$out\"\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "osv-scanner"), []byte(script), 0o700); err != nil { //nolint:gosec // test-only executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsLog
}

// osvReportJSON builds a one-lockfile report with n advisory groups.
func osvReportJSON(path string, n int) string {
	var groups []string
	for i := range n {
		groups = append(groups, `{"ids":["GHSA-`+strconv.Itoa(i)+`"]}`)
	}
	return `{"results":[{"source":{"path":"` + path + `"},"packages":[{"groups":[` + strings.Join(groups, ",") + `]}]}]}`
}

const composerBefore = `{"packages":[{"name":"laravel/framework","version":"v10.1.0"}]}`
const composerAfter = `{"packages":[{"name":"laravel/framework","version":"v10.2.0"}]}`

func lockfileVerifier() (*verifier, *fakeStore) {
	store := newFakeStore()
	return &verifier{store: store, sandboxExec: miseShellExec, log: slog.Default()}, store
}

func lockfileEvents(t *testing.T, store *fakeStore) []map[string]any {
	t.Helper()
	events, _ := store.Events(context.Background(), "m1")
	var out []map[string]any
	for _, e := range events {
		if e.Kind == "mission.lockfile_evidence" {
			var p map[string]any
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			out = append(out, p)
		}
	}
	return out
}

// TestVerifyAllLockfileUpgradeBreaksTest: the upgrade breaks a test, so
// the unit owning composer.lock fails on harness evidence even though
// its artifacts exist and it has no check_cmd.
func TestVerifyAllLockfileUpgradeBreaksTest(t *testing.T) {
	m := lockfileFixture(t,
		map[string]string{"composer.lock": composerBefore, "run-tests.sh": "echo 'Tests:    36 passed (72 assertions)'\n"},
		map[string]string{"composer.lock": composerAfter, "run-tests.sh": "echo 'FAILED Tests\\ExampleTest'\necho 'Tests:    1 failed, 35 passed (72 assertions)'\nexit 1\n"},
		&TestSummary{Passed: 36, Parsed: true}, 0)
	argsLog := fakeOSV(t, osvReportJSON(filepath.Join(m.WorktreePath(), "composer.lock"), 0), 0)
	v, store := lockfileVerifier()

	got, err := v.verifyAll(context.Background(), m, nil, false)
	if err != nil {
		t.Fatalf("verifyAll: %v", err)
	}
	if len(got) != 1 || got[0].Passed || got[0].Check != lockfileCheck {
		t.Fatalf("verifyAll = %+v, want unit 0 failed on %s", got, lockfileCheck)
	}
	for _, want := range []string{"composer.lock", "exited 1", "1 failed, 35 passed", "FAILED Tests"} {
		if !strings.Contains(got[0].Excerpt, want) {
			t.Errorf("excerpt lacks %q:\n%s", want, got[0].Excerpt)
		}
	}
	state, _ := applyVerification(StepState{Units: m.Plan.Units}, got)
	if u := state.Units[0]; u.HarnessPassed || u.Passes {
		t.Fatalf("unit after applyVerification = %+v, want passes false", u)
	}
	evs := lockfileEvents(t, store)
	if len(evs) != 1 || evs[0]["met"] != false || evs[0]["test_exit"] != float64(1) {
		t.Fatalf("lockfile events = %v, want one unmet with exit 1", evs)
	}
	args, err := os.ReadFile(argsLog) //nolint:gosec // test temp file
	if err != nil || !strings.Contains(string(args), "-L composer.lock") || !strings.Contains(string(args), "osv-after.json") {
		t.Fatalf("osv-scanner args = %q (%v), want the prepare invocation over composer.lock", args, err)
	}
	stored := store.missions["m1"].EnvFacts
	if stored == nil || stored.Lockfile == nil || stored.Lockfile.Met() || stored.Prepare == nil {
		t.Fatalf("stored env facts = %+v, want prepare kept and unmet lockfile evidence", stored)
	}
}

// TestVerifyAllLockfileWarningsRegression is the be8a2860 shape: tests
// still exit 0 but the upgrade brings 36 warnings over a clean baseline.
func TestVerifyAllLockfileWarningsRegression(t *testing.T) {
	m := lockfileFixture(t,
		map[string]string{"composer.lock": composerBefore, "run-tests.sh": "echo 'Tests:    36 passed (72 assertions)'\n"},
		map[string]string{"composer.lock": composerAfter, "run-tests.sh": "echo 'Tests:    36 warnings, 36 passed (72 assertions)'\n"},
		&TestSummary{Passed: 36, Parsed: true}, 0)
	fakeOSV(t, osvReportJSON(filepath.Join(m.WorktreePath(), "composer.lock"), 0), 0)
	v, _ := lockfileVerifier()

	got, err := v.verifyAll(context.Background(), m, nil, false)
	if err != nil {
		t.Fatalf("verifyAll: %v", err)
	}
	if len(got) != 1 || got[0].Passed || !strings.Contains(got[0].Excerpt, "warnings rose from 0 to 36") {
		t.Fatalf("verifyAll = %+v, want the warnings criterion to fail the unit", got)
	}
}

// TestVerifyAllLockfileAdvisoriesRise fails the unit on a higher osv
// count with passing tests.
func TestVerifyAllLockfileAdvisoriesRise(t *testing.T) {
	m := lockfileFixture(t,
		map[string]string{"composer.lock": composerBefore, "run-tests.sh": "echo 'Tests:    36 passed'\n"},
		map[string]string{"composer.lock": composerAfter},
		&TestSummary{Passed: 36, Parsed: true}, 1)
	fakeOSV(t, osvReportJSON(filepath.Join(m.WorktreePath(), "composer.lock"), 2), osvExitAdvisories)
	v, _ := lockfileVerifier()

	got, err := v.verifyAll(context.Background(), m, nil, false)
	if err != nil {
		t.Fatalf("verifyAll: %v", err)
	}
	if len(got) != 1 || got[0].Passed || !strings.Contains(got[0].Excerpt, "advisories rose from 1 to 2") {
		t.Fatalf("verifyAll = %+v, want the advisory criterion to fail the unit", got)
	}
}

// TestVerifyAllLockfileEvidencePassesAndIsReused: met criteria leave the
// unit passed, and a second pass over the same tree reuses the stored
// evidence instead of running the tests again.
func TestVerifyAllLockfileEvidencePassesAndIsReused(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs")
	script := "echo run >> '" + counter + "'\necho 'Tests:    36 passed'\n"
	m := lockfileFixture(t,
		map[string]string{"composer.lock": composerBefore, "run-tests.sh": script},
		map[string]string{"composer.lock": composerAfter},
		&TestSummary{Passed: 36, Parsed: true}, 2)
	fakeOSV(t, osvReportJSON(filepath.Join(m.WorktreePath(), "composer.lock"), 0), 0)
	v, store := lockfileVerifier()

	got, err := v.verifyAll(context.Background(), m, nil, false)
	if err != nil || len(got) != 1 || !got[0].Passed {
		t.Fatalf("verifyAll = %+v, %v; want unit 0 passed", got, err)
	}
	m.EnvFacts = store.missions["m1"].EnvFacts
	if e := m.EnvFacts.Lockfile; e == nil || *e.VulnsBefore != 2 || *e.VulnsAfter != 0 || e.AfterTests() != "36 passed, 0 failed, 0 warnings" {
		t.Fatalf("stored evidence = %+v", e)
	}
	if _, err := v.verifyAll(context.Background(), m, nil, false); err != nil {
		t.Fatal(err)
	}
	runs, _ := os.ReadFile(counter) //nolint:gosec // test temp file
	if n := strings.Count(string(runs), "run"); n != 1 {
		t.Fatalf("test command ran %d times, want 1 (second pass reuses the evidence)", n)
	}
	if evs := lockfileEvents(t, store); len(evs) != 1 {
		t.Fatalf("lockfile events = %d, want 1", len(evs))
	}
}

// TestVerifyAllLockfileAuditOffline: with osvOffline set, the after
// audit scans the local database like prepare's before audit.
func TestVerifyAllLockfileAuditOffline(t *testing.T) {
	m := lockfileFixture(t,
		map[string]string{"composer.lock": composerBefore, "run-tests.sh": "echo 'Tests:    36 passed'\n"},
		map[string]string{"composer.lock": composerAfter},
		&TestSummary{Passed: 36, Parsed: true}, 0)
	argsLog := fakeOSV(t, osvReportJSON(filepath.Join(m.WorktreePath(), "composer.lock"), 0), 0)
	v, _ := lockfileVerifier()
	v.osvOffline = true

	if _, err := v.verifyAll(context.Background(), m, nil, false); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsLog) //nolint:gosec // test temp file
	if !strings.Contains(string(args), "--offline-vulnerabilities") {
		t.Fatalf("osv-scanner args = %q, want --offline-vulnerabilities", args)
	}
}

// TestVerifyAllNoLockfileChangeRunsNothing: a diff without a lockfile
// adds no criteria and runs no tests.
func TestVerifyAllNoLockfileChangeRunsNothing(t *testing.T) {
	m := lockfileFixture(t,
		map[string]string{"composer.lock": composerBefore, "src/a.php": "a", "run-tests.sh": "exit 1\n"},
		map[string]string{"src/a.php": "b"},
		&TestSummary{Passed: 36, Parsed: true}, 0)
	m.Plan.Units[0].Artifacts = []string{"src/a.php"}
	v, store := lockfileVerifier()

	got, err := v.verifyAll(context.Background(), m, nil, false)
	if err != nil || len(got) != 1 || !got[0].Passed {
		t.Fatalf("verifyAll = %+v, %v; want unit 0 passed", got, err)
	}
	if evs := lockfileEvents(t, store); len(evs) != 0 {
		t.Fatalf("lockfile events = %v, want none", evs)
	}
}
