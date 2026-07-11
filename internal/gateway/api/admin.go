package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/gateway/admin"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
)

// RegisterAdmin mounts the internal control-plane routes. No auth here
// — brain proxies them behind its bearer as /v1/admin/*.
func RegisterAdmin(srv *httpserver.Server, adm *admin.Admin) {
	h := &adminAPI{adm: adm}
	srv.Handle("GET /internal/admin/providers", http.HandlerFunc(h.list))
	srv.Handle("POST /internal/admin/providers", http.HandlerFunc(h.create))
	srv.Handle("PATCH /internal/admin/providers/{id}", http.HandlerFunc(h.patch))
	srv.Handle("DELETE /internal/admin/providers/{id}", http.HandlerFunc(h.delete))
	srv.Handle("POST /internal/admin/providers/{id}/test", http.HandlerFunc(h.test))
	srv.Handle("GET /internal/admin/providers/health", http.HandlerFunc(h.health))
	srv.Handle("GET /internal/admin/routes", http.HandlerFunc(h.routes))
	srv.Handle("PATCH /internal/admin/routes/{category}", http.HandlerFunc(h.patchRoute))
}

type adminAPI struct {
	adm *admin.Admin
}

// fail maps admin sentinel errors onto HTTP statuses; everything else
// is the caller's input (400) — the DB layer wraps its own as 500s.
func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, admin.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, admin.ErrInUse):
		jsonError(w, http.StatusConflict, "in_use", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

func (h *adminAPI) list(w http.ResponseWriter, r *http.Request) {
	providers, err := h.adm.List(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "admin_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"providers": providers})
}

func (h *adminAPI) create(w http.ResponseWriter, r *http.Request) {
	var p admin.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := h.adm.Create(r.Context(), p)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

func (h *adminAPI) patch(w http.ResponseWriter, r *http.Request) {
	var patch admin.ProviderPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.adm.Patch(r.Context(), r.PathValue("id"), patch); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminAPI) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.adm.Delete(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminAPI) test(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // empty body = default model
	res, err := h.adm.Test(r.Context(), r.PathValue("id"), body.Model)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, res)
}

func (h *adminAPI) health(w http.ResponseWriter, r *http.Request) {
	rows, err := h.adm.Health(r.Context())
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "admin_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"providers": rows})
}

func (h *adminAPI) routes(w http.ResponseWriter, r *http.Request) {
	routes, err := h.adm.Routes(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "admin_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"routes": routes})
}

func (h *adminAPI) patchRoute(w http.ResponseWriter, r *http.Request) {
	var patch admin.RoutePatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.adm.PatchRoute(r.Context(), r.PathValue("category"), patch); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
