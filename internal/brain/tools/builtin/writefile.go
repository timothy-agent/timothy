package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// writeFileMaxBytes bounds one write_file call — an artifact, not a
// datastore dump.
const writeFileMaxBytes = 1 << 20

// WriteFileConfig fixes the directory writes are confined to.
type WriteFileConfig struct {
	Root string
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteFile is the purpose-built alternative to writing files through
// shell redirects: `echo ... > f` and heredocs classify as destructive
// (or opaque, when the CONTENT happens to contain $() or backticks)
// and park the turn on a human prompt — while this tool is confined to
// its root by construction, so there is nothing to classify. Fewer
// escaping games for the model, no false-positive prompts, cheaper
// turns.
func WriteFile(cfg WriteFileConfig) *tools.Tool {
	return &tools.Tool{
		Name: "write_file",
		Description: `Writes a file in the workspace, creating parent directories as needed and replacing any existing content.

This is the ONLY correct way to create or update a file — do not use
shell redirects (>, >>) or heredocs, which require interactive
approval and often fail.

Arguments:
- path (string, required): workspace-relative file path, e.g.
  "summary.md" or "reports/http-429.md". Absolute paths and paths
  containing ".." are rejected.
- content (string, required): the complete file content to write.

Returns a confirmation with the byte count written.

Example: {"path": "summary.md", "content": "# Title\n\nBody."}`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Workspace-relative file path to write"
				},
				"content": {
					"type": "string",
					"description": "Complete file content"
				}
			},
			"required": ["path", "content"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args writeFileArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if cfg.Root == "" {
				return "", fmt.Errorf("write_file is not configured with a workspace")
			}
			if args.Path == "" {
				return "", fmt.Errorf("path is empty")
			}
			if len(args.Content) > writeFileMaxBytes {
				return "", fmt.Errorf("content is %d bytes, the limit is %d", len(args.Content), writeFileMaxBytes)
			}
			if filepath.IsAbs(args.Path) {
				return "", fmt.Errorf("path must be workspace-relative, got an absolute path")
			}
			cleaned := filepath.Clean(args.Path)
			if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("path escapes the workspace")
			}
			target := filepath.Join(cfg.Root, cleaned)
			if dir := filepath.Dir(target); dir != cfg.Root {
				if err := os.MkdirAll(dir, 0o750); err != nil {
					return "", fmt.Errorf("create parent directories: %w", err)
				}
			}
			// The lexical join above can't see a symlinked ancestor
			// inside the workspace pointing outside it; WithinRoot
			// resolves symlinks before the write lands.
			if err := tools.WithinRoot(cfg.Root, target); err != nil {
				return "", err
			}
			if err := os.WriteFile(target, []byte(args.Content), 0o644); err != nil { //nolint:gosec // artifact files are meant to be readable; path is root-confined above
				return "", fmt.Errorf("write file: %w", err)
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), cleaned), nil
		},
	}
}
