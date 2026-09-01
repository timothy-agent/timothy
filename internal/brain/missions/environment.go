package missions

import (
	"os"
	"path/filepath"
)

// Environments is the D-05x allowlist of sandbox environment keys a
// coding mission may explicitly select — mirrored by sandboxd's own
// key->image map (internal/sandboxd/manager.go); kept here too so the
// API layer can validate a create/schedule request without brain
// importing sandboxd. "base" forces the base image explicitly
// (distinct from "", which means "detect", see ValidEnvironment).
var Environments = map[string]bool{
	"base":   true,
	"go":     true,
	"node":   true,
	"python": true,
	"java":   true,
	"php":    true,
}

// ValidEnvironment reports whether v is "" (detect) or a registered
// environment key. "" and "base" are NOT the same: "" means detect from
// the real workspace once it exists (repo markers right after the
// clone, else the discover turn's own report, issue #495), while
// "base" is the operator explicitly opting out of detection entirely.
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
// matched. Returns "", "" when worktree is empty/unreadable or no
// marker matches — a freshly self-initialized mission repo has no
// marker files, which is what the discover turn's own environment
// report (runner.go's DiscoverSession) covers.
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
