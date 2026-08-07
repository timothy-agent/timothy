package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/secretstore"
)

// registerMissions mounts the mission surface: served locally,
// missions are brain's domain like agents and connectors. nil store
// leaves the surface unmounted (404s), matching the memories/admin/
// agents gating pattern. agentReg resolves a mission's route/
// review_route/budget/approval_allowlist defaults from the chosen
// agent when the create request omits them. routeForRole resolves the
// route bound to the "default" system role (D-049) — an agent's empty
// route means "the default chain," but the gateway's /v1/stream
// requires a real, non-empty route name.
func (a *API) registerMissions(handle func(pattern string, h http.Handler), store *missions.Store, driver *missions.Driver, notifier *missions.Notifier, agentReg *agents.Store, workspace *missions.Workspace, resolveSecret func(context.Context, string) (string, error), routeForRole func(context.Context, string) string, classify agents.Classify) {
	if store == nil {
		return
	}
	h := &missionAPI{store: store, driver: driver, notifier: notifier, agentReg: agentReg, workspace: workspace, resolveSecret: resolveSecret, routeForRole: routeForRole, classify: classify, perms: a.perms, dir: a.dir, log: a.log}
	handle("GET /v1/missions", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/missions", a.auth(http.HandlerFunc(h.create)))
	handle("POST /v1/missions/classify", a.auth(http.HandlerFunc(h.classifyGoal)))
	handle("GET /v1/missions/{id}", a.auth(http.HandlerFunc(h.get)))
	handle("DELETE /v1/missions/{id}", a.auth(http.HandlerFunc(h.delete)))
	handle("GET /v1/missions/{id}/events", a.auth(http.HandlerFunc(h.events)))
	handle("POST /v1/missions/{id}/resume", a.auth(http.HandlerFunc(h.resume)))
	handle("POST /v1/missions/{id}/cancel", a.auth(http.HandlerFunc(h.cancel)))
	handle("POST /v1/missions/{id}/permission", a.auth(http.HandlerFunc(h.permission)))
	handle("GET /v1/missions/{id}/files", a.auth(http.HandlerFunc(h.files)))
	handle("GET /v1/missions/{id}/files/{path...}", a.auth(http.HandlerFunc(h.download)))
	handle("GET /v1/missions/{id}/archive", a.auth(http.HandlerFunc(h.archive)))
	handle("POST /v1/missions/{id}/push", a.auth(http.HandlerFunc(h.push)))
	handle("GET /v1/notifications", a.auth(http.HandlerFunc(h.notifications)))
	handle("POST /v1/notifications/{id}/read", a.auth(http.HandlerFunc(h.markRead)))
}

type missionAPI struct {
	store    *missions.Store
	driver   *missions.Driver
	notifier *missions.Notifier
	agentReg *agents.Store
	// workspace performs mission-directory git operations (Push,
	// Teardown for delete) outside the normal Drive loop.
	workspace *missions.Workspace
	// resolveSecret resolves a credential_ref to its plaintext value for
	// push; nil means no secret store is configured.
	resolveSecret func(context.Context, string) (string, error)
	// routeForRole resolves the route bound to a system role ("default"
	// for missions); nil or an unbound role fall back to "" and the
	// gateway's own no_route error, same as an unconfigured route.
	routeForRole func(context.Context, string) string
	// classify resolves an omitted create request's kind from its goal
	// (classifyKind below) and backs the /v1/missions/classify preview
	// endpoint; nil (no gateway wiring) makes every omitted kind default
	// straight to "coding", same as any classify error.
	classify agents.Classify
	// perms answers a mission's pending_permission — the same
	// PermissionResolver chat sessions use (A.perms), never a
	// mission-specific broker.
	perms PermissionResolver
	// dir deletes a mission's hidden session on mission delete — the
	// same Directory the top-level session routes use (a.dir), not a
	// mission-specific store.
	dir Directory
	log *slog.Logger
}

func failMission(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, missions.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, missions.ErrBranchConflict):
		jsonError(w, http.StatusConflict, "branch_conflict", err.Error())
	case errors.Is(err, missions.ErrTerminal):
		jsonError(w, http.StatusConflict, "already_finished", err.Error())
	case errors.Is(err, missions.ErrNotTerminal):
		jsonError(w, http.StatusConflict, "not_terminal", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

// list serves GET /v1/missions, optionally narrowed by ?schedule_id=
// (a recurring schedule's fire history: every mission it spawned)
// and/or ?limit= (a positive result cap). Both are ignored when
// empty/absent — the original "every mission" behavior — and a
// malformed value is a 400 rather than a silently-empty filter.
func (h *missionAPI) list(w http.ResponseWriter, r *http.Request) {
	var filter missions.ListFilter
	if v := r.URL.Query().Get("schedule_id"); v != "" {
		if !validSessionID(v) {
			jsonError(w, http.StatusBadRequest, "bad_request", "schedule_id must be a UUID")
			return
		}
		filter.ScheduleID = v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			jsonError(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
		filter.Limit = n
	}
	rows, err := h.store.List(r.Context(), filter)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "missions_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"missions": rows})
}

type createMissionRequest struct {
	Goal string `json:"goal"`
	// Kind is optional: an empty value is classified from Goal (see
	// classifyKind) rather than rejected — the web UI's chip preview
	// resolves it before submit, but any other caller (a script, a
	// future integration) can still just send a goal and let the
	// server decide.
	Kind        string `json:"kind"`
	AgentID     string `json:"agent_id"`
	Route       string `json:"route"`
	ReviewRoute string `json:"review_route"`
	// EscalationRoute, when set, is where worker turns move after a
	// failure or rework. Empty keeps escalation off — never defaulted,
	// so a route change is always an explicit choice.
	EscalationRoute string   `json:"escalation_route"`
	MaxIterations   int      `json:"max_iterations"`
	BudgetAmount    *float64 `json:"budget_amount"`
	// BudgetCurrency is optional; an omitted value defaults to "USD"
	// directly in create() below. The web UI, not this handler, is
	// responsible for sending the settings page's configured default
	// currency when the user hasn't explicitly overridden it — this
	// keeps the handler simple and stateless w.r.t. settings.
	BudgetCurrency string `json:"budget_currency"`
	// AutoApproveSafe defaults true (a pointer so an omitted field is
	// distinguishable from an explicit false) — missions run for hours
	// unattended, so auto-approving DangerSafe shell calls is the
	// sensible default; destructive commands always ask regardless.
	AutoApproveSafe *bool `json:"auto_approve_safe"`
}

// create validates the request, resolves route/review_route/budget
// defaults from the chosen agent when the request omits them, and
// hands off to Driver.Create — provisioning and the mission's first
// turn happen in the background; this returns {id} immediately.
func (h *missionAPI) create(w http.ResponseWriter, r *http.Request) {
	var req createMissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Goal == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "goal is required")
		return
	}
	switch req.Kind {
	case "":
		req.Kind = classifyKind(r.Context(), h.classify, req.Goal)
	case "coding", "general":
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", `kind must be "coding" or "general"`)
		return
	}
	// Resolve even with an empty AgentID: ResolveByID("") falls back to
	// the default agent, same as chat sessions that don't pick one — a
	// mission created without an explicit agent still needs a real
	// route, not an empty string the gateway will reject. By id, not
	// name: req.AgentID is the mission row's agent_id FK value (the
	// picker sends a.id), unlike chat's session.agent which is a name.
	var promptOverlay string
	if h.agentReg != nil {
		if a, ok := h.agentReg.ResolveByID(r.Context(), req.AgentID); ok {
			if req.Route == "" {
				req.Route = a.Route
			}
			if req.ReviewRoute == "" {
				req.ReviewRoute = a.ReviewRoute
			}
			if req.BudgetAmount == nil {
				req.BudgetAmount = a.BudgetUSD
			}
			promptOverlay = a.PromptOverlay
		}
	}
	// An agent's route (and this handler's own fallback) can still be
	// "" — that's each agent's shorthand for "the default chain," same
	// as chat's own default-role fallback — but the gateway requires a
	// real route name, so the substitution has to happen somewhere
	// concrete.
	defaultRoute := ""
	if h.routeForRole != nil {
		defaultRoute = h.routeForRole(r.Context(), "default")
	}
	if req.Route == "" {
		req.Route = defaultRoute
	}
	if req.ReviewRoute == "" {
		req.ReviewRoute = defaultRoute
	}

	autoApproveSafe := true
	if req.AutoApproveSafe != nil {
		autoApproveSafe = *req.AutoApproveSafe
	}
	budgetCurrency := req.BudgetCurrency
	if budgetCurrency == "" {
		budgetCurrency = "USD"
	}
	m := missions.Mission{
		Goal: req.Goal, Kind: req.Kind, AgentID: req.AgentID,
		Route: req.Route, ReviewRoute: req.ReviewRoute, EscalationRoute: req.EscalationRoute,
		MaxIterations: req.MaxIterations, BudgetAmount: req.BudgetAmount, BudgetCurrency: budgetCurrency,
		AutoApproveSafe: autoApproveSafe, PromptOverlay: promptOverlay,
	}
	id, err := h.driver.Create(r.Context(), m)
	if err != nil {
		failMission(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// classifyKind decides how a mission's work happens when the create
// request omits kind. Biased hard toward "coding" on anything short of
// an unambiguous "general" reply: a coding goal misread as general
// loses branch/diff/rollback safety (the worse failure), while a
// general goal misread as coding only wastes an empty worktree — so
// every ambiguous or failed classification lands on the cheap side of
// the error. nil classify (no gateway wiring) takes the same default.
func classifyKind(ctx context.Context, classify agents.Classify, goal string) string {
	if classify == nil {
		return "coding"
	}
	prompt := "Decide how this mission's work happens. Answer with exactly one word.\n" +
		"coding — the goal requires creating or modifying code, scripts, or configuration in a project/repository.\n" +
		"general — everything else (documents, analysis, data gathering, operations).\n\n" +
		"Goal: " + goal
	reply, err := classify(ctx, prompt)
	if err != nil {
		return "coding"
	}
	reply = strings.ToLower(reply)
	// "general" wins only when "coding" is absent from the reply.
	if strings.Contains(reply, "general") && !strings.Contains(reply, "coding") {
		return "general"
	}
	return "coding"
}

// classifyGoal serves POST /v1/missions/classify: the same
// classification create() falls back to when kind is omitted, exposed
// standalone so the web UI's chip preview can show a mission's inferred
// kind before submit without actually creating anything.
func (h *missionAPI) classifyGoal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Goal string `json:"goal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Goal == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "goal is required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"kind": classifyKind(r.Context(), h.classify, req.Goal)})
}

func (h *missionAPI) get(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// delete permanently removes a terminal mission: its row (Store.Delete
// cascades mission_events/notifications), its hidden session, and its
// on-disk workspace. Only a terminal mission (done/error, which
// includes cancelled) may be deleted — 409 not_terminal otherwise — so
// a live mission's row can never vanish out from under a running
// Driver.Advance. Session and workspace cleanup are best-effort past
// that point: the mission row is already gone, so a failure here is
// logged, never turned into an error response.
func (h *missionAPI) delete(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Delete(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.SessionID != "" && h.dir != nil {
		if err := h.dir.Delete(r.Context(), m.SessionID); err != nil {
			h.log.Warn("mission delete: hidden session cleanup failed", "mission_id", m.ID, "session_id", m.SessionID, "error", err)
		}
	}
	if m.Workspace != "" && h.workspace != nil {
		if err := h.workspace.Teardown(r.Context(), m.Workspace, m.Worktree, m.Kind); err != nil {
			h.log.Warn("mission delete: workspace cleanup failed", "mission_id", m.ID, "workspace", m.Workspace, "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *missionAPI) events(w http.ResponseWriter, r *http.Request) {
	events, err := h.store.Events(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// resumeAnswerCap bounds the answer text carried into the progress note
// and the mission.answered event payload — same cap as other bounded
// event-payload fields in this package (e.g. push_failed's reason).
const resumeAnswerCap = 500

// resume handles POST /v1/missions/{id}/resume. An optional JSON body
// {"answer": "..."} carries the human's reply to a worker's blocked
// question: it's appended as a progress note (so the next worker
// session's packet renders it, same as any other progress note) and as
// a mission.answered event, BEFORE the resume signal — so the worker
// that runs next already has the answer in its packet. An empty or
// absent body (or one with an empty/missing answer) resumes exactly as
// before this existed; only malformed JSON is rejected.
func (h *missionAPI) resume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON with an optional answer field")
		return
	}
	id := r.PathValue("id")
	if body.Answer != "" {
		answer := missions.NeutralizeSlot(truncateAnswer(body.Answer, resumeAnswerCap))
		if err := h.store.AppendProgress(r.Context(), id, "Answer to your question: "+answer); err != nil {
			h.log.Warn("mission: record answer progress note failed", "mission_id", id, "error", err)
		}
		if err := h.store.AppendEvent(r.Context(), id, "mission.answered", map[string]any{"answer": answer}); err != nil {
			h.log.Warn("mission: record mission.answered event failed", "mission_id", id, "error", err)
		}
	}
	if err := h.driver.Signal(r.Context(), id, missions.InputResume); err != nil {
		failMission(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// truncateAnswer caps an answer at n runes-as-bytes, matching the
// truncate helper's shape elsewhere in this codebase (missions.truncate
// is unexported to this package).
func truncateAnswer(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (h *missionAPI) cancel(w http.ResponseWriter, r *http.Request) {
	if err := h.driver.Signal(r.Context(), r.PathValue("id"), missions.InputCancel); err != nil {
		failMission(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// permission answers a mission's pending_permission — the SAME
// permission broker chat sessions use (a.perms), keyed by the
// broker-issued id stored on the mission row rather than a session id.
// No mission-specific broker exists; a mission's pending_permission is
// only ever set by the same interactive-prompt code path chat already
// goes through.
func (h *missionAPI) permission(w http.ResponseWriter, r *http.Request) {
	if h.perms == nil {
		jsonError(w, http.StatusNotFound, "not_found", "permissions are not enabled")
		return
	}
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.PendingPermission == "" {
		jsonError(w, http.StatusNotFound, "not_found", "this mission has no pending permission request")
		return
	}
	var body struct {
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON with a decision field")
		return
	}
	switch body.Decision {
	case "once", "session", "deny":
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", `decision must be "once", "session", or "deny"`)
		return
	}
	if !h.perms.Resolve(m.PendingPermission, body.Decision) {
		jsonError(w, http.StatusNotFound, "not_found", "unknown or already-answered permission request")
		return
	}
	// Best-effort: the decision already took effect (Resolve above
	// unparked the turn); a failure to log it is a missing Timeline
	// entry, not a wrong mission state, so it must not fail this request.
	if err := h.store.AppendEvent(r.Context(), m.ID, "mission.permission_answered",
		map[string]any{"tool": m.PendingPermissionTool, "decision": body.Decision}); err != nil {
		h.log.Warn("mission: record permission_answered event failed", "mission_id", m.ID, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// declaredArtifacts builds the workspace-relative, filepath.Clean'ed
// set of paths the mission's plan units declare — files.go itself
// never imports or knows about Spec, keeping the two decoupled.
func declaredArtifacts(spec missions.Spec) map[string]bool {
	declared := map[string]bool{}
	for _, u := range spec.Units {
		for _, a := range u.Artifacts {
			declared[filepath.Clean(a)] = true
		}
	}
	return declared
}

func (h *missionAPI) files(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.Workspace == "" {
		jsonError(w, http.StatusNotFound, "no_workspace", "this mission has no workspace")
		return
	}
	entries, truncated, err := missions.ListFiles(m.WorkRoot(), declaredArtifacts(m.Spec))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "files_failed", err.Error())
		return
	}
	if entries == nil {
		entries = []missions.FileEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": entries, "truncated": truncated})
}

// download streams one workspace file. Content-Type is always forced
// to application/octet-stream + nosniff — this is same-origin SPA
// traffic, and a worker-authored file (which could be arbitrary HTML)
// must never be rendered by the browser.
func (h *missionAPI) download(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.Workspace == "" {
		jsonError(w, http.StatusNotFound, "no_workspace", "this mission has no workspace")
		return
	}
	rel := r.PathValue("path")
	f, fi, err := missions.OpenFile(m.WorkRoot(), rel)
	if err != nil {
		// A containment violation and a plain not-found both read as 404
		// — the reason must never leak through a different status.
		jsonError(w, http.StatusNotFound, "not_found", "file not found")
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(rel)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// archive streams the mission's whole workspace as a zip. Headers are
// sent before WriteArchive begins, so a mid-stream failure can only be
// logged — the HTTP status is already committed.
func (h *missionAPI) archive(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.Workspace == "" {
		jsonError(w, http.StatusNotFound, "no_workspace", "this mission has no workspace")
		return
	}
	id := m.ID
	if len(id) > 8 {
		id = id[:8]
	}
	name := fmt.Sprintf("mission-%s.zip", id)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Content-Type", "application/zip")
	if err := missions.WriteArchive(m.WorkRoot(), w); err != nil {
		h.log.Warn("mission archive stream failed", "mission_id", m.ID, "error", err)
	}
}

// pushRefPattern bounds credential_ref the same way any other operator
// identifier is bounded elsewhere in this API — a defensive shape
// check, not a secret-store lookup.
var pushRefPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,128}$`)

// push pushes the mission's branch to its worktree's origin remote,
// authenticated with the resolved credential_ref. Guards (kind,
// branch, worktree presence) are Go code, never a prompt — only coding
// missions with a live worktree are ever pushable.
func (h *missionAPI) push(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.Kind != "coding" {
		jsonError(w, http.StatusBadRequest, "not_pushable", "only coding missions can be pushed")
		return
	}
	if m.Branch == "" {
		jsonError(w, http.StatusBadRequest, "not_pushable", "mission has no branch")
		return
	}
	if _, err := os.Stat(m.Worktree); err != nil {
		jsonError(w, http.StatusBadRequest, "not_pushable", "mission worktree is not available")
		return
	}
	var req struct {
		CredentialRef string `json:"credential_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if !pushRefPattern.MatchString(req.CredentialRef) {
		jsonError(w, http.StatusBadRequest, "bad_request", "credential_ref is required and must match ^[A-Za-z0-9_./-]{1,128}$")
		return
	}
	if h.resolveSecret == nil {
		jsonError(w, http.StatusNotFound, "not_found", "secret store not configured")
		return
	}
	token, err := h.resolveSecret(r.Context(), req.CredentialRef)
	if err != nil {
		if errors.Is(err, secretstore.ErrNotFound) {
			jsonError(w, http.StatusNotFound, "secret_not_found", "no secret configured for that credential_ref")
			return
		}
		jsonError(w, http.StatusBadGateway, "push_failed", "failed to resolve credential")
		return
	}
	host, pushErr := h.workspace.Push(r.Context(), m.Worktree, m.Branch, token)
	if pushErr != nil {
		reason := "push failed"
		status, code := http.StatusBadGateway, "push_failed"
		switch {
		case errors.Is(pushErr, missions.ErrRemoteUnsupported):
			status, code = http.StatusBadRequest, "remote_unsupported"
			reason = "remote unsupported"
		case errors.Is(pushErr, missions.ErrPushRejected):
			status, code = http.StatusConflict, "push_rejected"
			reason = "push rejected"
		}
		if err := h.store.AppendEvent(r.Context(), m.ID, "mission.push_failed", map[string]any{"reason": reason}); err != nil {
			h.log.Warn("mission: record push_failed event failed", "mission_id", m.ID, "error", err)
		}
		jsonError(w, status, code, pushErr.Error())
		return
	}
	if err := h.store.AppendEvent(r.Context(), m.ID, "mission.pushed", map[string]any{"branch": m.Branch, "remote_host": host}); err != nil {
		h.log.Warn("mission: record pushed event failed", "mission_id", m.ID, "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"branch": m.Branch, "remote_host": host})
}

func (h *missionAPI) notifications(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		jsonError(w, http.StatusNotFound, "not_found", "notifications are not enabled")
		return
	}
	rows, err := h.notifier.List(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "notifications_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": rows})
}

func (h *missionAPI) markRead(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		jsonError(w, http.StatusNotFound, "not_found", "notifications are not enabled")
		return
	}
	if err := h.notifier.MarkRead(r.Context(), r.PathValue("id")); err != nil {
		jsonError(w, http.StatusInternalServerError, "mark_read_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
