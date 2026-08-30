// Package sandboxd (this file) is the HTTP surface: brain's only way
// to reach the Docker daemon this service holds the socket for. Every
// route validates its mission id/workdir in Go code before touching
// Docker — never pass an unvalidated value through, no allowlist
// fallthrough (a remote server must reject the unrecognized, not
// silently let it pass).
package sandboxd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
)

const (
	// execMinTimeout / execMaxTimeout clamp a client-supplied
	// timeout_seconds server-side — never trust the caller's number
	// unbounded, even though brain is the only caller today. 15m covers
	// verify_cmd's own ceiling (10m) with room to spare.
	execMinTimeout = 1 * time.Second
	execMaxTimeout = 15 * time.Minute

	// execBodyLimit bounds the exec request body; the request has
	// exactly three small fields, so 1 MiB is already generous.
	execBodyLimit = 1 << 20

	// defaultMaxExecs / defaultMaxContainers are the concurrency caps
	// applied when the operator sets neither SANDBOXD_MAX_EXECS nor
	// SANDBOXD_MAX_CONTAINERS — generous relative to today's realistic
	// worst case (4 concurrently-working missions × 4 parallel tool
	// calls each = 16) while still bounding the blast radius of a
	// runaway caller.
	defaultMaxExecs      = 32
	defaultMaxContainers = 32
)

// missionIDPattern matches exactly the UUID gen_random_uuid() produces
// (migrations/0001_init.sql), checked before any Docker call, so
// no mission-id-shaped input ever reaches a container name.
var missionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// D-053: per-exec env is deny-by-default — only names a detached
// executor (headless claude CLI) actually needs ever reach the
// container. Container-level Env (createContainer) stays PATH+HOME
// only; this allowlist governs solely the per-exec addition.
var execEnvAllowlist = map[string]bool{
	"ANTHROPIC_API_KEY":       true,
	"ANTHROPIC_AUTH_TOKEN":    true,
	"ANTHROPIC_BASE_URL":      true,
	"ANTHROPIC_MODEL":         true,
	"CLAUDE_CODE_OAUTH_TOKEN": true,
	"NO_COLOR":                true,
	"TERM":                    true,
	// NODE_OPTIONS (D-056): bounds the claude/pi CLI's node heap so a
	// long run's transcript doesn't balloon toward the sandbox's 2 GiB
	// cap — set unconditionally by the adapter, not a credential.
	"NODE_OPTIONS": true,
	// codex adapter env (executor/codex.go): CODEX_API_KEY is the
	// credential config.toml's env_key points at; CODEX_HOME pins
	// codex's state/config dir inside the run dir.
	"CODEX_API_KEY": true,
	"CODEX_HOME":    true,
	// pi adapter env (executor/pi.go): PI_API_KEY is the credential;
	// the rest pin pi's agent-state dir and disable network/telemetry
	// calls the sandbox has no route to make anyway.
	"PI_API_KEY":            true,
	"PI_CODING_AGENT_DIR":   true,
	"PI_OFFLINE":            true,
	"PI_SKIP_VERSION_CHECK": true,
	"PI_TELEMETRY":          true,
	// opencode adapter env (executor/opencode.go): OPENCODE_API_KEY is
	// the credential the config file's "{env:OPENCODE_API_KEY}"
	// template resolves; OPENCODE_CONFIG points opencode at the config
	// file the adapter wrote into the run dir.
	"OPENCODE_API_KEY": true,
	"OPENCODE_CONFIG":  true,
	// cursor adapter env (executor/cursor.go): CURSOR_API_KEY is the
	// subscription credential; CURSOR_CONFIG_DIR pins cursor-agent's
	// state/config dir inside the run dir; AGENT_CLI_CREDENTIAL_STORE
	// forces file-based credential storage for the containerized run.
	"CURSOR_API_KEY":             true,
	"CURSOR_CONFIG_DIR":          true,
	"AGENT_CLI_CREDENTIAL_STORE": true,
}

// execEnvMaxValueLen bounds a single env value — generous for a token
// or URL, small enough that this can never become a payload smuggling
// channel.
const execEnvMaxValueLen = 4096

// validExecEnv rejects any name outside execEnvAllowlist or any value
// over execEnvMaxValueLen, returning the offending name only — the
// value itself must never be logged or echoed back to the caller.
func validExecEnv(env map[string]string) (badName string, ok bool) {
	for name, value := range env {
		if !execEnvAllowlist[name] {
			return name, false
		}
		if len(value) > execEnvMaxValueLen {
			return name, false
		}
	}
	return "", true
}

// Config bounds the two things this service must cap regardless of
// what a caller asks for: concurrent execs (a slow/hung command must
// not let an unbounded number pile up) and live containers (a runaway
// caller must not be able to exhaust the host). Zero fields fall back
// to the package defaults.
type Config struct {
	MaxExecs      int
	MaxContainers int
}

// API serves sandboxd's routes.
type API struct {
	mgr *Manager
	log *slog.Logger

	execSem       chan struct{}
	maxContainers int
}

// Register mounts sandboxd's routes on the shared server.
func Register(s *httpserver.Server, mgr *Manager, cfg Config, log *slog.Logger) {
	maxExecs := cfg.MaxExecs
	if maxExecs <= 0 {
		maxExecs = defaultMaxExecs
	}
	maxContainers := cfg.MaxContainers
	if maxContainers <= 0 {
		maxContainers = defaultMaxContainers
	}
	a := &API{
		mgr:           mgr,
		log:           log,
		execSem:       make(chan struct{}, maxExecs),
		maxContainers: maxContainers,
	}
	s.Handle("POST /v1/sandboxes/{missionID}/exec", http.HandlerFunc(a.handleExec))
	s.Handle("DELETE /v1/sandboxes/{missionID}", http.HandlerFunc(a.handleRemove))
	s.Handle("GET /v1/sandboxes", http.HandlerFunc(a.handleList))
	s.Handle("GET /capacity", http.HandlerFunc(a.handleCapacity))
}

func jsonError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}

// validMissionID rejects anything that is not exactly a gen_random_uuid()
// shape — including image-name-shaped or path-traversal-shaped input —
// before it ever reaches containerName/Docker.
func validMissionID(id string) bool {
	return missionIDPattern.MatchString(id)
}

// validWorkdir is hygiene, not a security boundary: the sandbox mounts
// the whole workspace volume, so the boundary actually being defended
// is the host/daemon (missionID validation, container naming), not
// mission-vs-mission file access. Still, an absolute, clean path under
// /workspace is the only shape a legitimate caller ever sends.
func validWorkdir(w string) bool {
	if w != "/workspace" && !strings.HasPrefix(w, "/workspace/") {
		return false
	}
	return path.Clean(w) == w
}

type execRequest struct {
	Workdir        string            `json:"workdir"`
	Command        string            `json:"command"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Env            map[string]string `json:"env,omitempty"`
	// Environment selects the mission's sandbox image (D-05x) — a key
	// into manager.go's environmentKeys allowlist, never a free-form
	// image string. "" and "base" both mean the configured base image.
	// Only matters on a mission's first exec (image is fixed once its
	// container is created); a later exec sends whatever the mission
	// row still carries, which validExecEnvironment accepts even if it
	// no longer matches the running container — ensureContainer never
	// re-resolves the image for an existing container.
	Environment string `json:"environment,omitempty"`
}

// validExecEnvironment rejects any environment key outside
// manager.go's environmentKeys allowlist (plus the "" and "base"
// aliases for the base image) — same shape as validExecEnv's D-053
// allowlist check, checked before the exec ever reaches Docker so an
// unrecognized key is a clean 400, not a mid-stream infra error.
func validExecEnvironment(environment string) bool {
	switch environment {
	case "", "base":
		return true
	default:
		return environmentKeys[environment]
	}
}

// handleExec streams command's combined stdout+stderr as SSE. Pre-stream
// failures (bad request, at-capacity, ensure failure) are plain JSON
// errors; once headers are written the response commits to the SSE
// contract and every subsequent failure becomes a terminal `event:
// error`.
func (a *API) handleExec(w http.ResponseWriter, r *http.Request) {
	missionID := r.PathValue("missionID")
	if !validMissionID(missionID) {
		jsonError(w, http.StatusBadRequest, "bad_request", "invalid mission id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, execBodyLimit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req execRequest
	if err := dec.Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if !validWorkdir(req.Workdir) {
		jsonError(w, http.StatusBadRequest, "bad_request", "workdir must be an absolute, clean path under /workspace")
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "command is required")
		return
	}
	if badName, ok := validExecEnv(req.Env); !ok {
		jsonError(w, http.StatusBadRequest, "bad_request", "env var not allowed: "+badName)
		return
	}
	if !validExecEnvironment(req.Environment) {
		jsonError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("unknown environment %q", req.Environment))
		return
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout < execMinTimeout {
		timeout = execMinTimeout
	}
	if timeout > execMaxTimeout {
		timeout = execMaxTimeout
	}

	select {
	case a.execSem <- struct{}{}:
		defer func() { <-a.execSem }()
	default:
		jsonError(w, http.StatusTooManyRequests, "at_capacity", "too many concurrent execs")
		return
	}

	if n, err := a.liveContainers(r.Context()); err != nil {
		jsonError(w, http.StatusServiceUnavailable, "infra", err.Error())
		return
	} else if n >= a.maxContainers {
		jsonError(w, http.StatusServiceUnavailable, "at_capacity", "too many live sandbox containers")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer cannot flush")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctrl := http.NewResponseController(w)
	out := &sseOutputWriter{w: w, flusher: flusher, ctrl: ctrl}

	exitCode, err := a.mgr.ExecEnv(r.Context(), missionID, req.Environment, req.Workdir, req.Command, req.Env, timeout, out)
	switch {
	case err != nil && errors.Is(err, ErrTimeout):
		writeEvent(w, flusher, "error", map[string]any{
			"code": "timeout", "exit_code": 124, "message": err.Error(),
		})
	case err != nil:
		writeEvent(w, flusher, "error", map[string]any{
			"code": "infra", "message": err.Error(),
		})
	default:
		writeEvent(w, flusher, "exit", map[string]any{"exit_code": exitCode})
	}
}

// liveContainers counts sandbox containers currently known to Docker —
// the ensure-time cap that keeps a runaway caller from exhausting the
// host, distinct from execSem's cap on concurrent Exec calls.
func (a *API) liveContainers(ctx context.Context) (int, error) {
	ids, err := a.mgr.List(ctx)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

// sseOutputWriter adapts Manager.Exec's io.Writer contract (arbitrary
// binary chunks) onto the SSE wire format: one `event: output` /
// `data: <base64>` per Write, flushed immediately so the client sees
// output as it happens, refreshing the write deadline each time so a
// stalled client (not a stalled command) is what times this out.
type sseOutputWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	ctrl    *http.ResponseController
}

func (o *sseOutputWriter) Write(p []byte) (int, error) {
	_ = o.ctrl.SetWriteDeadline(time.Now().Add(30 * time.Second))
	encoded := base64.StdEncoding.EncodeToString(p)
	if _, err := fmt.Fprintf(o.w, "event: output\ndata: %s\n\n", encoded); err != nil {
		return 0, err
	}
	o.flusher.Flush()
	return len(p), nil
}

// writeEvent emits one terminal SSE event (exit or error) as a single
// JSON data line.
func writeEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	flusher.Flush()
}

// handleRemove force-removes missionID's sandbox container. Idempotent:
// a mission with no container (already removed, or never created)
// reports 204 the same as a successful removal — matching Manager.Remove's
// own not-found-is-fine contract.
func (a *API) handleRemove(w http.ResponseWriter, r *http.Request) {
	missionID := r.PathValue("missionID")
	if !validMissionID(missionID) {
		jsonError(w, http.StatusBadRequest, "bad_request", "invalid mission id")
		return
	}
	if err := a.mgr.Remove(r.Context(), missionID); err != nil {
		a.log.Warn("sandbox remove failed", "mission_id", missionID, "error", err)
		jsonError(w, http.StatusBadGateway, "infra", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type listResponse struct {
	MissionIDs []string `json:"mission_ids"`
}

// handleList reports the mission id of every sandbox container that
// currently exists — brain's sweep uses this to decide what is safe to
// remove; this service holds no Postgres state to make that decision
// itself.
func (a *API) handleList(w http.ResponseWriter, r *http.Request) {
	ids, err := a.mgr.List(r.Context())
	if err != nil {
		a.log.Warn("sandbox list failed", "error", err)
		jsonError(w, http.StatusBadGateway, "infra", err.Error())
		return
	}
	if ids == nil {
		ids = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(listResponse{MissionIDs: ids})
}

type capacityResponse struct {
	Admit            bool   `json:"admit"`
	MemAvailableMB   int    `json:"mem_available_mb"`
	RunningSandboxes int    `json:"running_sandboxes"`
	Reason           string `json:"reason,omitempty"`
}

// handleCapacity reports whether the host can afford one more working
// mission (D-056) — brain's admission gate consults this before flipping
// a mission idle->working.
func (a *API) handleCapacity(w http.ResponseWriter, r *http.Request) {
	report, err := a.mgr.Capacity(r.Context())
	if err != nil {
		a.log.Warn("sandbox capacity failed", "error", err)
		jsonError(w, http.StatusBadGateway, "infra", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(capacityResponse(report))
}
