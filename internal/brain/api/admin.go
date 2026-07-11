package api

import "net/http"

// adminRoutePatterns is the EXHAUSTIVE admin surface brain exposes;
// everything else on the gateway's internal API stays unreachable
// from outside. Tests pin this scope like the memory proxy's.
var adminRoutePatterns = []string{
	"GET /v1/admin/usage/{rest...}",
	"GET /v1/admin/providers",
	"POST /v1/admin/providers",
	"PATCH /v1/admin/providers/{id}",
	"DELETE /v1/admin/providers/{id}",
	"POST /v1/admin/providers/{id}/test",
	"GET /v1/admin/providers/health",
	"GET /v1/admin/routes",
	"PATCH /v1/admin/routes/{category}",
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
