package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
)

// registerConnectors mounts brain's own integration control plane —
// served locally like the settings routes, not proxied: connectors
// are brain's domain (their tools execute in the agent loop). nil mgr
// leaves the surface unmounted (no master key, connectors disabled).
func (a *API) registerConnectors(handle func(pattern string, h http.Handler), mgr *connectors.Manager, goog *connectors.Google) {
	if mgr == nil {
		return
	}
	h := &connectorAPI{mgr: mgr, goog: goog}
	handle("GET /v1/admin/connectors", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/admin/connectors", a.auth(http.HandlerFunc(h.create)))
	handle("PATCH /v1/admin/connectors/{id}", a.auth(http.HandlerFunc(h.patch)))
	handle("DELETE /v1/admin/connectors/{id}", a.auth(http.HandlerFunc(h.delete)))
	handle("POST /v1/admin/connectors/{id}/test", a.auth(http.HandlerFunc(h.test)))
	handle("GET /v1/admin/connectors/{id}/repos", a.auth(http.HandlerFunc(h.listRepos)))
	handle("POST /v1/admin/connectors/{id}/repos", a.auth(http.HandlerFunc(h.createRepo)))
	if goog != nil {
		handle("POST /v1/admin/connectors/{id}/oauth/start", a.auth(http.HandlerFunc(h.oauthStart)))
		// The callback is Google redirecting the user's browser — no
		// bearer possible; the single-use expiring state token is the
		// auth. It writes nothing an attacker chooses: a forged call
		// without a live state is rejected.
		handle("GET /v1/connectors/oauth/callback", http.HandlerFunc(h.oauthCallback))
	}
}

type connectorAPI struct {
	mgr  *connectors.Manager
	goog *connectors.Google
}

// oauthStart begins the OAuth dance and returns the URL the browser
// should visit.
func (h *connectorAPI) oauthStart(w http.ResponseWriter, r *http.Request) {
	authURL, err := h.goog.StartAuth(r.Context(), r.PathValue("id"))
	if err != nil {
		failConnector(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": authURL})
}

// oauthCallback finishes the dance and bounces the browser back to
// Settings with the outcome in the query.
func (h *connectorAPI) oauthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errCode := q.Get("error"); errCode != "" {
		http.Redirect(w, r, "/settings/connectors?oauth_error="+url.QueryEscape(errCode), http.StatusFound)
		return
	}
	name, err := h.goog.HandleCallback(r.Context(), q.Get("state"), q.Get("code"))
	if err != nil {
		http.Redirect(w, r, "/settings/connectors?oauth_error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/settings/connectors?oauth_connected="+url.QueryEscape(name), http.StatusFound)
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

// listRepos serves GET /v1/admin/connectors/{id}/repos: every repo the
// connector's PAT can see, for the mission-create repo picker.
// Non-github-kind (or unbuildable) connectors 400/422 via failConnector
// (ErrUnsupported maps to 422 there — kept as 400 here since a picker
// asking a non-github connector for repos is caller error, not an
// infra condition).
func (h *connectorAPI) listRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := h.mgr.ListRepos(r.Context(), r.PathValue("id"))
	if err != nil {
		failConnector(w, err)
		return
	}
	if repos == nil {
		repos = []connectors.GitHubRepo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

// createRepo serves POST /v1/admin/connectors/{id}/repos body
// {"name","private"}: creates a new repo through the connector's PAT,
// auto-initialized so it has a default branch to clone.
func (h *connectorAPI) createRepo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Private bool   `json:"private"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	repo, err := h.mgr.CreateRepo(r.Context(), r.PathValue("id"), req.Name, req.Private)
	if err != nil {
		failConnector(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repo)
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
// errors only for unknown ids and unbuildable kinds. A successful
// github-kind test also carries the resolved identity (D-057): the
// connector has no tools to prove itself with, so identity is the
// evidence a working PAT was configured.
func (h *connectorAPI) test(w http.ResponseWriter, r *http.Request) {
	identity, err := h.mgr.TestIdentity(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		out := map[string]any{"ok": true}
		if identity != nil {
			out["identity"] = identity
		}
		writeJSON(w, http.StatusOK, out)
	case errors.Is(err, connectors.ErrNotFound), errors.Is(err, connectors.ErrUnsupported):
		failConnector(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
	}
}
