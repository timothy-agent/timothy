package missions

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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

// TestCheckCodeFloor covers the toolchain requirement per environment:
// a unit that produces source must build, test or run it; document
// units and unknown environments stay permissive.
func TestCheckCodeFloor(t *testing.T) {
	cases := []struct {
		name      string
		env       string
		artifacts []string
		cmd       string
		wantErr   string
	}{
		{"go grep only", "go", []string{"internal/core/a.go"}, `grep -q 'func Foo' internal/core/a.go`, "never builds, tests or runs"},
		{"go test first", "go", []string{"internal/core/a.go"}, `go test ./internal/core/... && grep -q Foo internal/core/a.go`, ""},
		{"go vet counts", "go", []string{"a.go"}, `GOTOOLCHAIN=auto go vet ./... && grep -q x a.go`, ""},
		{"go doc unit", "go", []string{"docs/DESIGN.md"}, `grep -q '## Scheme' docs/DESIGN.md`, ""},
		{"python grep only", "python", []string{"app/a.py"}, `grep -q 'def main' app/a.py`, "never builds, tests or runs"},
		{"python pytest", "python", []string{"app/a.py"}, `python3 -m pytest tests/ -q`, ""},
		{"node vitest", "node", []string{"src/a.ts"}, `npx vitest run src`, ""},
		{"node grep only", "node", []string{"src/a.ts"}, `grep -q export src/a.ts`, "never builds, tests or runs"},
		{"unknown env accepts any toolchain", "", []string{"a.py"}, `pytest tests`, ""},
		{"unknown env still needs one", "", []string{"a.py"}, `grep -q def a.py`, "never builds, tests or runs"},
		{"wrong env toolchain rejected", "go", []string{"a.go"}, `npm test`, "never builds, tests or runs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := Plan{Units: []PlanUnit{{Title: "u", Artifacts: tc.artifacts, CheckCmd: tc.cmd}}}
			err := checkCodeFloor(plan, tc.env)
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
	return func(_ context.Context, _, _, _, command string, _ time.Duration, out io.Writer) (int, error) {
		_, _ = fmt.Fprint(out, output)
		return codes[command], nil
	}
}

// TestProbeCheckCmds covers the three probe outcomes: a gate that
// already passes is rejected, a missing command is rejected, an
// ordinary failure (artifacts not there yet) is the expected state.
func TestProbeCheckCmds(t *testing.T) {
	m := Mission{ID: "m1", Kind: KindCoding, Environment: "go"}
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
	plan, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Kind: KindCoding, Environment: "go"}, "")
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
	_, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Kind: KindCoding, Environment: "go"}, "")
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
