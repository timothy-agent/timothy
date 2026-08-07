package missions

import (
	"os"
	"path/filepath"
	"regexp"
)

// Environments is the D-05x allowlist of sandbox environment keys a
// coding mission may explicitly select — mirrored by sandboxd's own
// key->image map (internal/sandboxd/manager.go); kept here too so the
// API layer can validate a create/schedule request without brain
// importing sandboxd. "base" forces the base image explicitly
// (distinct from "", which means "auto-detect" — see ValidEnvironment).
var Environments = map[string]bool{
	"base":   true,
	"go":     true,
	"node":   true,
	"python": true,
	"java":   true,
	"php":    true,
}

// ValidEnvironment reports whether v is "" (auto-detect) or a
// registered environment key. "" and "base" are NOT the same: "" means
// resolve the fallback chain (markers, then goal keyword) at create
// time, while "base" is the operator explicitly opting out of
// detection entirely.
func ValidEnvironment(v string) bool {
	return v == "" || Environments[v]
}

// environmentMarkers maps a repo marker file to the environment it
// implies, checked in this order — first match wins. Only one marker
// per environment is needed: these are presence checks, not build
// tooling detection.
var environmentMarkers = []struct {
	file string
	env  string
}{
	{"go.mod", "go"},
	{"package.json", "node"},
	{"composer.json", "php"},
	{"pom.xml", "java"},
	{"build.gradle", "java"},
	{"pyproject.toml", "python"},
	{"requirements.txt", "python"},
}

// detectEnvironmentFromMarkers scans worktree's top level for a known
// marker file, returning the implied environment and the marker that
// matched. Returns "", "" (base) when worktree is empty/unreadable or
// no marker matches — including a freshly self-initialized mission
// repo, which has no marker files at provisioning time (worktree.go's
// initSelfRepo): that's exactly the case goalEnvironmentKeyword below
// exists to still make a reasonable guess for.
func detectEnvironmentFromMarkers(worktree string) (env, marker string) {
	if worktree == "" {
		return "", ""
	}
	for _, m := range environmentMarkers {
		if fi, err := os.Stat(filepath.Join(worktree, m.file)); err == nil && !fi.IsDir() {
			return m.env, m.file
		}
	}
	return "", ""
}

// goalEnvironmentKeyword is the last resort in the detection chain
// (explicit request -> repo markers -> this -> base): a fresh coding
// mission's self-initialized repo has no marker files yet, so the
// goal text itself is the only remaining signal. Plain word-boundary
// regex, never an LLM call — cheap, deterministic, and resolved at
// mission creation time so the environment is fixed before the
// sandbox container is ever created. First matching rule wins; order
// matters only for "golang" vs "go" (checked as a distinct alternative
// so "go" doesn't need to worry about matching inside "golang" itself,
// though \b already prevents that).
var goalEnvironmentRules = []struct {
	pattern *regexp.Regexp
	env     string
}{
	{regexp.MustCompile(`(?i)\b(golang|go)\b`), "go"},
	{regexp.MustCompile(`(?i)\b(python|pip|django|flask|pytest)\b`), "python"},
	{regexp.MustCompile(`(?i)\b(node|npm|typescript|javascript|react|vite)\b`), "node"},
	{regexp.MustCompile(`(?i)\b(java|maven|gradle|spring)\b`), "java"},
	{regexp.MustCompile(`(?i)\b(php|composer|laravel)\b`), "php"},
}

// goalEnvironmentKeyword returns the environment implied by the first
// matching keyword rule against goal, or "" if none matches.
func goalEnvironmentKeyword(goal string) string {
	for _, r := range goalEnvironmentRules {
		if r.pattern.MatchString(goal) {
			return r.env
		}
	}
	return ""
}

// DetectEnvironment layers the full fallback chain below the explicit
// request (checked by callers before ever reaching here): repo
// markers, then the goal-keyword heuristic, then base. marker names
// which signal fired ("go.mod", or "goal:go" for the keyword path) —
// "" means base, nothing matched. worktree == "" (no repo yet, e.g.
// at mission-create time before provisioning) skips straight to the
// goal-keyword heuristic.
func DetectEnvironment(worktree, goal string) (env, marker string) {
	if env, marker := detectEnvironmentFromMarkers(worktree); env != "" {
		return env, marker
	}
	if env := goalEnvironmentKeyword(goal); env != "" {
		return env, "goal:" + env
	}
	return "", ""
}
