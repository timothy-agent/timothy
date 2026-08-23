package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/workflows"
)

// workflowStarter is the narrow slice of *workflows.Engine the API
// needs to start a run — an interface so this file doesn't force a
// hard *workflows.Engine dependency shape beyond what's used here.
type workflowStarter interface {
	StartRun(ctx context.Context, workflowID string, runContext map[string]string) (string, error)
}

// registerWorkflows mounts the workflows CRUD + run surface — served
// locally, WORKFLOWS_ENABLED-gated (nil store leaves it unmounted, same
// nil-gating pattern as registerDestinations).
func (a *API) registerWorkflows(handle func(pattern string, h http.Handler), store *workflows.Store, engine workflowStarter) {
	if store == nil {
		return
	}
	h := &workflowAPI{store: store, engine: engine}
	handle("GET /v1/admin/workflows", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/admin/workflows", a.auth(http.HandlerFunc(h.create)))
	handle("GET /v1/admin/workflows/{id}", a.auth(http.HandlerFunc(h.get)))
	handle("PATCH /v1/admin/workflows/{id}", a.auth(http.HandlerFunc(h.patch)))
	handle("POST /v1/admin/workflows/{id}/runs", a.auth(http.HandlerFunc(h.startRun)))
	handle("GET /v1/admin/workflows/{id}/runs", a.auth(http.HandlerFunc(h.listRuns)))
	handle("GET /v1/admin/workflow-runs/{id}", a.auth(http.HandlerFunc(h.getRun)))
}

type workflowAPI struct {
	store  *workflows.Store
	engine workflowStarter
}

func failWorkflow(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workflows.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, workflows.ErrDuplicate):
		jsonError(w, http.StatusConflict, "duplicate", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

func (h *workflowAPI) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.List(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "workflows_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": rows})
}

func (h *workflowAPI) get(w http.ResponseWriter, r *http.Request) {
	wf, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failWorkflow(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

type createWorkflowRequest struct {
	Name       string          `json:"name"`
	Definition json.RawMessage `json:"definition"`
}

func (h *workflowAPI) create(w http.ResponseWriter, r *http.Request) {
	var req createWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	id, err := h.store.Create(r.Context(), req.Name, req.Definition)
	if err != nil {
		failWorkflow(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

type patchWorkflowRequest struct {
	Definition json.RawMessage `json:"definition"`
	Enabled    bool            `json:"enabled"`
}

func (h *workflowAPI) patch(w http.ResponseWriter, r *http.Request) {
	var req patchWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.store.Update(r.Context(), r.PathValue("id"), req.Definition, req.Enabled); err != nil {
		failWorkflow(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type startRunRequest struct {
	Context map[string]string `json:"context"`
}

func (h *workflowAPI) startRun(w http.ResponseWriter, r *http.Request) {
	if h.engine == nil {
		jsonError(w, http.StatusNotFound, "not_found", "workflows are not enabled")
		return
	}
	var req startRunRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}
	id, err := h.engine.StartRun(r.Context(), r.PathValue("id"), req.Context)
	if err != nil {
		failWorkflow(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (h *workflowAPI) listRuns(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListRuns(r.Context(), r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "workflows_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": rows})
}

func (h *workflowAPI) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.store.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		failWorkflow(w, err)
		return
	}
	events, err := h.store.RunEvents(r.Context(), r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "workflows_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "events": events})
}
