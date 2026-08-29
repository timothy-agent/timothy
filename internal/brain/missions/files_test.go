package missions

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

func TestListFilesSkipsGit(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "real.txt"), "hello")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, ".git", "foo"), "internal git state")

	entries, truncated, err := ListFiles(root, nil)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if truncated {
		t.Fatal("truncated = true, want false")
	}
	if len(entries) != 1 || entries[0].Path != "real.txt" {
		t.Fatalf("entries = %+v, want exactly [real.txt]", entries)
	}
}

// TestListFilesSkipsGitFile covers a linked worktree, where .git at the
// root is a regular file (a pointer to the parent repo's
// .git/worktrees/<id>), not a directory.
func TestListFilesSkipsGitFile(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "real.txt"), "hello")
	mustWriteFile(t, filepath.Join(root, ".git"), "gitdir: /elsewhere/.git/worktrees/wt\n")

	entries, _, err := ListFiles(root, nil)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "real.txt" {
		t.Fatalf("entries = %+v, want exactly [real.txt]", entries)
	}
}

func TestListFilesDeclaredFlag(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "a")
	mustWriteFile(t, filepath.Join(root, "b.txt"), "b")
	declared := map[string]bool{"a.txt": true}

	entries, _, err := ListFiles(root, declared)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	byPath := map[string]FileEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if !byPath["a.txt"].Declared {
		t.Fatal("a.txt should be Declared=true")
	}
	if byPath["b.txt"].Declared {
		t.Fatal("b.txt should be Declared=false")
	}
}

func TestListFilesExcludesSymlinkToOutsideFile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	mustWriteFile(t, target, "outside content")
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "real.txt"), "inside")

	entries, _, err := ListFiles(root, nil)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	for _, e := range entries {
		if e.Path == "link.txt" {
			t.Fatal("symlink to an outside file must not appear in the listing")
		}
	}
	if len(entries) != 1 || entries[0].Path != "real.txt" {
		t.Fatalf("entries = %+v, want exactly [real.txt]", entries)
	}
}

func TestListFilesExcludesSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "nested.txt"), "outside dir content")
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "real.txt"), "inside")

	entries, _, err := ListFiles(root, nil)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	for _, e := range entries {
		if e.Path == "linkdir" || filepath.Dir(e.Path) == "linkdir" {
			t.Fatalf("nothing under a symlinked dir should appear, got %q", e.Path)
		}
	}
	if len(entries) != 1 || entries[0].Path != "real.txt" {
		t.Fatalf("entries = %+v, want exactly [real.txt]", entries)
	}
}

func TestListFilesCapAndTruncated(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxFileEntries+5; i++ {
		mustWriteFile(t, filepath.Join(root, fileName(i)), "x")
	}
	entries, truncated, err := ListFiles(root, nil)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if !truncated {
		t.Fatal("truncated = false, want true when entry count exceeds the cap")
	}
	if len(entries) != maxFileEntries {
		t.Fatalf("len(entries) = %d, want exactly %d", len(entries), maxFileEntries)
	}
}

func fileName(i int) string {
	return "f" + itoa(i) + ".txt"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestOpenFileRejectsEscape(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "ok.txt"), "fine")

	if _, _, err := OpenFile(root, "../../etc/passwd"); err == nil {
		t.Fatal("OpenFile with a ../ escape must fail")
	} else if !tools.IsViolation(err) {
		t.Fatalf("expected a containment violation, got: %v", err)
	}
}

func TestOpenFileRejectsAbsolutePath(t *testing.T) {
	root := t.TempDir()
	if _, _, err := OpenFile(root, "/etc/passwd"); err == nil {
		t.Fatal("OpenFile with an absolute path outside root must fail")
	}
}

func TestOpenFileRejectsSymlinkOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	mustWriteFile(t, target, "top secret")
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}

	if _, _, err := OpenFile(root, "link.txt"); err == nil {
		t.Fatal("OpenFile on a symlink pointing outside root must fail")
	} else if !tools.IsViolation(err) {
		t.Fatalf("expected a containment violation, got: %v", err)
	}
}

func TestOpenFileRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "subdir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenFile(root, "subdir"); err == nil {
		t.Fatal("OpenFile on a directory must fail")
	}
}

func TestOpenFileHappyPath(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "ok.txt"), "known content")

	f, fi, err := OpenFile(root, "ok.txt")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(b) != "known content" {
		t.Fatalf("content = %q, want %q", b, "known content")
	}
	if fi.Size() != int64(len("known content")) {
		t.Fatalf("Size = %d, want %d", fi.Size(), len("known content"))
	}
}

func TestWriteArchiveExactContents(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "content a")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "sub", "b.txt"), "content b")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, ".git", "internal"), "git state")
	// A worktree's .git is a plain file, not a directory — must still
	// be excluded (TestListFilesSkipsGitFile covers the ListFiles side).
	if err := os.MkdirAll(filepath.Join(root, "wt"), 0o750); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "wt", ".git"), "gitdir: /elsewhere\n")
	target := filepath.Join(outside, "secret.txt")
	mustWriteFile(t, target, "should not appear")
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteArchive(root, &buf); err != nil {
		t.Fatalf("WriteArchive: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	want := map[string]string{
		"a.txt":     "content a",
		"sub/b.txt": "content b",
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read zip entry %s: %v", f.Name, err)
		}
		got[f.Name] = string(b)
	}
	if len(got) != len(want) {
		t.Fatalf("zip contains %v, want exactly %v", got, want)
	}
	for name, content := range want {
		if got[name] != content {
			t.Fatalf("zip entry %s = %q, want %q", name, got[name], content)
		}
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestOrderedMarkdownPaths(t *testing.T) {
	entries := func(paths ...string) []FileEntry {
		out := make([]FileEntry, len(paths))
		for i, p := range paths {
			out[i] = FileEntry{Path: p}
		}
		return out
	}

	tests := []struct {
		name  string
		input []FileEntry
		want  []string
	}{
		{
			name:  "non-markdown files excluded",
			input: entries("notes.md", "image.png", "data.json"),
			want:  []string{"notes.md"},
		},
		{
			name:  "mixed case extension matched",
			input: entries("a.MD", "b.Markdown", "c.txt"),
			want:  []string{"a.MD", "b.Markdown"},
		},
		{
			name:  "folders before files, case-insensitive within a level",
			input: entries("zebra.md", "docs/b.md", "Apple.md", "docs/a.md"),
			want:  []string{"docs/a.md", "docs/b.md", "Apple.md", "zebra.md"},
		},
		{
			name:  "nested dirs sort recursively, folders before files at each level",
			input: entries("b/y.md", "a/z.md", "a/sub/x.md", "a/a.md"),
			want:  []string{"a/sub/x.md", "a/a.md", "a/z.md", "b/y.md"},
		},
		{
			name:  "top-level README hoisted to front",
			input: entries("zebra.md", "README.md", "apple.md"),
			want:  []string{"README.md", "apple.md", "zebra.md"},
		},
		{
			name:  "top-level readme case-insensitive and .markdown extension",
			input: entries("b.md", "readme.markdown"),
			want:  []string{"readme.markdown", "b.md"},
		},
		{
			name:  "README in a subdirectory is not hoisted",
			input: entries("zebra.md", "docs/README.md", "apple.md"),
			want:  []string{"docs/README.md", "apple.md", "zebra.md"},
		},
		{
			name:  "no markdown files",
			input: entries("a.txt", "b.png"),
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := OrderedMarkdownPaths(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("OrderedMarkdownPaths() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("OrderedMarkdownPaths() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
