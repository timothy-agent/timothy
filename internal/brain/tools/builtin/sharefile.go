package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// ShareFileConfig fixes the directory share_file may read from.
type ShareFileConfig struct {
	Root string
}

type shareFileArgs struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// ShareFile reads a file from the workspace and publishes it to the
// user-facing transcript as generated media — the way an assistant
// turn hands back an image, document, or other file it produced, as
// opposed to write_file's plain confirmation text. The attachment
// store's own allowlist and per-type size caps are the only limits
// (tools.Collector.Emit -> the wired SaveFunc); a rejected type or an
// oversize file comes back as a clear tool error, nothing is silently
// dropped.
func ShareFile(cfg ShareFileConfig) *tools.Tool {
	return &tools.Tool{
		Name: "share_file",
		Description: `Publishes a file from the workspace to the user as generated media (an image, document, or other file the turn produced).

Arguments:
- path (string, required): workspace-relative file path, e.g.
  "chart.png" or "reports/summary.pdf". Absolute paths and paths
  containing ".." are rejected.
- name (string, optional): a display name shown to the user; defaults
  to the file's base name.

Returns a confirmation naming the stored id and detected mime type.
Rejected file types (e.g. HTML) come back as a clear error instead of
publishing anything.

Example: {"path": "chart.png"}`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Workspace-relative file path to share"
				},
				"name": {
					"type": "string",
					"description": "Optional display name; defaults to the file's base name"
				}
			},
			"required": ["path"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args shareFileArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if cfg.Root == "" {
				return "", fmt.Errorf("share_file is not configured with a workspace")
			}
			if args.Path == "" {
				return "", fmt.Errorf("path is empty")
			}
			if filepath.IsAbs(args.Path) {
				return "", fmt.Errorf("path must be workspace-relative, got an absolute path")
			}
			cleaned := filepath.Clean(args.Path)
			target := filepath.Join(cfg.Root, cleaned)
			if err := tools.WithinRoot(cfg.Root, target); err != nil {
				return "", err
			}
			f, err := os.Open(target) //nolint:gosec // G304: path is root-confined by WithinRoot above
			if err != nil {
				if os.IsNotExist(err) {
					return "", fmt.Errorf("no file at %q", cleaned)
				}
				return "", fmt.Errorf("open file: %w", err)
			}
			defer func() { _ = f.Close() }()

			name := args.Name
			if name == "" {
				name = filepath.Base(cleaned)
			}
			collector := tools.CollectorFrom(ctx)
			if collector == nil {
				return "", fmt.Errorf("media emission is not configured")
			}
			ref, err := collector.Emit(ctx, name, f)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("shared %q as %s (%s)", name, ref.ID, ref.Mime), nil
		},
	}
}
