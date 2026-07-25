package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFile(t *testing.T) {
	root := t.TempDir()
	tool := WriteFile(WriteFileConfig{Root: root})
	call := func(path, content string) (string, error) {
		args, _ := json.Marshal(map[string]string{"path": path, "content": content})
		return tool.Execute(context.Background(), args)
	}

	t.Run("writes workspace-relative file", func(t *testing.T) {
		out, err := call("summary.md", "# 429\n\nToo Many Requests.")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !strings.Contains(out, "summary.md") {
			t.Fatalf("out = %q", out)
		}
		b, err := os.ReadFile(filepath.Join(root, "summary.md")) //nolint:gosec // test reads from its own TempDir
		if err != nil || string(b) != "# 429\n\nToo Many Requests." {
			t.Fatalf("file = %q, err %v", b, err)
		}
	})

	t.Run("creates parent directories", func(t *testing.T) {
		if _, err := call("reports/deep/note.md", "hi"); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "reports", "deep", "note.md")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("overwrites existing content", func(t *testing.T) {
		if _, err := call("summary.md", "v2"); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		b, _ := os.ReadFile(filepath.Join(root, "summary.md")) //nolint:gosec // test reads from its own TempDir
		if string(b) != "v2" {
			t.Fatalf("file = %q, want overwritten", b)
		}
	})

	t.Run("rejects absolute path", func(t *testing.T) {
		if _, err := call("/etc/evil", "x"); err == nil {
			t.Fatal("absolute path accepted")
		}
	})

	t.Run("rejects escape via ..", func(t *testing.T) {
		if _, err := call("../outside.md", "x"); err == nil {
			t.Fatal(".. escape accepted")
		}
		if _, err := call("a/../../outside.md", "x"); err == nil {
			t.Fatal("nested .. escape accepted")
		}
	})

	t.Run("rejects oversized content", func(t *testing.T) {
		if _, err := call("big.md", strings.Repeat("x", writeFileMaxBytes+1)); err == nil {
			t.Fatal("oversized content accepted")
		}
	})

	t.Run("rejects empty path and unconfigured root", func(t *testing.T) {
		if _, err := call("", "x"); err == nil {
			t.Fatal("empty path accepted")
		}
		bare := WriteFile(WriteFileConfig{})
		args, _ := json.Marshal(map[string]string{"path": "a.md", "content": "x"})
		if _, err := bare.Execute(context.Background(), args); err == nil {
			t.Fatal("unconfigured root accepted")
		}
	})
}

// TestWriteFileRejectsSymlinkEscape covers the gap the lexical ".."
// check above can't see: a workspace-relative path that resolves
// cleanly on paper but walks through a symlinked ancestor pointing
// outside the root.
func TestWriteFileRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	tool := WriteFile(WriteFileConfig{Root: root})
	args, _ := json.Marshal(map[string]string{"path": "escape/evil.txt", "content": "x"})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("symlink escape accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); err == nil {
		t.Fatal("file was written outside the workspace")
	}
}

// TestWriteFileContentWithShellMetacharacters is the reason this tool
// exists: content full of $(), backticks, and redirects — which would
// classify a shell heredoc as opaque/destructive and park the turn —
// writes cleanly with no classification at all.
func TestWriteFileContentWithShellMetacharacters(t *testing.T) {
	root := t.TempDir()
	tool := WriteFile(WriteFileConfig{Root: root})
	content := "run `date` or $(curl -s example.com) > out.txt 2>&1"
	args, _ := json.Marshal(map[string]string{"path": "tricky.md", "content": content})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "tricky.md")) //nolint:gosec // test reads from its own TempDir
	if string(b) != content {
		t.Fatalf("file = %q, want metacharacters preserved verbatim", b)
	}
}
