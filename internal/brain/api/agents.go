package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
)

// registerAgents mounts the agent registry (D-034) — served locally
// like settings and connectors: agents are brain's domain. nil store
// leaves the surface unmounted.
func (a *API) registerAgents(handle func(pattern string, h http.Handler), reg *agents.Store) {
	if reg == nil {
		return
	}
	h := &agentAPI{reg: reg}
	handle("GET /v1/admin/agents", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/admin/agents", a.auth(http.HandlerFunc(h.create)))
	handle("PATCH /v1/admin/agents/{id}", a.auth(http.HandlerFunc(h.patch)))
	handle("PUT /v1/admin/agents/{id}/default", a.auth(http.HandlerFunc(h.setDefault)))
	handle("DELETE /v1/admin/agents/{id}", a.auth(http.HandlerFunc(h.delete)))
}

type agentAPI struct {
	reg *agents.Store
}

func failAgent(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agents.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, agents.ErrInUse):
		jsonError(w, http.StatusConflict, "in_use", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

func (h *agentAPI) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.reg.List(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "agents_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": rows})
}

func (h *agentAPI) create(w http.ResponseWriter, r *http.Request) {
	var a agents.Agent
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := h.reg.Create(r.Context(), a)
	if err != nil {
		failAgent(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h *agentAPI) patch(w http.ResponseWriter, r *http.Request) {
	var p agents.Patch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.reg.Patch(r.Context(), r.PathValue("id"), p); err != nil {
		failAgent(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *agentAPI) setDefault(w http.ResponseWriter, r *http.Request) {
	if err := h.reg.SetDefault(r.Context(), r.PathValue("id")); err != nil {
		failAgent(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *agentAPI) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.reg.Delete(r.Context(), r.PathValue("id")); err != nil {
		failAgent(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
