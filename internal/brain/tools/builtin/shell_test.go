package builtin

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

func shellTool(t *testing.T, timeout time.Duration) (*tools.Tool, string) {
	t.Helper()
	dir := t.TempDir()
	return Shell(ShellConfig{WorkspaceRoot: dir, Timeout: timeout}), dir
}

func runTool(t *testing.T, tool *tools.Tool, command string) (string, error) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"command": command})
	return tool.Execute(context.Background(), args)
}

func TestShellRunsInWorkspace(t *testing.T) {
	t.Parallel()
	tool, dir := shellTool(t, 0)

	got, err := runTool(t, tool, "pwd")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, dir) {
		t.Fatalf("pwd = %q, want workspace %q", got, dir)
	}
}

func TestShellReportsExitStatusWithOutput(t *testing.T) {
	t.Parallel()
	tool, _ := shellTool(t, 0)

	got, err := runTool(t, tool, "echo before-fail; exit 3")
	if err != nil {
		t.Fatalf("nonzero exit should not be an execute error: %v", err)
	}
	if !strings.Contains(got, "before-fail") || !strings.Contains(got, "exit status 3") {
		t.Fatalf("result = %q, want output plus exit status", got)
	}
}

func TestShellCombinesStderr(t *testing.T) {
	t.Parallel()
	tool, _ := shellTool(t, 0)

	got, err := runTool(t, tool, "echo to-stderr 1>&2")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "to-stderr") {
		t.Fatalf("stderr missing from result %q", got)
	}
}

func TestShellTimesOut(t *testing.T) {
	t.Parallel()
	tool, _ := shellTool(t, 200*time.Millisecond)

	start := time.Now()
	_, err := runTool(t, tool, "sleep 5")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout did not kill the command promptly")
	}
}

func TestShellCapsOutput(t *testing.T) {
	t.Parallel()
	tool, _ := shellTool(t, 0)

	got, err := runTool(t, tool, "yes x | head -c 200000")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(got) > shellMaxOutput+100 {
		t.Fatalf("output length %d exceeds cap", len(got))
	}
	if !strings.Contains(got, "[output capped]") {
		t.Fatal("capped output missing marker")
	}
}

// TestShellRejectsSymlinkEscape is the sandbox-bypass this tool must
// close on its own: a symlink already sitting inside the workspace
// (e.g. brought in by a repo checkout) pointing outside it lets a
// purely workspace-relative command like "rm -rf vendor/lib/file"
// destroy something elsewhere. Nothing in the permission chain's
// lexical containment check catches this — pathWithin only inspects
// absolute tokens, and a relative token never trips it. The tool
// itself re-checks with symlinks resolved right before it execs.
func TestShellRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	tool, dir := shellTool(t, 0)
	outside := t.TempDir()
	targetPath := outside + "/target"
	if err := os.WriteFile(targetPath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := os.Symlink(outside, dir+"/escape"); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, err := runTool(t, tool, "rm -rf escape/target")
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("err = %v, want symlink escape rejected", err)
	}
	if _, statErr := os.Stat(targetPath); statErr != nil {
		t.Fatalf("target was deleted through the symlink: %v", statErr)
	}
}

func TestShellRejectsEmptyCommandAndMissingWorkspace(t *testing.T) {
	t.Parallel()
	tool, _ := shellTool(t, 0)
	if _, err := runTool(t, tool, ""); err == nil {
		t.Fatal("empty command accepted")
	}

	unconfigured := Shell(ShellConfig{})
	if _, err := runTool(t, unconfigured, "true"); err == nil ||
		!strings.Contains(err.Error(), "workspace") {
		t.Fatalf("err = %v, want workspace error", err)
	}
}

// TestShellUsesRunnerWhenConfigured confirms a configured Runner (the
// sandbox-container backend) is called instead of local exec, and
// receives exactly the resolved command and timeout — the local
// exec.CommandContext path must never run when Runner is set.
func TestShellUsesRunnerWhenConfigured(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var gotCmd string
	var gotTimeout time.Duration
	runnerCalled := false
	tool := Shell(ShellConfig{
		WorkspaceRoot: dir,
		Runner: func(ctx context.Context, command string, timeout time.Duration) (string, error) {
			runnerCalled = true
			gotCmd, gotTimeout = command, timeout
			return "from runner", nil
		},
	})

	out, err := runTool(t, tool, "echo local-would-say-this")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !runnerCalled {
		t.Fatal("Runner was not called; local exec ran instead")
	}
	if out != "from runner" {
		t.Fatalf("output = %q, want the Runner's own result, not local exec output", out)
	}
	if gotCmd != "echo local-would-say-this" {
		t.Fatalf("Runner received command %q", gotCmd)
	}
	if gotTimeout != shellDefaultTimeout {
		t.Fatalf("Runner received timeout %s, want default %s", gotTimeout, shellDefaultTimeout)
	}
}

// TestShellRunnerStillRejectsSymlinkEscape confirms CheckCommandPaths
// still gates a Runner-backed shell — the containment check is
// path-shape analysis, independent of which backend actually executes,
// and must not be bypassed just because a sandbox backend is present.
func TestShellRunnerStillRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(outside+"/target", []byte("secret"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := os.Symlink(outside, dir+"/escape"); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	runnerCalled := false
	tool := Shell(ShellConfig{
		WorkspaceRoot: dir,
		Runner: func(ctx context.Context, command string, timeout time.Duration) (string, error) {
			runnerCalled = true
			return "", nil
		},
	})

	_, err := runTool(t, tool, "rm -rf escape/target")
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("err = %v, want symlink escape rejected", err)
	}
	if runnerCalled {
		t.Fatal("Runner was called despite a symlink-escape violation")
	}
}

// TestShellRunnerRespectsConfiguredMaxTimeout confirms MaxTimeout caps
// a model-requested timeout_seconds independent of ShellTimeoutClamp —
// ExtraTools (mission-scoped shells) bypass that middleware entirely,
// so this in-Execute clamp is the only enforcement they actually get.
func TestShellRunnerRespectsConfiguredMaxTimeout(t *testing.T) {
	t.Parallel()
	var gotTimeout time.Duration
	tool := Shell(ShellConfig{
		WorkspaceRoot: t.TempDir(),
		MaxTimeout:    5 * time.Second,
		Runner: func(ctx context.Context, command string, timeout time.Duration) (string, error) {
			gotTimeout = timeout
			return "", nil
		},
	})
	args, _ := json.Marshal(map[string]any{"command": "true", "timeout_seconds": 3600})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotTimeout != 5*time.Second {
		t.Fatalf("Runner received timeout %s, want the configured MaxTimeout of 5s", gotTimeout)
	}
}

// TestShellRunnerAllowsHigherMaxTimeoutThanChatDefault confirms a
// mission-scoped shell (MaxTimeout above ShellMaxTimeout) is not
// clamped down to chat's 120s ceiling — the whole point of the
// sandboxed shell getting a longer ceiling for app-dev workloads.
func TestShellRunnerAllowsHigherMaxTimeoutThanChatDefault(t *testing.T) {
	t.Parallel()
	var gotTimeout time.Duration
	tool := Shell(ShellConfig{
		WorkspaceRoot: t.TempDir(),
		MaxTimeout:    15 * time.Minute,
		Runner: func(ctx context.Context, command string, timeout time.Duration) (string, error) {
			gotTimeout = timeout
			return "", nil
		},
	})
	args, _ := json.Marshal(map[string]any{"command": "true", "timeout_seconds": 600})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotTimeout != 600*time.Second {
		t.Fatalf("Runner received timeout %s, want the requested 600s (under the 15min MaxTimeout)", gotTimeout)
	}
}
