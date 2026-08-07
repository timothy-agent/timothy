package missions

import "context"

// preferredCodingRoute is the operator's named route for coding
// missions' worker turns, checked before falling back to the "default"
// system role's route. No data-driven kind->route mapping exists today
// (routeForRole only binds "default"/"embedding"/"vision"/"summarize"
// system roles) — this is a fixed convention, not a config value,
// mirroring how harness's own "native" sentinel is a fixed name rather
// than a settings row.
const preferredCodingRoute = "coding"

// DefaultCodingRoute resolves a kind=coding request/template's empty
// route: prefer preferredCodingRoute when it actually exists (checked
// via routeExists, e.g. gwclient.ResolveRoute's success/404 outcome),
// falling back to defaultRoute (the "default" system role's route)
// otherwise — never errors, so a missing "coding" route degrades
// silently instead of 500ing mission creation. routeExists == nil
// (no gateway wiring) skips straight to defaultRoute.
func DefaultCodingRoute(ctx context.Context, routeExists func(context.Context, string) bool, defaultRoute string) string {
	if routeExists != nil && routeExists(ctx, preferredCodingRoute) {
		return preferredCodingRoute
	}
	return defaultRoute
}
