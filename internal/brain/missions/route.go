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

// Harness provenance values, shared by every resolution site
// (mission create, schedule fire, execution-plan preview) so they
// can never disagree on what a given source is called.
const (
	HarnessSourceExplicit = "explicit"
	HarnessSourceAgent    = "agent"
	HarnessSourceSettings = "settings"
)

// ResolveHarness is the single precedence chain for a coding mission's
// harness (docs/2026-08-26-mission-execution-plan.md, slice 2):
// mission.harness -> agent.harness -> settings.coding_executor ->
// native. Only kind=coding may ever delegate (mirrors policy.go's
// canDelegate, D-072); any other kind always resolves to "" (native).
// agentHarness is the agent's own harness field, already resolved by
// the caller (empty means the agent doesn't override); settingsDefault
// resolves settings.ValueCodingExecutor, nil when unwired. "native" is
// a stored sentinel for "off" at every level and normalizes to "".
func ResolveHarness(ctx context.Context, kind, explicit, agentHarness string, settingsDefault func(context.Context) string) (harness, source string) {
	if kind != KindCoding {
		return "", ""
	}
	switch {
	case explicit != "":
		harness, source = explicit, HarnessSourceExplicit
	case agentHarness != "":
		harness, source = agentHarness, HarnessSourceAgent
	case settingsDefault != nil:
		harness, source = settingsDefault(ctx), HarnessSourceSettings
	}
	if harness == "native" {
		harness = ""
	}
	if harness == "" {
		return "", ""
	}
	return harness, source
}
