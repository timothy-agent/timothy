package api

import (
	"encoding/json"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/settings"
)

// adminRoutePatterns is the EXHAUSTIVE admin surface brain exposes;
// everything else on the gateway's internal API stays unreachable
// from outside. Tests pin this scope like the memory proxy's.
var adminRoutePatterns = []string{
	"GET /v1/admin/usage/{rest...}",
	"PATCH /v1/admin/usage/budget",
	"GET /v1/admin/providers",
	"POST /v1/admin/providers",
	"PATCH /v1/admin/providers/{id}",
	"DELETE /v1/admin/providers/{id}",
	"POST /v1/admin/providers/{id}/test",
	"GET /v1/admin/providers/{id}/models",
	"POST /v1/admin/providers/validate",
	"GET /v1/admin/providers/health",
	"GET /v1/admin/routes",
	"PATCH /v1/admin/routes/{category}",
	"PUT /v1/admin/secrets/{ref_name}",
	"DELETE /v1/admin/secrets/{ref_name}",
	"GET /v1/admin/secrets/{ref_name}",
	"GET /v1/admin/secret-backends/{backend}",
	"PUT /v1/admin/secret-backends/{backend}",
	"DELETE /v1/admin/secret-backends/{backend}",
	"POST /v1/admin/secret-backends/{backend}/test",
}

// registerAdmin mounts the admin proxy behind bearer auth. nil leaves
// the surface unmounted (gateway URL misconfigured).
func (a *API) registerAdmin(handle func(pattern string, h http.Handler), admin http.Handler) {
	if admin == nil {
		return
	}
	for _, pattern := range adminRoutePatterns {
		handle(pattern, a.auth(admin))
	}
}

// registerSettings mounts brain's own feature switches — served
// locally, not proxied: the gateway has no business knowing them.
func (a *API) registerSettings(handle func(pattern string, h http.Handler), flags *settings.Store) {
	if flags == nil {
		return
	}
	handle("GET /v1/admin/settings", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"settings": flags.All(r.Context())})
	})))
	handle("PATCH /v1/admin/settings", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]bool
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) == 0 {
			jsonError(w, http.StatusBadRequest, "bad_request", "body must be a JSON object of setting keys to booleans")
			return
		}
		for key, value := range body {
			if err := flags.Set(r.Context(), key, value); err != nil {
				jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}
