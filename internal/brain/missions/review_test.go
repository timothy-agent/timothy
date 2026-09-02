package missions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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

// TestParseReviewVerdictSeverityAndResolved pins the D-092 schema
// additions: severity defaults to blocking when absent or unknown,
// minor is kept, and resolved ids pass through in order.
func TestParseReviewVerdictSeverityAndResolved(t *testing.T) {
	v, err := parseReviewVerdict([]byte(`{"decision":"rework","resolved":["F2","F1"],"findings":[
		{"title":"no severity"},
		{"title":"minor one","severity":"minor"},
		{"title":"unknown severity","severity":"critical"}
	]}`))
	if err != nil {
		t.Fatalf("parseReviewVerdict: %v", err)
	}
	if got := []string{v.Findings[0].Severity, v.Findings[1].Severity, v.Findings[2].Severity}; got[0] != SeverityBlocking || got[1] != SeverityMinor || got[2] != SeverityBlocking {
		t.Fatalf("severities = %v, want [blocking minor blocking]", got)
	}
	if len(v.Resolved) != 2 || v.Resolved[0] != "F2" || v.Resolved[1] != "F1" {
		t.Fatalf("Resolved = %v, want [F2 F1]", v.Resolved)
	}
	if !v.Findings[0].Blocking() || v.Findings[1].Blocking() {
		t.Fatal("Blocking() disagrees with the parsed severities")
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
	diff, err := BaselineDiff(context.Background(), worktree, base, nil)
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
	diff, err := baselineDiffCapped(context.Background(), worktree, base, nil, 200)
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

// TestBaselineDiffRestrictedToScope pins D-095 against a real git
// binary: the reviewer's diff covers only the scope paths, while the
// stat and the changed-file list cover the whole change.
func TestBaselineDiffRestrictedToScope(t *testing.T) {
	worktree, base := gitRepo(t, map[string]string{
		"src/a.ts":  "export const a = 1;\n",
		"docs/b.md": "# b\n",
		"report.md": "# report\n",
		"README.md": "readme\n",
	})
	ctx := context.Background()
	diff, err := BaselineDiff(ctx, worktree, base, []string{"src", "report.md"})
	if err != nil {
		t.Fatalf("BaselineDiff: %v", err)
	}
	for _, want := range []string{"src/a.ts", "report.md"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("scoped diff missing %s:\n%s", want, diff)
		}
	}
	for _, unwanted := range []string{"docs/b.md", "README.md"} {
		if strings.Contains(diff, unwanted) {
			t.Fatalf("scoped diff includes out-of-scope %s:\n%s", unwanted, diff)
		}
	}
	stat, err := DiffStat(ctx, worktree, base)
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	for _, want := range []string{"src/a.ts", "docs/b.md", "README.md", "4 files changed"} {
		if !strings.Contains(stat, want) {
			t.Fatalf("stat missing %q:\n%s", want, stat)
		}
	}
	files, err := ChangedFiles(ctx, worktree, base)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if len(files) != 4 {
		t.Fatalf("ChangedFiles = %v, want 4 paths", files)
	}
}

// TestTruncateDiffCutsOnFileBoundary pins the D-095 truncation rule:
// whole files are dropped from the end with a count marker; a first
// file larger than the budget keeps its head, cut on a line.
func TestTruncateDiffCutsOnFileBoundary(t *testing.T) {
	a := "diff --git a/a b/a\n" + strings.Repeat("+aaaa\n", 20)
	b := "diff --git a/b b/b\n" + strings.Repeat("+bbbb\n", 20)
	c := "diff --git a/c b/c\n+c\n"
	full := a + b + c

	if got := truncateDiff(full, len(full)); got != full {
		t.Fatalf("diff within budget was altered:\n%s", got)
	}
	got := truncateDiff(full, len(a)+len(b)-2)
	if !strings.Contains(got, "a/a") || strings.Contains(got, "a/b") || strings.Contains(got, "a/c") {
		t.Fatalf("truncated diff should keep only the first file:\n%s", got)
	}
	if !strings.HasSuffix(got, "[diff truncated: 2 files omitted]") {
		t.Fatalf("truncated diff missing the omitted-files marker:\n%s", got)
	}
	got = truncateDiff(full, 40)
	if !strings.Contains(got, "diff --git a/a b/a\n+aaaa\n") || !strings.Contains(got, "[truncated at ") || !strings.HasSuffix(got, "[diff truncated: 2 files omitted]") {
		t.Fatalf("oversized first file should keep its head with both markers:\n%s", got)
	}
	if strings.Contains(got, "+bbbb") {
		t.Fatalf("oversized first file cut must not leak the next file:\n%s", got)
	}
}

// TestGateFindings is the D-095 evidence gate table: a blocking finding
// survives only with a known file and non-empty evidence.
func TestGateFindings(t *testing.T) {
	known := []string{"./src/a.ts", "report.md"}
	tests := []struct {
		name       string
		finding    Finding
		wantMinor  bool
		wantReason string
	}{
		{"known file with evidence stays blocking", Finding{Title: "x", File: "src/a.ts", Evidence: "+const a = 1"}, false, ""},
		{"no file", Finding{Title: "x", Evidence: "+const a = 1"}, true, "names no file"},
		{"file outside the change", Finding{Title: "x", File: "src/zzz.ts", Evidence: "+z"}, true, "not in the diff"},
		{"no evidence", Finding{Title: "x", File: "report.md"}, true, "quotes no evidence"},
		{"blank evidence", Finding{Title: "x", File: "report.md", Evidence: "  "}, true, "quotes no evidence"},
		{"minor untouched", Finding{Title: "x", Severity: SeverityMinor}, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gated, demoted := gateFindings([]Finding{tc.finding}, known)
			if got := !gated[0].Blocking(); got != tc.wantMinor {
				t.Fatalf("minor = %v, want %v", got, tc.wantMinor)
			}
			if tc.wantReason == "" {
				if len(demoted) != 0 {
					t.Fatalf("demoted = %+v, want none", demoted)
				}
				return
			}
			if len(demoted) != 1 || !strings.Contains(demoted[0].Reason, tc.wantReason) {
				t.Fatalf("demoted = %+v, want one with reason containing %q", demoted, tc.wantReason)
			}
		})
	}
	in := []Finding{{Title: "x"}}
	gateFindings(in, nil)
	if in[0].Severity == SeverityMinor {
		t.Fatal("gateFindings mutated its input")
	}
}

// TestBlockingRemain pins the approve-after-gate rule: only-minor new
// findings approve unless a prior open blocking finding was left
// unresolved.
func TestBlockingRemain(t *testing.T) {
	minor := []Finding{{Title: "nit", Severity: SeverityMinor}}
	open := []Finding{{ID: "F1", Severity: SeverityBlocking, Status: FindingOpen}}
	if blockingRemain(minor, nil, nil) {
		t.Fatal("minor-only findings with no prior open ones must not block")
	}
	if !blockingRemain(minor, open, nil) {
		t.Fatal("an unresolved prior blocking finding must block")
	}
	if blockingRemain(minor, open, []string{"F1"}) {
		t.Fatal("a resolved prior blocking finding must not block")
	}
	if !blockingRemain([]Finding{{Title: "real", File: "a", Evidence: "b"}}, nil, nil) {
		t.Fatal("a blocking finding must block")
	}
}

// TestReviewArtifacts pins which artifacts the D-095 packet carries:
// all of them for legacy units or diff-less missions, otherwise only
// those a criterion names.
func TestReviewArtifacts(t *testing.T) {
	units := []PlanUnit{
		{Artifacts: []string{"docs/summary.md", "src/a.ts"}, Criteria: []string{"Summary.md lists every endpoint", "tests pass"}},
		{Artifacts: []string{"src/b.ts"}, Criteria: []string{"b exports the client"}},
	}
	if got := reviewArtifacts(units, true); len(got) != 1 || got[0] != "docs/summary.md" {
		t.Fatalf("reviewArtifacts(with diff) = %v, want only the criteria-named summary", got)
	}
	if got := reviewArtifacts(units, false); len(got) != 3 {
		t.Fatalf("reviewArtifacts(no diff) = %v, want every artifact", got)
	}
	legacy := []PlanUnit{{Artifacts: []string{"out.md", "out.md"}}}
	if got := reviewArtifacts(legacy, true); len(got) != 1 || got[0] != "out.md" {
		t.Fatalf("reviewArtifacts(legacy) = %v, want the deduplicated artifact", got)
	}
}

// TestReadArtifactsPerFileCap pins the 8 KB per-file cap (D-095) on
// top of the total cap.
func TestReadArtifactsPerFileCap(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.md"), []byte(strings.Repeat("x", reviewArtifactFileCap+100)), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ReadArtifacts(root, []string{"big.md"})["big.md"]
	if !strings.Contains(got, "per-file cap") || len(got) > reviewArtifactFileCap+200 {
		t.Fatalf("big artifact not capped per file (len %d)", len(got))
	}
}

// TestParseReviewVerdictEvidence pins that a finding's evidence line
// survives parsing.
// TestFindingFiles pins the D-096 helper: distinct finding files in
// first-seen order, normalized for duplicates, blanks skipped.
func TestFindingFiles(t *testing.T) {
	got := findingFiles([]Finding{
		{File: "src/x.go"}, {File: ""}, {File: "./src/x.go"}, {File: "docs/a.md"}, {File: "  "},
	})
	if !reflect.DeepEqual(got, []string{"src/x.go", "docs/a.md"}) {
		t.Fatalf("findingFiles = %v, want [src/x.go docs/a.md]", got)
	}
}

// TestOutsideScope pins the scope-creep rule (D-096): a file equal to or
// beneath a scope entry is in scope; an empty scope (legacy plan) puts
// nothing outside; a directory prefix must match on a path boundary.
func TestOutsideScope(t *testing.T) {
	files := []string{"src/main.go", "src/util/x.go", "srcs/y.go", "docs/notes.md", "README.md"}
	got := outsideScope(files, []string{"src", "README.md"})
	if !reflect.DeepEqual(got, []string{"srcs/y.go", "docs/notes.md"}) {
		t.Fatalf("outsideScope = %v, want [srcs/y.go docs/notes.md]", got)
	}
	if got := outsideScope(files, nil); got != nil {
		t.Fatalf("empty scope must put nothing outside, got %v", got)
	}
	if got := outsideScope(files, []string{"./src/", "docs", "README.md", "srcs"}); got != nil {
		t.Fatalf("every file in scope, got %v", got)
	}
}

func TestParseReviewVerdictEvidence(t *testing.T) {
	v, err := parseReviewVerdict([]byte(`{"decision":"rework","findings":[{"title":"x","file":"a.go","evidence":"+return nil"}]}`))
	if err != nil {
		t.Fatalf("parseReviewVerdict: %v", err)
	}
	if v.Findings[0].Evidence != "+return nil" {
		t.Fatalf("Evidence = %q", v.Findings[0].Evidence)
	}
}

// TestReviewByteBudget pins D-097's derivation: window x 3.5 bytes per
// token minus the fixed margin, with 128k tokens standing in for an
// unknown window.
func TestReviewByteBudget(t *testing.T) {
	tests := []struct {
		name   string
		window int
		want   int
	}{
		{"known 200k window", 200_000, 700_000 - reviewByteMargin},
		{"unknown window falls back to 128k", 0, 448_000 - reviewByteMargin},
		{"negative window is unknown too", -1, 448_000 - reviewByteMargin},
		{"small window keeps the same margin", 32_000, 112_000 - reviewByteMargin},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewByteBudget(tc.window); got != tc.want {
				t.Fatalf("reviewByteBudget(%d) = %d, want %d", tc.window, got, tc.want)
			}
		})
	}
}

// TestFitReviewPacketShrinkOrder pins D-097's shrink order: the diff
// gives first (down to the floor), then the artifacts, then the diff
// again; criteria and findings are never touched; a packet within
// budget is returned as is.
func TestFitReviewPacketShrinkOrder(t *testing.T) {
	criteria := []string{"the report names every retry header", "the summary is under 200 words"}
	findings := []Finding{{ID: "F1", Title: "missing retry-after", File: "report.md", Status: FindingOpen, Detail: strings.Repeat("d", 300)}}
	// Two 40 KB files: 80 KB total, above the 64 KB floor, so the first
	// pass can drop the second file and no more.
	fileA := "diff --git a/a b/a\n" + strings.Repeat("+aaaa\n", 40<<10/6)
	fileB := "diff --git a/b b/b\n" + strings.Repeat("+bbbb\n", 40<<10/6)
	diff := fileA + fileB
	artifacts := map[string]string{"report.md": strings.Repeat("report line\n", 200), "notes.md": strings.Repeat("note line\n", 100)}
	base := ReviewPacket{
		Units:        []PlanUnit{{Title: "u1", Criteria: criteria, Artifacts: []string{"report.md"}}},
		Diff:         diff,
		Artifacts:    artifacts,
		OpenFindings: findings,
	}
	full := len(renderReviewContent(base))

	t.Run("within budget is untouched", func(t *testing.T) {
		got, r := fitReviewPacket(base, full)
		if r.cut() || got.Diff != diff || len(got.Artifacts["report.md"]) != len(artifacts["report.md"]) {
			t.Fatalf("packet within budget was altered: %+v", r)
		}
	})
	t.Run("diff shrinks first, artifacts untouched", func(t *testing.T) {
		budget := full - 1000
		got, r := fitReviewPacket(base, budget)
		if r.DiffCut == 0 || r.ArtifactsCut != 0 {
			t.Fatalf("shrink = %+v, want only the diff cut", r)
		}
		if got.Artifacts["report.md"] != artifacts["report.md"] {
			t.Fatal("artifacts were cut while the diff still had room")
		}
		if !strings.Contains(got.Diff, "[diff truncated: 1 files omitted]") || strings.Contains(got.Diff, "+bbbb") {
			t.Fatalf("diff was not cut on the file boundary:\n%s", got.Diff[len(got.Diff)-120:])
		}
		if rendered := len(renderReviewContent(got)); rendered > budget {
			t.Fatalf("rendered %d bytes, want <= %d", rendered, budget)
		}
	})
	t.Run("artifacts shrink once the diff is at the floor", func(t *testing.T) {
		// More than the diff can give at the floor: the second file goes,
		// the first stays whole, and the rest comes out of the artifacts.
		budget := full - len(fileB) - 1000
		got, r := fitReviewPacket(base, budget)
		if r.DiffCut == 0 || r.ArtifactsCut == 0 {
			t.Fatalf("shrink = %+v, want both diff and artifacts cut", r)
		}
		if !strings.HasPrefix(got.Diff, fileA) {
			t.Fatal("first diff file was cut before the artifacts gave")
		}
		if !strings.Contains(got.Artifacts["notes.md"], "review byte budget reached") {
			t.Fatalf("cut artifact lacks its marker:\n%s", got.Artifacts["notes.md"])
		}
		if base.Artifacts["notes.md"] != artifacts["notes.md"] {
			t.Fatal("caller's artifact map was mutated")
		}
		if rendered := len(renderReviewContent(got)); rendered > budget {
			t.Fatalf("rendered %d bytes, want <= %d", rendered, budget)
		}
	})
	t.Run("diff goes below the floor only after the artifacts are gone", func(t *testing.T) {
		got, r := fitReviewPacket(base, 3000)
		if r.DiffCut == 0 || r.ArtifactsCut == 0 {
			t.Fatalf("shrink = %+v, want both cut", r)
		}
		if len(got.Diff) >= len(fileA) {
			t.Fatalf("diff = %d bytes, want cut below the first file's %d", len(got.Diff), len(fileA))
		}
	})
	t.Run("criteria and findings are never dropped", func(t *testing.T) {
		got, _ := fitReviewPacket(base, 10)
		if !reflect.DeepEqual(got.Units[0].Criteria, criteria) || !reflect.DeepEqual(got.OpenFindings, findings) {
			t.Fatalf("criteria/findings changed: %+v / %+v", got.Units[0].Criteria, got.OpenFindings)
		}
		rendered := renderReviewContent(got)
		for _, c := range criteria {
			if !strings.Contains(rendered, c) {
				t.Fatalf("criterion %q missing from the shrunk packet", c)
			}
		}
		if !strings.Contains(rendered, "F1") {
			t.Fatal("finding F1 missing from the shrunk packet")
		}
	})
}
