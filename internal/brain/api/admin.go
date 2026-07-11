package api

import "net/http"

// adminRoutePatterns is the EXHAUSTIVE admin surface brain exposes;
// everything else on the gateway's internal API stays unreachable
// from outside. Tests pin this scope like the memory proxy's.
var adminRoutePatterns = []string{
	"GET /v1/admin/usage/{rest...}",
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
