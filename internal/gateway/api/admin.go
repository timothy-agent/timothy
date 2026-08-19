package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/SumonMSelim/timothy/internal/gateway/admin"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
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
	srv.Handle("GET /internal/admin/providers/{id}/models", http.HandlerFunc(h.models))
	srv.Handle("POST /internal/admin/providers/validate", http.HandlerFunc(h.validate))
	srv.Handle("GET /internal/admin/providers/health", http.HandlerFunc(h.health))
	srv.Handle("GET /internal/admin/routes", http.HandlerFunc(h.routes))
	srv.Handle("POST /internal/admin/routes", http.HandlerFunc(h.createRoute))
	srv.Handle("PATCH /internal/admin/routes/{name}", http.HandlerFunc(h.patchRoute))
	srv.Handle("DELETE /internal/admin/routes/{name}", http.HandlerFunc(h.deleteRoute))
	srv.Handle("PUT /internal/admin/routes/{name}/role", http.HandlerFunc(h.setRouteRole))
	srv.Handle("PATCH /internal/admin/usage/budget", http.HandlerFunc(h.patchBudget))
	srv.Handle("GET /internal/admin/secrets", http.HandlerFunc(h.listSecrets))
	srv.Handle("PUT /internal/admin/secrets/{ref_name}", http.HandlerFunc(h.setSecret))
	srv.Handle("DELETE /internal/admin/secrets/{ref_name}", http.HandlerFunc(h.deleteSecret))
	srv.Handle("GET /internal/admin/secrets/{ref_name}", http.HandlerFunc(h.getSecretStatus))
	srv.Handle("POST /internal/admin/secrets/{ref_name}/migrate", http.HandlerFunc(h.migrateSecret))
	srv.Handle("POST /internal/admin/secrets/migrate", http.HandlerFunc(h.migrateAllSecrets))
	srv.Handle("GET /internal/admin/secret-backends", http.HandlerFunc(h.listSecretBackends))
	srv.Handle("PUT /internal/admin/secret-backends/default", http.HandlerFunc(h.putDefaultSecretBackend))
	srv.Handle("GET /internal/admin/secret-backends/{backend}", http.HandlerFunc(h.getSecretBackend))
	srv.Handle("PUT /internal/admin/secret-backends/{backend}", http.HandlerFunc(h.putSecretBackend))
	srv.Handle("DELETE /internal/admin/secret-backends/{backend}", http.HandlerFunc(h.deleteSecretBackend))
	srv.Handle("POST /internal/admin/secret-backends/{backend}/test", http.HandlerFunc(h.testSecretBackend))
	srv.Handle("POST /internal/admin/catalog/refresh", http.HandlerFunc(h.catalogRefresh))
	srv.Handle("GET /internal/admin/catalog/status", http.HandlerFunc(h.catalogStatus))
	srv.Handle("GET /internal/admin/catalog/models", http.HandlerFunc(h.catalogModels))
	srv.Handle("POST /internal/admin/catalog/prices", http.HandlerFunc(h.catalogPrices))
	srv.Handle("GET /internal/admin/providers/{id}/catalog-suggestions", http.HandlerFunc(h.catalogSuggestions))
	srv.Handle("GET /internal/admin/providers/{id}/catalog-models", http.HandlerFunc(h.catalogModelsForProvider))
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
	case errors.Is(err, admin.ErrUnsupported):
		jsonError(w, http.StatusUnprocessableEntity, "unsupported", err.Error())
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

// models proxies the provider's own model-listing endpoint; 422 means
// the driver cannot list models and the UI falls back to manual entry.
func (h *adminAPI) models(w http.ResponseWriter, r *http.Request) {
	models, err := h.adm.AvailableModels(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"models": models})
}

// validate probes an unsaved provider config with a one-token
// completion. Probe failures come back 200 with {ok:false, detail} so
// the UI renders them inline; only invalid configs get an HTTP error.
func (h *adminAPI) validate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		admin.Provider
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	res, err := h.adm.Validate(r.Context(), body.Provider, body.Model)
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
	if err := h.adm.PatchRoute(r.Context(), r.PathValue("name"), patch); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminAPI) createRoute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string `json:"name"`
		Capability string `json:"capability"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := h.adm.CreateRoute(r.Context(), body.Name, body.Capability)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

func (h *adminAPI) deleteRoute(w http.ResponseWriter, r *http.Request) {
	if err := h.adm.DeleteRoute(r.Context(), r.PathValue("name")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminAPI) setRouteRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.adm.SetRouteRole(r.Context(), r.PathValue("name"), body.Role); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminAPI) patchBudget(w http.ResponseWriter, r *http.Request) {
	var patch map[string]*ledger.BudgetLimit
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil || len(patch) == 0 {
		jsonError(w, http.StatusBadRequest, "bad_request",
			"body must map budget scopes (day, month) to {amount, currency} limits or null")
		return
	}
	if err := h.adm.PatchBudget(r.Context(), patch); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setSecret stores {"value": ...} through the store-wide default
// backend: built-in storage encrypts the value, an external default
// (vault/asm) treats it as the reference of a secret already there.
// The client no longer picks a backend per key — the one exception is
// {"backend": "db"}, which pins built-in storage for bootstrap
// credentials (the vault token, ASM secret key): the credential that
// unlocks an external backend cannot live behind that backend.
func (h *adminAPI) setSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value   string `json:"value"`
		Backend string `json:"backend"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var err error
	switch body.Backend {
	case "":
		err = h.adm.SetSecretValue(r.Context(), r.PathValue("ref_name"), body.Value)
	case "db":
		err = h.adm.SetSecret(r.Context(), r.PathValue("ref_name"), body.Value)
	default:
		jsonError(w, http.StatusUnprocessableEntity, "bad_backend",
			"per-key backend selection was removed; set the default backend in the Secrets tab")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listSecrets serves the credentials directory: names, timestamps, and
// which providers reference each one. Never a value.
func (h *adminAPI) listSecrets(w http.ResponseWriter, r *http.Request) {
	refs, err := h.adm.ListSecrets(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "admin_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"secrets": refs})
}

func (h *adminAPI) deleteSecret(w http.ResponseWriter, r *http.Request) {
	if err := h.adm.DeleteSecret(r.Context(), r.PathValue("ref_name")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminAPI) getSecretStatus(w http.ResponseWriter, r *http.Request) {
	configured, backend, err := h.adm.SecretStatus(r.Context(), r.PathValue("ref_name"))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "admin_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"configured": configured, "backend": backend})
}

// migrateSecret moves one ref's stored value onto {"backend": ...},
// wiping its old storage. Registered as a literal .../{ref_name}/migrate
// suffix, so it never collides with the bulk .../secrets/migrate route
// (net/http's ServeMux picks the more specific pattern regardless of
// registration order).
func (h *adminAPI) migrateSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Backend string `json:"backend"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.adm.MigrateSecret(r.Context(), r.PathValue("ref_name"), body.Backend); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// migrateAllSecrets moves every ref not already on {"backend": ...}
// there; per-ref failures land in the response, never abort the batch.
func (h *adminAPI) migrateAllSecrets(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Backend string `json:"backend"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	results, err := h.adm.MigrateAllSecrets(r.Context(), body.Backend)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"results": results})
}

func (h *adminAPI) listSecretBackends(w http.ResponseWriter, r *http.Request) {
	backends, err := h.adm.SecretBackends(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "admin_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"backends": backends})
}

// putDefaultSecretBackend is registered as a literal path, so it wins
// over the {backend} wildcard routes on the same prefix.
func (h *adminAPI) putDefaultSecretBackend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Backend string `json:"backend"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.adm.SetDefaultSecretBackend(r.Context(), body.Backend); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminAPI) getSecretBackend(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.adm.SecretBackendConfig(r.Context(), r.PathValue("backend"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]json.RawMessage{"config": cfg})
}

func (h *adminAPI) putSecretBackend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.adm.SetSecretBackendConfig(r.Context(), r.PathValue("backend"), body.Config); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminAPI) deleteSecretBackend(w http.ResponseWriter, r *http.Request) {
	if err := h.adm.DeleteSecretBackendConfig(r.Context(), r.PathValue("backend")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testSecretBackend mirrors the provider test endpoint's shape: 200
// with {ok, error} rather than an HTTP error, so the UI renders the
// failure text inline.
func (h *adminAPI) testSecretBackend(w http.ResponseWriter, r *http.Request) {
	if err := h.adm.TestSecretBackend(r.Context(), r.PathValue("backend")); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (h *adminAPI) catalogRefresh(w http.ResponseWriter, r *http.Request) {
	st, err := h.adm.CatalogRefresh(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, "catalog_refresh_failed", err.Error())
		return
	}
	writeJSON(w, st)
}

func (h *adminAPI) catalogStatus(w http.ResponseWriter, r *http.Request) {
	st, err := h.adm.CatalogStatus(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "admin_failed", err.Error())
		return
	}
	writeJSON(w, st)
}

func (h *adminAPI) catalogModels(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	models, err := h.adm.CatalogSearch(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("provider"), limit)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "admin_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"models": models})
}

func (h *adminAPI) catalogSuggestions(w http.ResponseWriter, r *http.Request) {
	suggestions, err := h.adm.CatalogSuggestions(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"suggestions": suggestions})
}

func (h *adminAPI) catalogModelsForProvider(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	models, err := h.adm.CatalogModelsForProvider(r.Context(), r.PathValue("id"), r.URL.Query().Get("q"), limit)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"models": models})
}

// maxPricesModels caps POST .../catalog/prices' request body — an
// estimate lookup for a display page, never a bulk export.
const maxPricesModels = 100

func (h *adminAPI) catalogPrices(w http.ResponseWriter, r *http.Request) {
	var pairs []admin.ProviderModel
	if err := json.NewDecoder(r.Body).Decode(&pairs); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be a JSON array of {provider, model}")
		return
	}
	if len(pairs) > maxPricesModels {
		pairs = pairs[:maxPricesModels]
	}
	priced, err := h.adm.CatalogPrices(r.Context(), pairs)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "admin_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"prices": priced})
}
