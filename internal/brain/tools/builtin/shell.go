package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	shellDefaultTimeout = 30 * time.Second
	shellMaxOutput      = 64 << 10
)

// ShellConfig fixes where and how long commands run. The workspace
// root is the only directory commands start in; the permission chain
// and path allowlist decide what they may touch.
type ShellConfig struct {
	WorkspaceRoot string
	Timeout       time.Duration // 0 = shellDefaultTimeout
}

type shellArgs struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// ShellMaxTimeout caps model-requested timeouts; the constraint clamp
// enforces it regardless of what the model sends.
const ShellMaxTimeout = 120 * time.Second

// ShellTimeoutClamp overrides an out-of-range timeout_seconds instead
// of trusting the model: above the cap clamps down, zero or negative
// falls back to the default.
func ShellTimeoutClamp() tools.Clamp {
	return func(raw json.RawMessage) (json.RawMessage, error) {
		var args shellArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		maxSec := int(ShellMaxTimeout / time.Second)
		if args.TimeoutSeconds > maxSec {
			args.TimeoutSeconds = maxSec
		}
		if args.TimeoutSeconds < 0 {
			args.TimeoutSeconds = 0
		}
		return json.Marshal(args)
	}
}

func Shell(cfg ShellConfig) *tools.Tool {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = shellDefaultTimeout
	}
	return &tools.Tool{
		Name: "shell",
		Description: `Runs a shell command in the workspace directory.

Use for file inspection, text processing, and running programs inside
the workspace. The command executes with /bin/sh -c, so pipes,
redirects, and globs work. Every command starts in the workspace root
— use absolute paths under it or paths relative to it.

Arguments:
- command (string, required): the command line to run, e.g.
  "ls -la reports/" or "grep -rn 'TODO' src/ | head -20".
- timeout_seconds (integer, optional): seconds before the command is
  killed. Default 30, maximum 120 (higher values are clamped).

Returns combined stdout and stderr. A non-zero exit is reported with
the exit status alongside whatever output the command produced.
Commands are killed after a timeout; output is capped (a note marks
the cut) — long results are still stored in full and retrievable.

Edge cases: interactive commands (editors, REPLs, anything reading
stdin) hang until the timeout — don't run them. Network access
depends on the host environment.

Example: {"command": "wc -l notes.md"} → "42 notes.md"`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {
					"type": "string",
					"description": "Command line executed with /bin/sh -c in the workspace root"
				},
				"timeout_seconds": {
					"type": "integer",
					"description": "Seconds before the command is killed (default 30, max 120)"
				}
			},
			"required": ["command"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args shellArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if args.Command == "" {
				return "", fmt.Errorf("command is empty")
			}
			if cfg.WorkspaceRoot == "" {
				return "", fmt.Errorf("shell tool is not configured with a workspace")
			}
			runTimeout := timeout
			if args.TimeoutSeconds > 0 {
				runTimeout = time.Duration(args.TimeoutSeconds) * time.Second
			}
			return runShell(ctx, cfg.WorkspaceRoot, runTimeout, args.Command)
		},
	}
}

func runShell(ctx context.Context, dir string, timeout time.Duration, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Running model-supplied commands is this tool's purpose; the
	// constraint middleware and permission chain gate what reaches
	// here (danger-classified commands always prompt).
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command) //nolint:gosec // see above
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &capWriter{w: &buf, max: shellMaxOutput}
	cmd.Stderr = cmd.Stdout
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	out := buf.String()
	if buf.Len() >= shellMaxOutput {
		out += "\n[output capped]"
	}

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return out, fmt.Errorf("command timed out after %s", timeout)
	case err != nil:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// A failing command is a result the model must see, not
			// an infrastructure fault: report status with the output.
			return fmt.Sprintf("%s\n(exit status %d)", out, exitErr.ExitCode()), nil
		}
		return out, fmt.Errorf("run command: %w", err)
	}
	return out, nil
}

// capWriter stops writing (silently succeeding) once max bytes are
// kept, so runaway commands can't balloon memory while still letting
// the process finish.
type capWriter struct {
	w   *bytes.Buffer
	max int
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := c.max - c.w.Len(); room > 0 {
		if len(p) > room {
			c.w.Write(p[:room])
		} else {
			c.w.Write(p)
		}
	}
	return len(p), nil
}
