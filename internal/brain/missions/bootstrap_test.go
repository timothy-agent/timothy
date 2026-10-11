package missions

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

const (
	bootstrapCheck = "./.tools/php/bin/php -v | grep -q 'PHP 8'"
	laterCheck     = "./.tools/php/bin/php artisan test"
)

func bootstrapPlan() Plan {
	return Plan{Units: []PlanUnit{
		{Title: "Install PHP", Bootstrap: true, Artifacts: []string{".tools/php/bin/php"}, CheckCmd: bootstrapCheck},
		{Title: "Add feature", Artifacts: []string{"app/a.php"}, CheckCmd: laterCheck},
	}}
}

// TestProbeCheckCmdsBootstrap covers D-124 (issue #980): a missing
// command is the expected pre-state when the first unit is a bootstrap
// unit, and still a rejection otherwise; "already exits 0" applies to
// every unit regardless.
func TestProbeCheckCmdsBootstrap(t *testing.T) {
	m := Mission{ID: "m1", Kind: KindCoding}
	noBootstrap := Plan{Units: []PlanUnit{{Title: "Add feature", Artifacts: []string{"app/a.php"}, CheckCmd: laterCheck}}}
	cases := []struct {
		name    string
		plan    Plan
		codes   map[string]int
		output  string
		wantErr string
	}{
		{"bootstrap unit exit 127", bootstrapPlan(), map[string]int{bootstrapCheck: 127, laterCheck: 1}, "x\n", ""},
		{"bootstrap and later unit exit 127", bootstrapPlan(), map[string]int{bootstrapCheck: 127, laterCheck: 127}, "x\n", ""},
		{"shell not-found line", bootstrapPlan(), map[string]int{bootstrapCheck: 1, laterCheck: 1}, "/bin/sh: 1: php: not found\n", ""},
		{"no bootstrap exit 127", noBootstrap, map[string]int{laterCheck: 127}, "x\n", "does not have"},
		{"bootstrap check already exits 0", bootstrapPlan(), map[string]int{bootstrapCheck: 0, laterCheck: 127}, "x\n", "already exits 0"},
		{"later unit already exits 0", bootstrapPlan(), map[string]int{bootstrapCheck: 127, laterCheck: 0}, "x\n", "already exits 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &nativeRunner{log: slog.Default(), sandbox: scriptedSandbox(tc.codes, tc.output)}
			err := r.probeCheckCmds(context.Background(), m, tc.plan)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("probe = %v, want accepted", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("probe = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestCheckBootstrap(t *testing.T) {
	u := func(title string, bootstrap bool, cmd string) PlanUnit {
		return PlanUnit{Title: title, Bootstrap: bootstrap, CheckCmd: cmd}
	}
	cases := []struct {
		name    string
		kind    string
		units   []PlanUnit
		wantErr []string
	}{
		{"general mission bootstrap rejected", KindGeneral, []PlanUnit{u("Bootstrap toolchain", true, "a"), u("Write explainer", false, "b")}, []string{"Bootstrap toolchain", "only coding missions", "drop the bootstrap unit"}},
		{"general mission without bootstrap", KindGeneral, []PlanUnit{u("Write explainer", false, "a")}, nil},
		{"two bootstrap units", KindCoding, []PlanUnit{u("Install PHP", true, "a"), u("Install Composer", true, "b")}, []string{"Install PHP", "Install Composer"}},
		{"bootstrap not first", KindCoding, []PlanUnit{u("Feature", false, "a"), u("Install PHP", true, "b")}, []string{"Install PHP", "first unit"}},
		{"bootstrap without check_cmd", KindCoding, []PlanUnit{u("Install PHP", true, "  "), u("Feature", false, "b")}, []string{"Install PHP", "check_cmd"}},
		{"valid first bootstrap", KindCoding, []PlanUnit{u("Install PHP", true, "a"), u("Feature", false, "b")}, nil},
		{"no bootstrap", KindCoding, []PlanUnit{u("Feature", false, "a")}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkBootstrap(Plan{Units: tc.units}, Mission{Kind: tc.kind})
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("checkBootstrap = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("checkBootstrap = nil, want error")
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("checkBootstrap = %v, want containing %q", err, w)
				}
			}
		})
	}
}

func TestParsePlanBootstrapRoundTrip(t *testing.T) {
	plan, err := parsePlan(`{"units":[{"title":"Install PHP","bootstrap":true,"artifacts":[".tools/php/bin/php"],"criteria":["a","b"],"check_cmd":"./.tools/php/bin/php -v"}]}`)
	if err != nil {
		t.Fatalf("parsePlan: %v", err)
	}
	if !plan.Units[0].Bootstrap {
		t.Fatalf("Bootstrap = false, want true")
	}
}

// TestShellNotFoundRealShell runs a missing command through the real
// /bin/sh: the exit code and the last stderr line are what the probe
// keys on, so a fake sandbox cannot vouch for them.
func TestShellNotFoundRealShell(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this machine")
	}
	var buf bytes.Buffer
	cmd := exec.Command("/bin/sh", "-c", "php-definitely-missing -v | grep -q 'PHP 8'") //nolint:gosec // G204: executing the composed command is the point of the test.
	cmd.Stderr = &buf
	cmd.Stdout = &buf
	_ = cmd.Run()
	if line := lastLine(buf.String()); !shellNotFound.MatchString(line) {
		t.Fatalf("last line %q does not match shellNotFound", line)
	}
	buf.Reset()
	cmd = exec.Command("/bin/sh", "-c", "php-definitely-missing -v") //nolint:gosec // G204: executing the composed command is the point of the test.
	cmd.Stderr = &buf
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 127 {
		t.Fatalf("exit = %v, want 127", err)
	}
	if !shellNotFound.MatchString(lastLine(buf.String())) {
		t.Fatalf("stderr %q does not match shellNotFound", buf.String())
	}
}

// TestPlanSessionAcceptsBootstrapPlan: the planner submits a bootstrap
// first unit and the sandbox reports every command missing; the plan is
// accepted on the first turn.
func TestPlanSessionAcceptsBootstrapPlan(t *testing.T) {
	good := `{"units":[` +
		`{"title":"Install PHP","bootstrap":true,"artifacts":[".tools/php/bin/php"],"criteria":["c1","c2"],"check_cmd":"./.tools/php/bin/php -v | grep -q 'PHP 8'"},` +
		`{"title":"Add feature","artifacts":["app/a.php"],"criteria":["c1","c2"],"check_cmd":"./.tools/php/bin/php artisan test"}]}`
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{toolEndEvent(planToolName, good)}}}
	r := newTestRunner(agent)
	r.sandbox = func(_ context.Context, _, _, _ string, _ time.Duration, out io.Writer) (int, error) {
		_, _ = out.Write([]byte("sh: 1: ./.tools/php/bin/php: not found\n"))
		return 127, nil
	}
	plan, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Kind: KindCoding}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if agent.call != 1 || len(plan.Units) != 2 || !plan.Units[0].Bootstrap {
		t.Fatalf("plan = %+v after %d turns, want the bootstrap plan accepted at once", plan, agent.call)
	}
}

// TestPlanPromptBootstrapRule: the bootstrap instruction reaches the
// planner only for coding missions whose discover notes carry a
// harness bootstrap allowance (issue #996).
func TestPlanPromptBootstrapRule(t *testing.T) {
	stackNote := "Stack: Rust CLI. The sandbox has no preinstalled toolchain for it; the plan's first unit must be a " + bootstrapAllowance + " that installs it into the workspace.\n\nfindings"
	cases := []struct {
		name  string
		kind  string
		notes string
		want  bool
	}{
		{"general mission", KindGeneral, "plain findings", false},
		{"general mission with a stack note", KindGeneral, stackNote, false},
		{"coding mission without stack note", KindCoding, "go.mod at the root", false},
		{"coding mission with stack note", KindCoding, stackNote, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed := bootstrapAllowed(Mission{Kind: tc.kind}, tc.notes)
			if allowed != tc.want {
				t.Fatalf("bootstrapAllowed = %v, want %v", allowed, tc.want)
			}
			for _, hasPlan := range []bool{false, true} {
				got := strings.Contains(planSystemPrompt(hasPlan, allowed, tc.kind == KindCoding), "bootstrap=true")
				if got != tc.want {
					t.Fatalf("hasPlan=%v: prompt carries bootstrap rule = %v, want %v", hasPlan, got, tc.want)
				}
			}
		})
	}
}
