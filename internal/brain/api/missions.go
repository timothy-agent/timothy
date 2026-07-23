package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// defaultMissionRoute mirrors chat.defaultRoute: an agent's empty
// route means "the default chain," but the gateway's /v1/stream
// requires a real, non-empty route name.
const defaultMissionRoute = "default"

// registerMissions mounts the mission surface: served locally,
// missions are brain's domain like agents and connectors. nil store
// leaves the surface unmounted (404s), matching the memories/admin/
// agents gating pattern. agentReg resolves a mission's route/
// review_route/budget/approval_allowlist defaults from the chosen
// agent when the create request omits them.
func (a *API) registerMissions(handle func(pattern string, h http.Handler), store *missions.Store, driver *missions.Driver, notifier *missions.Notifier, agentReg *agents.Store) {
	if store == nil {
		return
	}
	h := &missionAPI{store: store, driver: driver, notifier: notifier, agentReg: agentReg, perms: a.perms, log: a.log}
	handle("GET /v1/missions", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/missions", a.auth(http.HandlerFunc(h.create)))
	handle("GET /v1/missions/{id}", a.auth(http.HandlerFunc(h.get)))
	handle("GET /v1/missions/{id}/events", a.auth(http.HandlerFunc(h.events)))
	handle("POST /v1/missions/{id}/resume", a.auth(http.HandlerFunc(h.resume)))
	handle("POST /v1/missions/{id}/cancel", a.auth(http.HandlerFunc(h.cancel)))
	handle("POST /v1/missions/{id}/permission", a.auth(http.HandlerFunc(h.permission)))
	handle("GET /v1/notifications", a.auth(http.HandlerFunc(h.notifications)))
	handle("POST /v1/notifications/{id}/read", a.auth(http.HandlerFunc(h.markRead)))
}

type missionAPI struct {
	store    *missions.Store
	driver   *missions.Driver
	notifier *missions.Notifier
	agentReg *agents.Store
	// perms answers a mission's pending_permission — the same
	// PermissionResolver chat sessions use (A.perms), never a
	// mission-specific broker.
	perms PermissionResolver
	log   *slog.Logger
}

func failMission(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, missions.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, missions.ErrBranchConflict):
		jsonError(w, http.StatusConflict, "branch_conflict", err.Error())
	case errors.Is(err, missions.ErrTerminal):
		jsonError(w, http.StatusConflict, "already_finished", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

func (h *missionAPI) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.List(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "missions_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"missions": rows})
}

type createMissionRequest struct {
	Goal          string   `json:"goal"`
	Kind          string   `json:"kind"`
	AgentID       string   `json:"agent_id"`
	Route         string   `json:"route"`
	ReviewRoute   string   `json:"review_route"`
	MaxIterations int      `json:"max_iterations"`
	BudgetUSD     *float64 `json:"budget_usd"`
	RepoPath      string   `json:"repo_path"`
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
	case "coding", "research", "scheduled":
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", `kind must be "coding", "research", or "scheduled"`)
		return
	}
	if req.Kind == "coding" && req.RepoPath == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "repo_path is required for coding missions")
		return
	}

	// Resolve even with an empty AgentID: agents.Resolve("") falls back
	// to the default agent, same as chat sessions that don't pick one —
	// a mission created without an explicit agent still needs a real
	// route, not an empty string the gateway will reject.
	if h.agentReg != nil {
		if a, ok := h.agentReg.Resolve(r.Context(), req.AgentID); ok {
			if req.Route == "" {
				req.Route = a.Route
			}
			if req.ReviewRoute == "" {
				req.ReviewRoute = a.ReviewRoute
			}
			if req.BudgetUSD == nil {
				req.BudgetUSD = a.BudgetUSD
			}
		}
	}
	// An agent's route (and this handler's own fallback) can still be
	// "" — that's each agent's shorthand for "the default chain," same
	// as chat.defaultRoute — but the gateway requires a real route
	// name, so the substitution has to happen somewhere concrete.
	if req.Route == "" {
		req.Route = defaultMissionRoute
	}
	if req.ReviewRoute == "" {
		req.ReviewRoute = defaultMissionRoute
	}

	autoApproveSafe := true
	if req.AutoApproveSafe != nil {
		autoApproveSafe = *req.AutoApproveSafe
	}
	m := missions.Mission{
		Goal: req.Goal, Kind: req.Kind, AgentID: req.AgentID,
		Route: req.Route, ReviewRoute: req.ReviewRoute,
		MaxIterations: req.MaxIterations, BudgetUSD: req.BudgetUSD,
		AutoApproveSafe: autoApproveSafe,
	}
	id, err := h.driver.Create(r.Context(), m, req.RepoPath)
	if err != nil {
		failMission(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h *missionAPI) get(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (h *missionAPI) events(w http.ResponseWriter, r *http.Request) {
	events, err := h.store.Events(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (h *missionAPI) resume(w http.ResponseWriter, r *http.Request) {
	if err := h.driver.Signal(r.Context(), r.PathValue("id"), missions.InputResume); err != nil {
		failMission(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
