package missions

import (
	"os"
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
