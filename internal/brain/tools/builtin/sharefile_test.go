package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// fakeSaver stubs tools.SaveFunc: every save gets a sequential id and
// echoes back a fixed mime, or errors when maxBytes is exceeded.
func fakeSaver(maxBytes int) tools.SaveFunc {
	n := 0
	return func(_ context.Context, r io.Reader) (string, string, error) {
		data, err := io.ReadAll(r)
		if err != nil {
			return "", "", err
		}
		if maxBytes > 0 && len(data) > maxBytes {
			return "", "", errors.New("file too large for its type")
		}
		n++
		return fmt.Sprintf("id-%d", n), "image/png", nil
	}
}

func TestShareFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "chart.png"), []byte("fake-png-bytes"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reports", "big.png"), bytes.Repeat([]byte("x"), 100), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.png"), []byte("nope"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	call := func(tool *tools.Tool, ctx context.Context, path, name string) (string, error) {
		args, _ := json.Marshal(shareFileArgs{Path: path, Name: name})
		return tool.Execute(ctx, args)
	}

	t.Run("happy path", func(t *testing.T) {
		tool := ShareFile(ShareFileConfig{Root: root})
		ctx := tools.WithCollector(context.Background(), tools.NewCollector(fakeSaver(0)))
		out, err := call(tool, ctx, "chart.png", "")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !strings.Contains(out, "id-1") || !strings.Contains(out, "image/png") {
			t.Fatalf("out = %q", out)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		tool := ShareFile(ShareFileConfig{Root: root})
		ctx := tools.WithCollector(context.Background(), tools.NewCollector(fakeSaver(0)))
		if _, err := call(tool, ctx, "nope.png", ""); err == nil {
			t.Fatal("missing file accepted")
		}
	})

	t.Run("oversize file", func(t *testing.T) {
		tool := ShareFile(ShareFileConfig{Root: root})
		ctx := tools.WithCollector(context.Background(), tools.NewCollector(fakeSaver(10)))
		if _, err := call(tool, ctx, "reports/big.png", ""); err == nil {
			t.Fatal("oversize file accepted")
		}
	})

	t.Run("rejects absolute path", func(t *testing.T) {
		tool := ShareFile(ShareFileConfig{Root: root})
		ctx := tools.WithCollector(context.Background(), tools.NewCollector(fakeSaver(0)))
		if _, err := call(tool, ctx, "/etc/passwd", ""); err == nil {
			t.Fatal("absolute path accepted")
		}
	})

	t.Run("rejects escape via ..", func(t *testing.T) {
		tool := ShareFile(ShareFileConfig{Root: root})
		ctx := tools.WithCollector(context.Background(), tools.NewCollector(fakeSaver(0)))
		if _, err := call(tool, ctx, "../outside.png", ""); err == nil {
			t.Fatal(".. escape accepted")
		}
	})

	t.Run("rejects symlink escape", func(t *testing.T) {
		tool := ShareFile(ShareFileConfig{Root: root})
		ctx := tools.WithCollector(context.Background(), tools.NewCollector(fakeSaver(0)))
		if _, err := call(tool, ctx, "escape/secret.png", ""); err == nil {
			t.Fatal("symlink escape accepted")
		}
	})

	t.Run("rejects empty path and unconfigured root", func(t *testing.T) {
		tool := ShareFile(ShareFileConfig{Root: root})
		ctx := tools.WithCollector(context.Background(), tools.NewCollector(fakeSaver(0)))
		if _, err := call(tool, ctx, "", ""); err == nil {
			t.Fatal("empty path accepted")
		}
		bare := ShareFile(ShareFileConfig{})
		if _, err := call(bare, ctx, "chart.png", ""); err == nil {
			t.Fatal("unconfigured root accepted")
		}
	})

	t.Run("no collector configured", func(t *testing.T) {
		tool := ShareFile(ShareFileConfig{Root: root})
		if _, err := call(tool, context.Background(), "chart.png", ""); err == nil {
			t.Fatal("missing collector accepted")
		}
	})
}
