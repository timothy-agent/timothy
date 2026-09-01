package destinations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

func TestArtifactPathsDedupesAcrossUnits(t *testing.T) {
	m := missions.Mission{Plan: missions.Plan{Units: []missions.PlanUnit{
		{Artifacts: []string{"a.md", "b.md"}},
		{Artifacts: []string{"b.md", "c.md", ""}},
	}}}
	got := artifactPaths(m)
	want := []string{"a.md", "b.md", "c.md"}
	if len(got) != len(want) {
		t.Fatalf("artifactPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("artifactPaths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveArtifactFilesReadsUnderWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "out.md"), []byte("report body"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m := missions.Mission{Workspace: root, Plan: missions.Plan{Units: []missions.PlanUnit{
		{Artifacts: []string{"out.md"}},
	}}}

	files, texts, oversize := resolveArtifactFiles(m)
	if len(oversize) != 0 {
		t.Fatalf("expected no oversize files, got %v", oversize)
	}
	if len(files) != 0 {
		t.Fatalf("expected .md to route to texts, not files: %+v", files)
	}
	if len(texts) != 1 || texts[0].Name != "out.md" || texts[0].Content != "report body" {
		t.Fatalf("unexpected texts: %+v", texts)
	}
}

func TestResolveArtifactFilesUsesWorktreeOverWorkspace(t *testing.T) {
	workspace := t.TempDir()
	worktree := filepath.Join(workspace, "wt")
	if err := os.MkdirAll(worktree, 0o750); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "out.md"), []byte("from worktree"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m := missions.Mission{Kind: "coding", Flow: "full", Workspace: workspace, Plan: missions.Plan{Units: []missions.PlanUnit{
		{Artifacts: []string{"out.md"}},
	}}}

	_, texts, _ := resolveArtifactFiles(m)
	if len(texts) != 1 || texts[0].Content != "from worktree" {
		t.Fatalf("expected the worktree copy to win, got %+v", texts)
	}
}

func TestResolveArtifactFilesRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	// A file that genuinely exists just outside root, so a naive
	// filepath.Join without the guard would happily read it.
	outside := filepath.Dir(root)
	secretPath := filepath.Join(outside, "secret-"+filepath.Base(root)+".txt")
	if err := os.WriteFile(secretPath, []byte("should never be read"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(secretPath) })

	tests := []string{
		"../" + filepath.Base(secretPath),
		"/etc/passwd",
		"..",
	}
	for _, artifact := range tests {
		m := missions.Mission{Workspace: root, Plan: missions.Plan{Units: []missions.PlanUnit{
			{Artifacts: []string{artifact}},
		}}}
		files, _, _ := resolveArtifactFiles(m)
		if len(files) != 0 {
			t.Fatalf("artifact %q: expected no files resolved (path escapes workspace), got %+v", artifact, files)
		}
	}
}

func TestResolveArtifactFilesSkipsMissingFile(t *testing.T) {
	root := t.TempDir()
	m := missions.Mission{Workspace: root, Plan: missions.Plan{Units: []missions.PlanUnit{
		{Artifacts: []string{"never-written.md"}},
	}}}
	files, _, oversize := resolveArtifactFiles(m)
	if len(files) != 0 || len(oversize) != 0 {
		t.Fatalf("expected nothing resolved for a missing file, got files=%v oversize=%v", files, oversize)
	}
}

func TestResolveArtifactFilesOversizeListedByName(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, MaxAttachBytes+1)
	if err := os.WriteFile(filepath.Join(root, "huge.bin"), big, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m := missions.Mission{Workspace: root, Plan: missions.Plan{Units: []missions.PlanUnit{
		{Artifacts: []string{"huge.bin"}},
	}}}
	files, _, oversize := resolveArtifactFiles(m)
	if len(files) != 0 {
		t.Fatalf("expected the oversize file not attached, got %+v", files)
	}
	if len(oversize) != 1 || oversize[0] != "huge.bin" {
		t.Fatalf("expected huge.bin listed as oversize, got %v", oversize)
	}
}

func TestResolveArtifactFilesNoWorkspace(t *testing.T) {
	m := missions.Mission{Plan: missions.Plan{Units: []missions.PlanUnit{{Artifacts: []string{"a.md"}}}}}
	files, _, oversize := resolveArtifactFiles(m)
	if len(files) != 0 || len(oversize) != 0 {
		t.Fatalf("expected nothing resolved with no workspace, got files=%v oversize=%v", files, oversize)
	}
}

func TestOversizeNotice(t *testing.T) {
	if got := oversizeNotice(nil); got != "" {
		t.Fatalf("oversizeNotice(nil) = %q, want empty", got)
	}
	got := oversizeNotice([]string{"a.zip", "b.zip"})
	if got == "" {
		t.Fatal("expected a non-empty notice")
	}
	for _, name := range []string{"a.zip", "b.zip"} {
		if !strings.Contains(got, name) {
			t.Fatalf("notice %q missing %q", got, name)
		}
	}
}
