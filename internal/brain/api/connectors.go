package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
)

// registerConnectors mounts brain's own integration control plane —
// served locally like the settings routes, not proxied: connectors
// are brain's domain (their tools execute in the agent loop). nil
// leaves the surface unmounted (no master key, connectors disabled).
func (a *API) registerConnectors(handle func(pattern string, h http.Handler), mgr *connectors.Manager) {
	if mgr == nil {
		return
	}
	h := &connectorAPI{mgr: mgr}
	handle("GET /v1/admin/connectors", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/admin/connectors", a.auth(http.HandlerFunc(h.create)))
	handle("PATCH /v1/admin/connectors/{id}", a.auth(http.HandlerFunc(h.patch)))
	handle("DELETE /v1/admin/connectors/{id}", a.auth(http.HandlerFunc(h.delete)))
	handle("POST /v1/admin/connectors/{id}/test", a.auth(http.HandlerFunc(h.test)))
}

type connectorAPI struct {
	mgr *connectors.Manager
}

// failConnector maps the package's sentinel errors onto HTTP statuses;
// everything else is the caller's input.
func failConnector(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, connectors.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, connectors.ErrUnsupported):
		jsonError(w, http.StatusUnprocessableEntity, "unsupported", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

func (h *connectorAPI) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.mgr.Store().List(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "connectors_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": rows})
}

func (h *connectorAPI) create(w http.ResponseWriter, r *http.Request) {
	var c connectors.Connector
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := h.mgr.Store().Create(r.Context(), c)
	if err != nil {
		failConnector(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (h *connectorAPI) patch(w http.ResponseWriter, r *http.Request) {
	var patch connectors.Patch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.mgr.Store().Patch(r.Context(), r.PathValue("id"), patch); err != nil {
		failConnector(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *connectorAPI) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.mgr.Store().Delete(r.Context(), r.PathValue("id")); err != nil {
		failConnector(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// test mirrors the provider test endpoint's shape: 200 with {ok,
// error} for probe outcomes so the UI renders failures inline; HTTP
// errors only for unknown ids and unbuildable kinds.
func (h *connectorAPI) test(w http.ResponseWriter, r *http.Request) {
	err := h.mgr.Test(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case errors.Is(err, connectors.ErrNotFound), errors.Is(err, connectors.ErrUnsupported):
		failConnector(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
	}
}
