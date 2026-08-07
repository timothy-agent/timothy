// Package sandboxclient is brain's client for sandboxd's internal API
// — the only way brain reaches per-mission sandbox containers now that
// the Docker socket lives in sandboxd, not brain. Exec matches the
// missions package's sandboxExec function type exactly, so
// cmd/brain/main.go wires *Client.Exec in wherever *sandbox.Manager.Exec
// used to go.
package sandboxclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/sse"
)

// Client talks to one sandboxd base URL.
type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{
		// No overall timeout: Exec streams for as long as the mission's
		// own timeout_seconds allows. Bound only the phase that can hang
		// before any byte arrives — sandboxd writes headers and flushes
		// before starting the exec, so a quiet command never trips this.
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}}
}

type execRequest struct {
	Workdir        string            `json:"workdir"`
	Command        string            `json:"command"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Env            map[string]string `json:"env,omitempty"`
	// Environment selects the mission's sandbox image (D-05x) — a key
	// into sandboxd's own environmentImages allowlist, never a
	// free-form image string. Only matters on the mission's first exec
	// (image is fixed once its container is created).
	Environment string `json:"environment,omitempty"`
}

// Exec matches the missions package's sandboxExec function type: runs
// command in missionID's sandbox container via sandboxd, streaming
// decoded output to out, and returns the exit code. err is non-nil only
// for infrastructure failures or a timeout — mirroring
// sandboxd.Manager.Exec's own contract (a command that ran and exited
// non-zero is reported via exitCode, not err).
func (c *Client) Exec(ctx context.Context, missionID, environment, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
	return c.ExecEnv(ctx, missionID, environment, workdir, command, nil, timeout, out)
}

// ExecEnv is Exec plus per-exec environment variables (D-053) —
// sandboxd validates env against its own allowlist server-side; this
// client passes it through unmodified. Existing Exec callers are
// unaffected: they route through here with env == nil, which the
// json:"omitempty" tag drops from the wire request entirely.
func (c *Client) ExecEnv(ctx context.Context, missionID, environment, workdir, command string, env map[string]string, timeout time.Duration, out io.Writer) (int, error) {
	body, err := json.Marshal(execRequest{
		Workdir: workdir, Command: command, TimeoutSeconds: int(timeout / time.Second), Env: env, Environment: environment,
	})
	if err != nil {
		return 0, fmt.Errorf("sandboxclient: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/sandboxes/"+missionID+"/exec", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("sandboxclient: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("sandboxclient: sandboxd unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("sandboxclient: sandboxd http %d: %s", resp.StatusCode, string(msg))
	}

	var (
		exitCode    int
		execErr     error
		sawTerminal bool
	)
	readErr := sse.Read(resp.Body, func(ev sse.Event) bool {
		switch ev.Name {
		case "output":
			decoded, err := base64.StdEncoding.DecodeString(ev.Data)
			if err != nil {
				execErr = fmt.Errorf("sandboxclient: decode output chunk: %w", err)
				sawTerminal = true
				return false
			}
			if _, err := out.Write(decoded); err != nil {
				execErr = fmt.Errorf("sandboxclient: write output: %w", err)
				sawTerminal = true
				return false
			}
			return true
		case "exit":
			var payload struct {
				ExitCode int `json:"exit_code"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
				execErr = fmt.Errorf("sandboxclient: decode exit event: %w", err)
			} else {
				exitCode = payload.ExitCode
			}
			sawTerminal = true
			return false
		case "error":
			var payload struct {
				Code     string `json:"code"`
				ExitCode int    `json:"exit_code"`
				Message  string `json:"message"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
				execErr = fmt.Errorf("sandboxclient: decode error event: %w", err)
			} else if payload.Code == "timeout" {
				exitCode = payload.ExitCode
				execErr = fmt.Errorf("%s", payload.Message)
			} else {
				execErr = fmt.Errorf("sandboxclient: %s", payload.Message)
			}
			sawTerminal = true
			return false
		default:
			return true
		}
	})
	if readErr != nil && !sawTerminal {
		return 0, fmt.Errorf("sandboxclient: stream read: %w", readErr)
	}
	if !sawTerminal {
		return 0, fmt.Errorf("sandboxclient: stream ended without a terminal event")
	}
	return exitCode, execErr
}

// Remove force-removes missionID's sandbox container via sandboxd.
// Idempotent — a mission with no container reports success the same as
// an actual removal.
func (c *Client) Remove(ctx context.Context, missionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v1/sandboxes/"+missionID, nil)
	if err != nil {
		return fmt.Errorf("sandboxclient: request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sandboxclient: sandboxd unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("sandboxclient: sandboxd http %d: %s", resp.StatusCode, string(msg))
	}
	return nil
}

// Sweep lists every sandbox container sandboxd knows about and removes
// each one isTerminal reports true for — the inversion of
// sandboxd.Manager.Sweep now that sandboxd holds no Postgres state to
// make that call itself. Continues past a single mission's removal
// failure so one bad container never blocks the rest; all failures
// join into the returned error.
func (c *Client) Sweep(ctx context.Context, isTerminal func(missionID string) bool) error {
	ids, err := c.list(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, missionID := range ids {
		if !isTerminal(missionID) {
			continue
		}
		if err := c.Remove(ctx, missionID); err != nil {
			errs = append(errs, fmt.Errorf("mission %s: %w", missionID, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Client) list(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/sandboxes", nil)
	if err != nil {
		return nil, fmt.Errorf("sandboxclient: request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sandboxclient: sandboxd unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("sandboxclient: sandboxd http %d: %s", resp.StatusCode, string(msg))
	}
	var out struct {
		MissionIDs []string `json:"mission_ids"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("sandboxclient: decode: %w", err)
	}
	return out.MissionIDs, nil
}

// Health reports whether sandboxd itself is reachable — the sandbox
// health check surfaced through brain's own /health.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return fmt.Errorf("sandboxclient: request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sandboxclient: sandboxd unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("sandboxclient: decode health: %w", err)
	}
	if body.Status != "ok" {
		return fmt.Errorf("sandboxclient: sandboxd degraded")
	}
	return nil
}
