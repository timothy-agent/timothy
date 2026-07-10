package builtin

import (
	"context"
	"encoding/json"
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
