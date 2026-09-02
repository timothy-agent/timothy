package missions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGapFingerprintEmpty(t *testing.T) {
	if got := GapFingerprint(nil); got != "" {
		t.Fatalf("GapFingerprint(nil) = %q, want empty string", got)
	}
	if got := GapFingerprint([]Finding{}); got != "" {
		t.Fatalf("GapFingerprint([]) = %q, want empty string", got)
	}
}

func TestGapFingerprintOrderIndependent(t *testing.T) {
	a := []Finding{
		{Title: "missing nil check", File: "foo.go", Detail: "first phrasing"},
		{Title: "unused import", File: "bar.go", Detail: "second phrasing"},
	}
	b := []Finding{
		{Title: "unused import", File: "bar.go", Detail: "different detail entirely"},
		{Title: "missing nil check", File: "foo.go", Detail: "yet another phrasing"},
	}
	fa, fb := GapFingerprint(a), GapFingerprint(b)
	if fa == "" || fb == "" {
		t.Fatal("non-empty findings produced an empty fingerprint")
	}
	if fa != fb {
		t.Fatalf("fingerprints differ for the same (title,file) pairs in different order and different detail: %q vs %q", fa, fb)
	}
}

func TestGapFingerprintDifferentFindingsDiffer(t *testing.T) {
	a := []Finding{{Title: "missing nil check", File: "foo.go"}}
	b := []Finding{{Title: "off by one", File: "foo.go"}}
	if GapFingerprint(a) == GapFingerprint(b) {
		t.Fatal("different findings produced the same fingerprint")
	}
}

func TestGapFingerprintNormalizesCaseAndWhitespace(t *testing.T) {
	a := []Finding{{Title: "  Missing Nil Check  ", File: "Foo.go"}}
	b := []Finding{{Title: "missing nil check", File: "foo.go"}}
	if GapFingerprint(a) != GapFingerprint(b) {
		t.Fatal("case/whitespace normalization did not collapse equivalent findings")
	}
}

func TestParseReviewVerdict(t *testing.T) {
	v, err := parseReviewVerdict([]byte(`{"decision":"rework","findings":[{"title":"x","file":"y.go","detail":"z"}]}`))
	if err != nil {
		t.Fatalf("parseReviewVerdict: %v", err)
	}
	if v.Approved || len(v.Findings) != 1 || v.Findings[0].Title != "x" {
		t.Fatalf("parseReviewVerdict = %+v", v)
	}

	approved, err := parseReviewVerdict([]byte(`{"decision":"approve"}`))
	if err != nil {
		t.Fatalf("parseReviewVerdict approve: %v", err)
	}
	if !approved.Approved {
		t.Fatal("decision=approve did not parse as Approved=true")
	}
}

// TestReadArtifactsSymlinkEscape confirms a symlink resolving outside
// workRoot is reported as unreadable, never followed to leak the real
// target's content.
func TestReadArtifactsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(target, []byte("outside content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "escape.md")); err != nil {
		t.Fatal(err)
	}

	out := ReadArtifacts(root, []string{"escape.md"})
	got := out["escape.md"]
	if strings.Contains(got, "outside content") {
		t.Fatalf("ReadArtifacts leaked the symlink target's real content: %q", got)
	}
	if !strings.Contains(got, "not readable") {
		t.Fatalf("ReadArtifacts(symlink escape) = %q, want a [not readable: ...] marker", got)
	}
}

// gitRepo creates a temp git repo, commits the given files, returns
// its base commit hash and worktree path; the fixture
// TestBaselineDiffExcludesLockfilesAndBuildOutput and
// TestBaselineDiffCappedParameter both build on.
func gitRepo(t *testing.T, files map[string]string) (worktree, baseCommit string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...) //nolint:gosec // args are fixed test-fixture git subcommands, not user input
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("commit", "--allow-empty", "-q", "-m", "base")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output() //nolint:gosec // fixed test-fixture git subcommand, not user input
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(out))

	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", "changes")
	return dir, base
}

// TestBaselineDiffExcludesLockfilesAndBuildOutput pins the pathspec
// syntax against a real git binary: a lockfile and a build-output file
// change alongside a real source file, and only the source file's diff
// survives.
func TestBaselineDiffExcludesLockfilesAndBuildOutput(t *testing.T) {
	worktree, base := gitRepo(t, map[string]string{
		"src/a.ts":          "export const a = 1;\n",
		"package-lock.json": `{"lockfileVersion": 2}` + "\n",
		"dist/x.js":         "console.log('built');\n",
	})
	diff, err := BaselineDiff(context.Background(), worktree, base)
	if err != nil {
		t.Fatalf("BaselineDiff: %v", err)
	}
	if !strings.Contains(diff, "src/a.ts") {
		t.Fatalf("diff missing src/a.ts:\n%s", diff)
	}
	if strings.Contains(diff, "package-lock.json") {
		t.Fatalf("diff included excluded package-lock.json:\n%s", diff)
	}
	if strings.Contains(diff, "dist/x.js") {
		t.Fatalf("diff included excluded dist/x.js:\n%s", diff)
	}
	if !strings.Contains(diff, baselineDiffExcludeTrailer) {
		t.Fatalf("diff missing exclusion trailer:\n%s", diff)
	}
}

// TestBaselineDiffCappedParameter confirms baselineDiffCapped honors a
// smaller cap than the package default, truncating accordingly.
func TestBaselineDiffCappedParameter(t *testing.T) {
	worktree, base := gitRepo(t, map[string]string{
		"big.txt": strings.Repeat("line of content here\n", 1000),
	})
	diff, err := baselineDiffCapped(context.Background(), worktree, base, 200)
	if err != nil {
		t.Fatalf("baselineDiffCapped: %v", err)
	}
	if !strings.Contains(diff, "truncated at") {
		t.Fatalf("diff not truncated with a small cap:\n%s", diff)
	}
	if len(diff) > 200+200 { // truncation marker + trailer add bounded overhead
		t.Fatalf("diff length %d far exceeds the 200-byte cap", len(diff))
	}
}

// TestReviewRoundSummary confirms the compact round summary carries
// finding titles/files/details and the verdict, never diff/artifact text.
func TestReviewRoundSummary(t *testing.T) {
	packet := ReviewPacket{
		UnitTitle: "Write the report",
		Diff:      "+this line must never appear in the summary",
		Artifacts: map[string]string{"report.md": "artifact body must never appear either"},
	}
	verdict := ReviewVerdict{
		Approved: false,
		Findings: []Finding{
			{Title: "missing citation", File: "report.md", Detail: "claim on line 4 has no source"},
		},
	}
	summary := reviewRoundSummary(packet, verdict)
	if !strings.Contains(summary, "Write the report") {
		t.Fatalf("summary missing unit title: %q", summary)
	}
	if !strings.Contains(summary, "rework") {
		t.Fatalf("summary missing verdict: %q", summary)
	}
	if !strings.Contains(summary, "missing citation") || !strings.Contains(summary, "report.md") || !strings.Contains(summary, "claim on line 4 has no source") {
		t.Fatalf("summary missing finding detail: %q", summary)
	}
	if strings.Contains(summary, "must never appear") {
		t.Fatalf("summary leaked diff/artifact content: %q", summary)
	}
}

func TestLastNewlineBefore(t *testing.T) {
	// "line one\nline two\nline three" — newlines at indices 8 and 17.
	b := []byte("line one\nline two\nline three")
	if got := lastNewlineBefore(b, 100); got != 17 {
		t.Fatalf("lastNewlineBefore = %d, want 17 (last newline, limit clamped to len)", got)
	}
	if got := lastNewlineBefore(b, 12); got != 8 {
		t.Fatalf("lastNewlineBefore(limit=12) = %d, want 8 (last newline at or before 12)", got)
	}
	noNewline := []byte("nonewlinehere")
	if got := lastNewlineBefore(noNewline, 5); got != 5 {
		t.Fatalf("lastNewlineBefore with no newline = %d, want fallback to limit 5", got)
	}
}
