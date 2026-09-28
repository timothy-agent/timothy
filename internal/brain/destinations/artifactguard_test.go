package destinations

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// requireGitForGuard skips when git isn't on PATH, same reasoning as
// missions/push_test.go's requireGitForPush (a distinct package, so a
// distinct helper).
func requireGitForGuard(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping artifact guard test")
	}
}

func gitRunGuard(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // args are fixed test-fixture git subcommands, not user input
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// initGuardRepo git-inits worktree with an initial commit and returns
// its hash, the guard's base_commit.
func initGuardRepo(t *testing.T, worktree string) string {
	t.Helper()
	if err := os.MkdirAll(worktree, 0o750); err != nil {
		t.Fatal(err)
	}
	gitRunGuard(t, worktree, "init", "-q")
	gitRunGuard(t, worktree, "config", "user.email", "test@example.com")
	gitRunGuard(t, worktree, "config", "user.name", "Test")
	commitGuardFiles(t, worktree, map[string][]byte{"README.md": []byte("base\n")}, "base")
	return strings.TrimSpace(gitRunGuard(t, worktree, "rev-parse", "HEAD"))
}

// commitGuardFiles writes files (creating parent dirs) and commits
// them in worktree.
func commitGuardFiles(t *testing.T, worktree string, files map[string][]byte, message string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(worktree, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitRunGuard(t, worktree, "add", "-A")
	gitRunGuard(t, worktree, "commit", "-q", "-m", message)
}

func unit(artifacts, scope []string) missions.PlanUnit {
	return missions.PlanUnit{Title: "unit", Artifacts: artifacts, Scope: scope}
}

// TestCheckDeliveryArtifacts table-tests the diff-vs-declared
// intersection (issue #949, D-122): empty (no declared path in diff),
// partial (some declared, some extra), full (every diff path
// declared), and a gitignored declared artifact explaining the empty
// case.
func TestCheckDeliveryArtifacts(t *testing.T) {
	requireGitForGuard(t)

	t.Run("empty: diff shares no path with declared", func(t *testing.T) {
		t.Parallel()
		wt := t.TempDir()
		base := initGuardRepo(t, wt)
		commitGuardFiles(t, wt, map[string][]byte{"core.234": {0x7f, 'E', 'L', 'F'}}, "add core dump")
		plan := missions.Plan{Units: []missions.PlanUnit{unit([]string{"internal/api/handler.go"}, nil)}}

		result, err := checkDeliveryArtifacts(t.Context(), wt, base, plan)
		if !errors.Is(err, ErrDeliveryNoArtifacts) {
			t.Fatalf("err = %v, want ErrDeliveryNoArtifacts", err)
		}
		if len(result.diff) != 1 || result.diff[0] != "core.234" {
			t.Fatalf("diff = %v, want [core.234]", result.diff)
		}
	})

	t.Run("partial: a declared path plus an extra", func(t *testing.T) {
		t.Parallel()
		wt := t.TempDir()
		base := initGuardRepo(t, wt)
		commitGuardFiles(t, wt, map[string][]byte{
			"internal/api/handler.go": []byte("package api\n"),
			"debug.log":               []byte("trace\n"),
		}, "add handler and stray log")
		plan := missions.Plan{Units: []missions.PlanUnit{unit([]string{"internal/api/handler.go"}, nil)}}

		result, err := checkDeliveryArtifacts(t.Context(), wt, base, plan)
		if err != nil {
			t.Fatalf("checkDeliveryArtifacts: %v", err)
		}
		if len(result.extra) != 1 || result.extra[0] != "debug.log" {
			t.Fatalf("extra = %v, want [debug.log]", result.extra)
		}
	})

	t.Run("full: every diff path declared", func(t *testing.T) {
		t.Parallel()
		wt := t.TempDir()
		base := initGuardRepo(t, wt)
		commitGuardFiles(t, wt, map[string][]byte{"internal/api/handler.go": []byte("package api\n")}, "add handler")
		plan := missions.Plan{Units: []missions.PlanUnit{unit([]string{"internal/api/handler.go"}, nil)}}

		result, err := checkDeliveryArtifacts(t.Context(), wt, base, plan)
		if err != nil {
			t.Fatalf("checkDeliveryArtifacts: %v", err)
		}
		if len(result.extra) != 0 {
			t.Fatalf("extra = %v, want none", result.extra)
		}
	})

	t.Run("scope covers a path under a declared directory", func(t *testing.T) {
		t.Parallel()
		wt := t.TempDir()
		base := initGuardRepo(t, wt)
		commitGuardFiles(t, wt, map[string][]byte{"internal/api/helper.go": []byte("package api\n")}, "add helper")
		plan := missions.Plan{Units: []missions.PlanUnit{unit([]string{"internal/api/handler.go"}, []string{"internal/api"})}}

		result, err := checkDeliveryArtifacts(t.Context(), wt, base, plan)
		if err != nil {
			t.Fatalf("checkDeliveryArtifacts: %v", err)
		}
		if len(result.extra) != 0 {
			t.Fatalf("extra = %v, want none: helper.go is under the declared scope dir", result.extra)
		}
	})

	t.Run("gitignored: every declared artifact is git-ignored", func(t *testing.T) {
		t.Parallel()
		wt := t.TempDir()
		base := initGuardRepo(t, wt)
		commitGuardFiles(t, wt, map[string][]byte{
			".gitignore": []byte("dist/\n"),
			"core.234":   {0x7f, 'E', 'L', 'F'},
		}, "add gitignore and core dump")
		plan := missions.Plan{Units: []missions.PlanUnit{unit([]string{"dist/bundle.js"}, nil)}}

		result, err := checkDeliveryArtifacts(t.Context(), wt, base, plan)
		if !errors.Is(err, ErrDeliveryNoArtifacts) {
			t.Fatalf("err = %v, want ErrDeliveryNoArtifacts", err)
		}
		if len(result.ignored) != 1 || result.ignored[0] != "dist/bundle.js" {
			t.Fatalf("ignored = %v, want [dist/bundle.js]", result.ignored)
		}
		if !strings.Contains(err.Error(), "dist/bundle.js") {
			t.Fatalf("error %q does not name the gitignored path", err.Error())
		}
	})
}

// TestDeliverMissionPushPRRefusesUndeclaredDiff is the destination-side
// test the issue asks for: a push_pr destination whose diff carries
// only a file no unit declared must push nothing and open no PR,
// failing with ErrDeliveryNoArtifacts and recording the diff paths on
// mission.delivery_no_artifacts.
func TestDeliverMissionPushPRRefusesUndeclaredDiff(t *testing.T) {
	requireGitForGuard(t)
	m := pushableMission(t)
	wt := m.WorktreePath()
	base := initGuardRepo(t, wt)
	commitGuardFiles(t, wt, map[string][]byte{"unexpected.bin": {0x00, 0x01}}, "add unexpected binary")
	m.BaseCommit = base
	m.Plan = missions.Plan{Units: []missions.PlanUnit{unit([]string{"internal/api/handler.go"}, nil)}}

	p := &fakePusher{host: "github.com"}
	ev := &fakeEvents{}
	c := githubClient(&fakeGitClient{repoExists: true, defaultBranch: "main"})
	a := &RepoAdapter{Pusher: p, Events: ev, ResolveToken: func(context.Context, string) (string, error) { return "tok", nil }, Clients: clients(c)}
	e := &missions.DestinationEntry{RepoURL: "https://github.com/octo/repo.git"}

	err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push_pr"}, m, e)
	if !errors.Is(err, ErrDeliveryNoArtifacts) {
		t.Fatalf("err = %v, want ErrDeliveryNoArtifacts", err)
	}
	if p.pushCalls != 0 {
		t.Fatalf("pushCalls = %d, want 0: no push on a refused delivery", p.pushCalls)
	}
	if c.createCalls != 0 {
		t.Fatalf("CreatePR called %d times, want 0: no PR on a refused delivery", c.createCalls)
	}
	if e.PRURL != "" {
		t.Fatalf("entry PRURL = %q, want empty", e.PRURL)
	}
	payload, ok := ev.find("mission.delivery_no_artifacts")
	if !ok {
		t.Fatal("mission.delivery_no_artifacts event not recorded")
	}
	diffPaths, _ := payload["diff_paths"].([]string)
	if len(diffPaths) != 1 || diffPaths[0] != "unexpected.bin" {
		t.Fatalf("diff_paths = %v, want [unexpected.bin]", payload["diff_paths"])
	}
}

// TestDeliverMissionPushPRRecordsExtraPaths covers the AC's success
// path: a diff with at least one declared path plus an extra one still
// opens the PR, and the extra path is listed on mission.pr_opened.
func TestDeliverMissionPushPRRecordsExtraPaths(t *testing.T) {
	requireGitForGuard(t)
	m := pushableMission(t)
	wt := m.WorktreePath()
	base := initGuardRepo(t, wt)
	commitGuardFiles(t, wt, map[string][]byte{
		"internal/api/handler.go": []byte("package api\n"),
		"debug.log":               []byte("trace\n"),
	}, "add handler and stray log")
	m.BaseCommit = base
	m.Plan = missions.Plan{Units: []missions.PlanUnit{unit([]string{"internal/api/handler.go"}, nil)}}

	ev := &fakeEvents{}
	c := githubClient(&fakeGitClient{repoExists: true, defaultBranch: "main", prURL: "https://github.com/octo/repo/pull/1", prNumber: 1})
	a := &RepoAdapter{Pusher: &fakePusher{host: "github.com"}, Events: ev, ResolveToken: func(context.Context, string) (string, error) { return "tok", nil }, Clients: clients(c)}
	e := &missions.DestinationEntry{RepoURL: "https://github.com/octo/repo.git"}

	if err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push_pr"}, m, e); err != nil {
		t.Fatalf("DeliverMission: %v", err)
	}
	if e.PRURL == "" {
		t.Fatal("PR was not opened despite a declared path in the diff")
	}
	payload, ok := ev.find("mission.pr_opened")
	if !ok {
		t.Fatal("mission.pr_opened event not recorded")
	}
	extra, _ := payload["extra_paths"].([]string)
	if len(extra) != 1 || extra[0] != "debug.log" {
		t.Fatalf("extra_paths = %v, want [debug.log]", payload["extra_paths"])
	}
}

// TestDeliverMissionPushPRRegressionF78f7fff pins the exact incident
// (mission f78f7fff opened SumonMSelim/scholia#1 with a single
// undeclared core dump, issue #949): a plan whose units declare source
// artifacts, with a diff carrying only core.234, must fail delivery
// rather than open the PR.
func TestDeliverMissionPushPRRegressionF78f7fff(t *testing.T) {
	requireGitForGuard(t)
	m := pushableMission(t)
	wt := m.WorktreePath()
	base := initGuardRepo(t, wt)
	commitGuardFiles(t, wt, map[string][]byte{"core.234": {0x7f, 'E', 'L', 'F', 0x02, 0x01}}, "core dump")
	m.BaseCommit = base
	m.Plan = missions.Plan{Units: []missions.PlanUnit{
		unit([]string{"internal/shortener/handler.go"}, nil),
		unit([]string{"internal/shortener/handler_test.go"}, nil),
	}}

	p := &fakePusher{host: "github.com"}
	ev := &fakeEvents{}
	c := githubClient(&fakeGitClient{repoExists: true, defaultBranch: "main"})
	a := &RepoAdapter{Pusher: p, Events: ev, ResolveToken: func(context.Context, string) (string, error) { return "tok", nil }, Clients: clients(c)}
	e := &missions.DestinationEntry{RepoURL: "https://github.com/octo/scholia.git"}

	err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push_pr"}, m, e)
	if !errors.Is(err, ErrDeliveryNoArtifacts) {
		t.Fatalf("err = %v, want ErrDeliveryNoArtifacts (regression: f78f7fff's core.234 must never reach a PR)", err)
	}
	if c.createCalls != 0 {
		t.Fatalf("CreatePR called %d times, want 0", c.createCalls)
	}
}
