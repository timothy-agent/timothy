package missions

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// TestEmptyOutputIdiom pins the regex on the spellings the planner
// actually produced and on look-alikes it must leave alone.
func TestEmptyOutputIdiom(t *testing.T) {
	match := []string{
		`gofmt -l a.go | grep -q '^$'`,
		`gofmt -l . | grep -q "^$" && go vet ./...`,
		`gofmt -l a.go|grep -q ^$`,
		`gofmt -l a.go | grep -qx '^$'`,
		`GOTOOLCHAIN=auto gofmt -l a.go b.go | grep -q '^$' && GOTOOLCHAIN=auto go test ./...`,
	}
	for _, c := range match {
		if !emptyOutputIdiom.MatchString(c) {
			t.Errorf("emptyOutputIdiom missed %q", c)
		}
	}
	noMatch := []string{
		`grep -q '^$ok' out.md`,
		`grep -q 'x' out.md`,
		`gofmt -l a.go | awk 'END{exit NR>0}'`,
		`grep -c '^$' out.md`,
	}
	for _, c := range noMatch {
		if emptyOutputIdiom.MatchString(c) {
			t.Errorf("emptyOutputIdiom false positive on %q", c)
		}
	}
}

// TestCheckCodeFloor covers the toolchain requirement per language
// (D-139): a unit that produces source must build, test or run it with
// that language's toolchain; document units stay permissive.
func TestCheckCodeFloor(t *testing.T) {
	const grepOnly = "never builds, tests or runs"
	cases := []struct {
		name      string
		artifacts []string
		cmd       string
		wantErr   string
	}{
		{"go grep only", []string{"internal/core/a.go"}, `grep -q 'func Foo' internal/core/a.go`, grepOnly},
		{"go test first", []string{"internal/core/a.go"}, `go test ./internal/core/... && grep -q Foo internal/core/a.go`, ""},
		{"go vet counts", []string{"a.go"}, `GOTOOLCHAIN=auto go vet ./... && grep -q x a.go`, ""},
		{"go doc unit", []string{"docs/DESIGN.md"}, `grep -q '## Scheme' docs/DESIGN.md`, ""},
		{"python grep only", []string{"app/a.py"}, `grep -q 'def main' app/a.py`, grepOnly},
		{"python pytest", []string{"app/a.py"}, `python3 -m pytest tests/ -q`, ""},
		{"python bare pytest", []string{"a.py"}, `pytest tests`, ""},
		{"node vitest", []string{"src/a.ts"}, `npx vitest run src`, ""},
		{"node grep only", []string{"src/a.ts"}, `grep -q export src/a.ts`, grepOnly},
		{"go unit with node toolchain rejected", []string{"a.go"}, `npm test`, "go test ./<package>/..."},
		{"php grep only", []string{"app/Models/User.php"}, `grep -q 'class User' app/Models/User.php`, "vendor/bin/phpunit"},
		{"php phpunit", []string{"app/Models/User.php"}, `vendor/bin/phpunit --filter UserTest`, ""},
		{"php pest", []string{"tests/Feature/UserTest.php"}, `./vendor/bin/pest tests/Feature/UserTest.php`, ""},
		{"php artisan test", []string{"app/a.php"}, `grep -q x app/a.php && php artisan test --filter=A`, ""},
		{"php artisan test via bootstrap path", []string{"app/a.php"}, `./.tools/php/bin/php artisan test`, ""},
		{"php composer test", []string{"src/A.php"}, `composer test`, ""},
		{"php composer run test", []string{"src/A.php"}, `composer run-script test`, ""},
		{"php artisan migrate is not a test", []string{"app/a.php"}, `php artisan migrate`, grepOnly},
		{"java grep only", []string{"src/main/java/App.java"}, `grep -q 'class App' src/main/java/App.java`, "./mvnw test"},
		{"java mvn", []string{"src/main/java/App.java"}, `mvn -q test`, ""},
		{"java mvnw", []string{"src/main/java/App.java"}, `./mvnw -q test -Dtest=AppTest`, ""},
		{"kotlin gradlew", []string{"src/main/kotlin/App.kt"}, `./gradlew test`, ""},
		{"kotlin grep only", []string{"src/main/kotlin/App.kt"}, `grep -q fun src/main/kotlin/App.kt`, grepOnly},
		{"ruby grep only", []string{"lib/a.rb"}, `grep -q 'def a' lib/a.rb`, "bundle exec rspec"},
		{"ruby rspec", []string{"lib/a.rb"}, `bundle exec rspec spec/a_spec.rb`, ""},
		{"ruby bin rspec", []string{"lib/a.rb"}, `bin/rspec`, ""},
		{"ruby rake test", []string{"lib/a.rb"}, `rake test`, ""},
		{"rust grep only", []string{"src/lib.rs"}, `grep -q 'fn a' src/lib.rs`, "cargo test"},
		{"rust cargo test", []string{"src/lib.rs"}, `cargo test a`, ""},
		{"rust cargo toolchain override", []string{"src/lib.rs"}, `cargo +nightly test`, ""},
		{"polyglot unit accepts either toolchain", []string{"app/a.php", "resources/js/app.ts"}, `npm test`, ""},
		{"polyglot unit names the first language", []string{"app/a.php", "resources/js/app.ts"}, `grep -q x app/a.php`, "vendor/bin/phpunit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := Plan{Units: []PlanUnit{{Title: "u", Artifacts: tc.artifacts, CheckCmd: tc.cmd}}}
			err := checkCodeFloor(plan)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkCodeFloor: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("checkCodeFloor = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestCheckPlanGatesGeneralKindSkipsFloor: a general mission's units
// are documents; the toolchain floor is a coding rule only. The empty
// output idiom is rejected for every kind.
func TestCheckPlanGatesGeneralKindSkipsFloor(t *testing.T) {
	plan := Plan{Units: []PlanUnit{{Title: "u", Artifacts: []string{"a.go"}, CheckCmd: `grep -q x a.go`}}}
	if err := checkPlanGates(plan, Mission{Kind: KindGeneral}); err != nil {
		t.Fatalf("general mission hit the code floor: %v", err)
	}
	plan.Units[0].CheckCmd = `ls | grep -q '^$'`
	if err := checkPlanGates(plan, Mission{Kind: KindGeneral}); err == nil {
		t.Fatal("empty-output idiom accepted on a general mission")
	}
}

// scriptedSandbox is a sandboxExec whose exit code is chosen by the
// command text, so a probe test can stage each outcome.
func scriptedSandbox(codes map[string]int, output string) sandboxExec {
	return func(_ context.Context, _, _, command string, _ time.Duration, out io.Writer) (int, error) {
		_, _ = fmt.Fprint(out, output)
		return codes[command], nil
	}
}

// TestProbeCheckCmds covers the three probe outcomes: a gate that
// already passes is rejected, a missing command is rejected, an
// ordinary failure (artifacts not there yet) is the expected state.
func TestProbeCheckCmds(t *testing.T) {
	m := Mission{ID: "m1", Kind: KindCoding}
	mk := func(cmd string) Plan {
		return Plan{Units: []PlanUnit{{Title: "u", Artifacts: []string{"a.go"}, CheckCmd: cmd}}}
	}
	t.Run("already passes", func(t *testing.T) {
		r := &nativeRunner{log: slog.Default(), sandbox: scriptedSandbox(map[string]int{"go test ./...": 0}, "ok")}
		err := r.probeCheckCmds(context.Background(), m, mk("go test ./..."))
		if err == nil || !strings.Contains(err.Error(), "already exits 0") {
			t.Fatalf("probe = %v, want tautology rejection", err)
		}
		if !strings.Contains(err.Error(), "`go test ./...`") || !strings.Contains(err.Error(), "func TestXxx") {
			t.Fatalf("rejection must quote the gate and give the go anchor recipe, got %v", err)
		}
	})
	t.Run("anchor recipe follows the artifact language", func(t *testing.T) {
		cmd := "vendor/bin/phpunit"
		r := &nativeRunner{log: slog.Default(), sandbox: scriptedSandbox(map[string]int{cmd: 0}, "OK")}
		plan := Plan{Units: []PlanUnit{{Title: "u", Artifacts: []string{"tests/Feature/ATest.php"}, CheckCmd: cmd}}}
		err := r.probeCheckCmds(context.Background(), m, plan)
		if err == nil || !strings.Contains(err.Error(), "function test_xxx") {
			t.Fatalf("probe = %v, want the php anchor recipe", err)
		}
	})
	t.Run("missing command", func(t *testing.T) {
		r := &nativeRunner{log: slog.Default(), sandbox: scriptedSandbox(map[string]int{"pytest tests": 127}, "sh: pytest: not found\n")}
		err := r.probeCheckCmds(context.Background(), m, mk("pytest tests"))
		if err == nil || !strings.Contains(err.Error(), "does not have") || !strings.Contains(err.Error(), "pytest: not found") {
			t.Fatalf("probe = %v, want missing-command rejection quoting the shell", err)
		}
	})
	t.Run("program not found is not a missing command", func(t *testing.T) {
		r := &nativeRunner{log: slog.Default(), sandbox: scriptedSandbox(map[string]int{"go test ./internal/core/ -run TestX": 1}, "stat /src/internal/core: directory not found\nFAIL\n")}
		if err := r.probeCheckCmds(context.Background(), m, mk("go test ./internal/core/ -run TestX")); err != nil {
			t.Fatalf("go's own not-found on an absent package must be the expected pre-work failure: %v", err)
		}
	})
	t.Run("expected failure accepted", func(t *testing.T) {
		r := &nativeRunner{log: slog.Default(), sandbox: scriptedSandbox(map[string]int{"go test ./...": 1}, "no Go files")}
		if err := r.probeCheckCmds(context.Background(), m, mk("go test ./...")); err != nil {
			t.Fatalf("probe rejected an ordinary pre-work failure: %v", err)
		}
	})
	t.Run("no sandbox skips", func(t *testing.T) {
		r := &nativeRunner{log: slog.Default()}
		if err := r.probeCheckCmds(context.Background(), m, mk("go test ./...")); err != nil {
			t.Fatalf("probe without a sandbox must be a no-op: %v", err)
		}
	})
}

// TestPlanSessionRejectsEmptyOutputGate replays the exact gate that
// killed four of five eval arms (issue #718): the first plan is
// rejected with a message naming the idiom and the fix, the recovery
// turn's corrected plan is accepted.
func TestPlanSessionRejectsEmptyOutputGate(t *testing.T) {
	bad := `{"units":[{"title":"Format and verify","artifacts":["internal/core/a.go"],"criteria":["c1","c2"],"check_cmd":"GOTOOLCHAIN=auto gofmt -l internal/core/a.go | grep -q '^$' && go vet ./... && go test ./internal/core/..."}]}`
	good := `{"units":[{"title":"Format and verify","artifacts":["internal/core/a.go"],"criteria":["c1","c2"],"check_cmd":"gofmt -l internal/core/a.go | awk 'END{exit NR>0}' && go test ./internal/core/..."}]}`
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, bad)},
		{toolEndEvent(planToolName, good)},
	}}
	r := newTestRunner(agent)
	plan, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Kind: KindCoding}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if agent.call != 2 {
		t.Fatalf("expected a recovery turn, got %d turns", agent.call)
	}
	if got := plan.Units[0].CheckCmd; !strings.Contains(got, "awk") {
		t.Fatalf("accepted plan check_cmd = %q, want the corrected gate", got)
	}
	req := agent.requests[1]
	last := req.Messages[len(req.Messages)-1].Content
	if !strings.Contains(last, "grep -q '^$'") || !strings.Contains(last, emptyOutputHint) {
		t.Fatalf("recovery message must name the idiom and the fix, got %q", last)
	}
}

// TestPlanSessionRejectsGrepOnlyCodeGate: a coding unit gated by greps
// alone is sent back for a toolchain call.
func TestPlanSessionRejectsGrepOnlyCodeGate(t *testing.T) {
	bad := `{"units":[{"title":"Implement Base62","artifacts":["internal/core/base62.go"],"criteria":["c1","c2"],"check_cmd":"grep -q 'package core' internal/core/base62.go && grep -q 'func .*Encode' internal/core/base62.go"}]}`
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, bad)},
		{toolEndEvent(planToolName, bad)},
	}}
	r := newTestRunner(agent)
	_, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Kind: KindCoding}, "")
	if err == nil || !strings.Contains(err.Error(), "go test") {
		t.Fatalf("PlanSession = %v, want a rejection pointing at go test", err)
	}
}

// TestCheckOwnArtifacts (issue #718): a unit whose every artifact is
// produced by an earlier unit is the trailing "format and verify" unit
// four eval arms died on; a unit that adds at least one file of its own
// passes even when it also lists an earlier unit's file.
func TestCheckOwnArtifacts(t *testing.T) {
	bad := Plan{Units: []PlanUnit{
		{Title: "base62", Artifacts: []string{"internal/core/base62.go", "internal/core/base62_test.go"}},
		{Title: "permute", Artifacts: []string{"internal/core/permute.go"}},
		{Title: "Verify and finalize", Artifacts: []string{"internal/core/base62.go", "./internal/core/permute.go"}},
	}}
	err := checkOwnArtifacts(bad)
	if err == nil || !strings.Contains(err.Error(), `"Verify and finalize"`) || !strings.Contains(err.Error(), "no artifact of its own") {
		t.Fatalf("checkOwnArtifacts = %v, want the trailing unit rejected", err)
	}
	good := Plan{Units: []PlanUnit{
		{Title: "base62", Artifacts: []string{"internal/core/base62.go"}},
		{Title: "wire base62 into the encoder", Artifacts: []string{"internal/core/base62.go", "internal/core/encoder.go"}},
	}}
	if err := checkOwnArtifacts(good); err != nil {
		t.Fatalf("checkOwnArtifacts rejected a unit that adds encoder.go: %v", err)
	}
}

// TestCheckUnitGranularity pins the coarse-unit gate (issue #720): a
// designed coding plan that splits a small single-directory change is
// rejected, in transcribe mode too; multi-package and larger plans are not.
func TestCheckUnitGranularity(t *testing.T) {
	fourUnitsOneDir := []PlanUnit{
		{Title: "base62", Artifacts: []string{"internal/core/base62.go", "internal/core/base62_test.go"}},
		{Title: "store", Artifacts: []string{"internal/core/store.go", "internal/core/store_test.go"}},
		{Title: "shorten", Artifacts: []string{"internal/core/shorten.go", "internal/core/shorten_test.go"}},
		{Title: "resolve", Artifacts: []string{"internal/core/resolve.go", "internal/core/resolve_test.go"}},
	}
	cases := []struct {
		name    string
		kind    string
		hasPlan bool
		units   []PlanUnit
		wantErr string
	}{
		{"eval slice shape rejected", KindCoding, false, fourUnitsOneDir, "they are one unit of work"},
		{"two packages accepted", KindCoding, false, []PlanUnit{
			{Title: "core", Artifacts: []string{"internal/core/a.go"}},
			{Title: "api", Artifacts: []string{"internal/api/b.go"}},
		}, ""},
		{"transcribe mode merged too", KindCoding, true, fourUnitsOneDir, "they are one unit of work"},
		{"general kind not applied", KindGeneral, false, fourUnitsOneDir, ""},
		{"nine artifacts in one dir accepted", KindCoding, false, []PlanUnit{
			{Title: "a", Artifacts: []string{"pkg/a1.go", "pkg/a2.go", "pkg/a3.go", "pkg/a4.go", "pkg/a5.go"}},
			{Title: "b", Artifacts: []string{"pkg/b1.go", "pkg/b2.go", "pkg/b3.go", "pkg/b4.go"}},
		}, ""},
		{"single unit accepted", KindCoding, false, []PlanUnit{
			{Title: "only", Artifacts: []string{"internal/core/a.go", "internal/core/b.go"}},
		}, ""},
		{"bootstrap unit never merged", KindCoding, false, []PlanUnit{
			{Title: "install php", Bootstrap: true, EvidenceOnly: true, CheckCmd: ".tools/bin/php -v"},
			{Title: "upgrade", Artifacts: []string{"composer.json", "composer.lock"}},
		}, ""},
		{"bootstrap excluded, rest still merged", KindCoding, false, []PlanUnit{
			{Title: "install php", Bootstrap: true, EvidenceOnly: true, CheckCmd: ".tools/bin/php -v"},
			{Title: "a", Artifacts: []string{"pkg/a.go"}},
			{Title: "b", Artifacts: []string{"pkg/b.go"}},
		}, "they are one unit of work"},
		{"root manifests plus report accepted", KindCoding, false, []PlanUnit{
			{Title: "upgrade", Artifacts: []string{"composer.json", "composer.lock"}},
			{Title: "report", Artifacts: []string{"SECURITY_REPORT.md"}},
		}, ""},
		{"evidence-only plus artifact unit accepted", KindCoding, false, []PlanUnit{
			{Title: "code", Artifacts: []string{"pkg/a.go"}},
			{Title: "file issue", EvidenceOnly: true, CheckCmd: "gh issue list | awk 'END{exit NR<1}'"},
		}, ""},
		{"one code unit plus report in the same dir accepted", KindCoding, false, []PlanUnit{
			{Title: "code", Artifacts: []string{"pkg/a.go", "pkg/a_test.go"}},
			{Title: "notes", Artifacts: []string{"pkg/NOTES.md"}},
		}, ""},
		{"code-only same-package split still rejected", KindCoding, false, []PlanUnit{
			{Title: "a", Artifacts: []string{"pkg/a.go", "pkg/README.md"}},
			{Title: "report", Artifacts: []string{"REPORT.md"}},
			{Title: "b", Artifacts: []string{"pkg/b.go"}},
		}, `units "a", "b" all produce code files in pkg and the plan has only 2 of them`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkUnitGranularity(Plan{Units: tc.units}, Mission{Kind: tc.kind, HasPlan: tc.hasPlan})
			if tc.kind != KindCoding {
				// checkPlanGates is where the kind gate lives.
				err = checkPlanGates(Plan{Units: tc.units}, Mission{Kind: tc.kind})
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkUnitGranularity: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("checkUnitGranularity = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestGranularityWaiver (issue #1007): a split plan is waived once a
// granularity rejection is on record, and never after the waiver is
// spent.
func TestGranularityWaiver(t *testing.T) {
	split := Plan{Units: []PlanUnit{
		{Title: "a", Artifacts: []string{"pkg/a.go"}, CheckCmd: "go test ./pkg/"},
		{Title: "b", Artifacts: []string{"pkg/b.go"}, CheckCmd: "go test ./pkg/"},
	}}
	cases := []struct {
		name       string
		gate       PlanGateState
		wantErr    bool
		wantWaived bool
	}{
		{"first submit rejected", PlanGateState{}, true, false},
		{"second submit waived", PlanGateState{GranularityRejected: true}, false, true},
		{"waiver already spent", PlanGateState{GranularityRejected: true, GranularityWaived: true}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Mission{Kind: KindCoding, PlanGate: tc.gate}
			err := checkPlanGates(split, m)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkPlanGates = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			r := &nativeRunner{log: slog.Default()}
			raw := `{"units":[{"title":"a","artifacts":["pkg/a.go"],"criteria":["c1","c2"],"check_cmd":"go test ./pkg/"},{"title":"b","artifacts":["pkg/b.go"],"criteria":["c1","c2"],"check_cmd":"go test ./pkg/"}]}`
			plan, err := r.acceptPlan(context.Background(), m, raw)
			if err != nil {
				t.Fatalf("acceptPlan: %v", err)
			}
			if plan.GranularityWaived != tc.wantWaived {
				t.Fatalf("GranularityWaived = %v, want %v", plan.GranularityWaived, tc.wantWaived)
			}
		})
	}
}

// TestPushesOrOpensPR pins the delivery pattern on the spellings a
// planner uses and on look-alikes it must leave alone.
func TestPushesOrOpensPR(t *testing.T) {
	cases := []struct {
		title, cmd string
		want       bool
	}{
		{"Push branch and open pull request", "", true},
		{"Open a PR", "", true},
		{"Create the PR for review", "", true},
		{"Deliver", "gh pr list --head x --json number --jq 'length' | awk '$1>=1'", true},
		{"Deliver", "git push origin HEAD", true},
		{"Push the mission branch", "", true},
		{"Upgrade dependencies", "grep -q laravel composer.json", false},
		{"Add PR template parser", "go test ./internal/pr/", false},
		{"Fix push notification retry", "go test ./push/", false},
		{"Write prompt docs", "grep -q x docs/prompt.md", false},
		{"Address pull request review comments", "go test ./internal/api/", false},
		{"Document the pull request template", "grep -q Summary .github/pull_request_template.md", false},
		{"Submit a pull request", "", true},
	}
	for _, tc := range cases {
		if got := pushesOrOpensPR(tc.title, tc.cmd); got != tc.want {
			t.Errorf("pushesOrOpensPR(%q, %q) = %v, want %v", tc.title, tc.cmd, got, tc.want)
		}
	}
}

// TestCheckHarnessDelivery (issue #1007): a push/PR unit is rejected
// only when a repo destination delivers the branch.
func TestCheckHarnessDelivery(t *testing.T) {
	plan := Plan{Units: []PlanUnit{
		{Title: "code", Artifacts: []string{"pkg/a.go"}, CheckCmd: "go test ./pkg/"},
		{Title: "Open pull request", EvidenceOnly: true, CheckCmd: "gh pr view | awk 'END{exit NR<1}'"},
	}}
	err := checkPlanGates(plan, Mission{Kind: KindCoding, PlanGate: PlanGateState{RepoDestination: true}})
	if err == nil || !strings.Contains(err.Error(), `"Open pull request"`) || !strings.Contains(err.Error(), "the harness pushes the mission branch and opens the PR itself after all units pass") {
		t.Fatalf("checkPlanGates with destination = %v, want the PR unit rejected", err)
	}
	if err := checkPlanGates(plan, Mission{Kind: KindCoding}); err != nil {
		t.Fatalf("checkPlanGates without destination: %v", err)
	}
}

// TestInstalledByWork covers the dependency-dir first-token rule.
func TestInstalledByWork(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"vendor/bin/phpunit --filter X", true},
		{"./vendor/bin/phpunit", true},
		{"node_modules/.bin/vitest run", true},
		{".venv/bin/pytest -q", true},
		{"bin/console lint", true},
		{"phpunit", false},
		{"/vendor/bin/phpunit", false},
		{"../vendor/bin/phpunit", false},
		{"grep -q x a && vendor/bin/pint", false},
		{"vendors/bin/phpunit", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := installedByWork(tc.cmd); got != tc.want {
			t.Errorf("installedByWork(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

// TestProbeAcceptsDependencyBinary (issue #1007): a gate naming
// vendor/bin/phpunit in a fresh clone exits 127; that is the pre-work
// state, not a missing tool.
func TestProbeAcceptsDependencyBinary(t *testing.T) {
	m := Mission{ID: "m1", Kind: KindCoding}
	cmd := "vendor/bin/phpunit --filter UpgradeTest"
	r := &nativeRunner{log: slog.Default(), sandbox: scriptedSandbox(map[string]int{cmd: 127}, "sh: 1: vendor/bin/phpunit: not found\n")}
	plan := Plan{Units: []PlanUnit{{Title: "u", Artifacts: []string{"composer.lock"}, CheckCmd: cmd}}}
	if err := r.probeCheckCmds(context.Background(), m, plan); err != nil {
		t.Fatalf("probe rejected a dependency-dir binary: %v", err)
	}
}

// The be8a2860 units are the plan shape that paused homelab mission
// be8a2860 (issue #1007): four units at the workspace root, the last
// one pushing and opening the PR.
const be8a2860Upgrade = `{"title":"Upgrade Laravel dependencies","artifacts":["composer.json","composer.lock"],"criteria":["laravel/framework is on the target major","composer.lock matches composer.json"],"check_cmd":"grep -q '\"laravel/framework\": \"^11' composer.json"}`
const be8a2860Tests = `{"title":"Run the test suite","artifacts":["test-results.txt"],"criteria":["the suite ran","no failures"],"check_cmd":"grep -q 'OK' test-results.txt"}`
const be8a2860Report = `{"title":"Write the security report","artifacts":["SECURITY_REPORT.md"],"criteria":["lists fixed advisories","names remaining risks"],"check_cmd":"grep -qi 'advisor' SECURITY_REPORT.md"}`
const be8a2860PR = `{"title":"Push branch and open pull request","evidence_only":true,"criteria":["branch pushed","PR open"],"check_cmd":"gh pr list --head mission/be8a2860 --json number --jq 'length' | awk '$1>=1'"}`

// TestBe8a2860PlanShape is the regression fixture: with a repo
// destination the only rejection is the PR unit; without that unit the
// plan is accepted.
func TestBe8a2860PlanShape(t *testing.T) {
	m := Mission{ID: "be8a2860", Kind: KindCoding, PlanGate: PlanGateState{RepoDestination: true}}
	r := &nativeRunner{log: slog.Default(), sandbox: func(_ context.Context, _, _, _ string, _ time.Duration, out io.Writer) (int, error) {
		_, _ = fmt.Fprint(out, "No such file or directory\n")
		return 1, nil
	}}
	full := `{"units":[` + be8a2860Upgrade + `,` + be8a2860Tests + `,` + be8a2860Report + `,` + be8a2860PR + `]}`
	_, err := r.acceptPlan(context.Background(), m, full)
	if err == nil || !strings.Contains(err.Error(), `unit "Push branch and open pull request" pushes a branch or opens a pull request`) {
		t.Fatalf("acceptPlan(full) = %v, want only the PR unit rejected", err)
	}
	noDest := m
	noDest.PlanGate = PlanGateState{}
	if _, err := r.acceptPlan(context.Background(), noDest, full); err != nil {
		t.Fatalf("acceptPlan(full, no destination) = %v, want the PR unit to be the only defect", err)
	}
	trimmed := `{"units":[` + be8a2860Upgrade + `,` + be8a2860Tests + `,` + be8a2860Report + `]}`
	plan, err := r.acceptPlan(context.Background(), m, trimmed)
	if err != nil {
		t.Fatalf("acceptPlan(without PR unit): %v", err)
	}
	if len(plan.Units) != 3 || plan.GranularityWaived {
		t.Fatalf("accepted plan = %+v, want 3 units and no waiver", plan)
	}
}

// TestPlanSessionRecoveryReplaysRejectedArgs (issue #1007): the
// recovery turn quotes the rejected submit_plan JSON, and each attempt
// runs acceptPlan once (one probe per attempt).
func TestPlanSessionRecoveryReplaysRejectedArgs(t *testing.T) {
	badCmd := "go test ./internal/core/... && grep -q Foo internal/core/a.go"
	goodCmd := "grep -q 'func TestFoo' internal/core/a_test.go && go test ./internal/core/ -run TestFoo"
	bad := `{"units":[{"title":"Foo","artifacts":["internal/core/a.go"],"criteria":["c1","c2"],"check_cmd":"` + badCmd + `"}]}`
	good := `{"units":[{"title":"Foo","artifacts":["internal/core/a.go","internal/core/a_test.go"],"criteria":["c1","c2"],"check_cmd":"` + goodCmd + `"}]}`
	calls := map[string]int{}
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, bad)},
		{toolEndEvent(planToolName, good)},
	}}
	r := newTestRunner(agent)
	r.sandbox = func(_ context.Context, _, _, command string, _ time.Duration, out io.Writer) (int, error) {
		calls[command]++
		if command == badCmd {
			return 0, nil
		}
		_, _ = fmt.Fprint(out, "no test files")
		return 1, nil
	}
	plan, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Kind: KindCoding}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.Units[0].CheckCmd != goodCmd {
		t.Fatalf("accepted check_cmd = %q, want the corrected gate", plan.Units[0].CheckCmd)
	}
	if calls[badCmd] != 1 || calls[goodCmd] != 1 {
		t.Fatalf("probe calls = %v, want acceptPlan once per attempt", calls)
	}
	last := agent.requests[1].Messages[len(agent.requests[1].Messages)-1].Content
	if !strings.Contains(last, "already exits 0") || !strings.Contains(last, "```json\n"+bad+"\n```") {
		t.Fatalf("recovery message must carry the reason and the rejected JSON, got %q", last)
	}
}

// TestPlanSessionWaivesSecondSplit (issue #1007): a split plan
// rejected for granularity and resubmitted split is waived in the same
// session; with the waiver spent the session fails.
func TestPlanSessionWaivesSecondSplit(t *testing.T) {
	split := `{"units":[{"title":"a","artifacts":["pkg/a.go"],"criteria":["c1","c2"],"check_cmd":"go test ./pkg/"},{"title":"b","artifacts":["pkg/b.go"],"criteria":["c1","c2"],"check_cmd":"go test ./pkg/"}]}`
	run := func(gate PlanGateState) (Plan, error) {
		agent := &scriptedAgent{batches: [][]stream.StreamEvent{
			{toolEndEvent(planToolName, split)},
			{toolEndEvent(planToolName, split)},
		}}
		return newTestRunner(agent).PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Kind: KindCoding, PlanGate: gate}, "")
	}
	plan, err := run(PlanGateState{})
	if err != nil || !plan.GranularityWaived {
		t.Fatalf("PlanSession = %+v, %v; want the resubmitted split waived", plan, err)
	}
	if _, err := run(PlanGateState{GranularityWaived: true}); err == nil || !strings.Contains(err.Error(), granularityMarker) {
		t.Fatalf("PlanSession with waiver spent = %v, want the granularity rejection", err)
	}
}

// TestAcceptPlanScope (issue #1023): scope entries must be
// workspace-relative; "." and "./" stay the whole tree.
func TestAcceptPlanScope(t *testing.T) {
	cases := []struct {
		scope   string
		want    string
		wantErr bool
	}{
		{"/", "", true},
		{"/abs/x", "", true},
		{"../x", "", true},
		{"a/../../x", "", true},
		{".", ".", false},
		{"./", "./", false},
		{"src/", "src/", false},
	}
	r := &nativeRunner{log: slog.Default()}
	m := Mission{Kind: KindGeneral}
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			raw := `{"units":[{"title":"docs","artifacts":["CHANGELOG.md","CONTRIBUTING.md"],"scope":[` + strconv.Quote(tc.scope) + `],"criteria":["c1","c2"],"check_cmd":"grep -q x CHANGELOG.md"}]}`
			plan, err := r.acceptPlan(context.Background(), m, raw)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), strconv.Quote(tc.scope)) || !strings.Contains(err.Error(), "workspace-relative") {
					t.Fatalf("acceptPlan = %v, want rejection naming %q", err, tc.scope)
				}
				return
			}
			if err != nil {
				t.Fatalf("acceptPlan: %v", err)
			}
			if got := plan.Units[0].Scope; len(got) != 1 || got[0] != tc.want {
				t.Fatalf("scope = %v, want [%s]", got, tc.want)
			}
		})
	}
}
