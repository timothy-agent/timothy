package missions

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Environments is the D-05x allowlist of sandbox environment keys a
// coding mission may explicitly select — mirrored by sandboxd's own
// key->image map (internal/sandboxd/manager.go); kept here too so the
// API layer can validate a mission or automation request without brain
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
// implies, in precedence order. Only one marker per environment is
// needed: these are presence checks, not build tooling detection.
// package.json is last because node tooling lives in the base image, so
// it must never outrank an ecosystem-specific marker.
var environmentMarkers = []struct {
	file string
	env  string
}{
	{"composer.json", "php"},
	{"go.mod", "go"},
	{"pom.xml", "java"},
	{"build.gradle", "java"},
	{"pyproject.toml", "python"},
	{"requirements.txt", "python"},
	{"package.json", "node"},
}

// environmentExtensions maps a source file extension to the
// environment it counts toward in the tie-break.
var environmentExtensions = map[string]string{
	".go":   "go",
	".php":  "php",
	".java": "java",
	".kt":   "java",
	".py":   "python",
	".js":   "node",
	".ts":   "node",
	".jsx":  "node",
	".tsx":  "node",
	".mjs":  "node",
	".cjs":  "node",
}

// sourceCountMaxDepth bounds the tie-break tree walk below the worktree root.
const sourceCountMaxDepth = 4

// detectEnvironmentFromMarkers scans worktree's top level for known
// marker files. The first marker in precedence order wins; when markers
// for different environments are present, the language with the most
// source files breaks the tie (precedence order on a tie). Returns the
// environment, the winning marker, and the other markers found.
// Returns "", "", nil when worktree is empty/unreadable or no marker
// matches: a freshly self-initialized mission repo has no marker files,
// which is what the discover turn's own environment report
// (runner.go's DiscoverSession) covers.
func detectEnvironmentFromMarkers(worktree string) (env, marker string, candidates []string) {
	if worktree == "" {
		return "", "", nil
	}
	envOf := map[string]string{}
	var found, specific []string
	for _, m := range environmentMarkers {
		envOf[m.file] = m.env
		if fi, err := os.Stat(filepath.Join(worktree, m.file)); err == nil && !fi.IsDir() {
			found = append(found, m.file)
			if m.env != "node" {
				specific = append(specific, m.file)
			}
		}
	}
	if len(found) == 0 {
		return "", "", nil
	}
	winner := found[0]
	if len(specific) > 1 && envOf[specific[0]] != envOf[specific[len(specific)-1]] {
		counts := countSourceFiles(worktree)
		best := -1
		for _, f := range specific {
			if c := counts[envOf[f]]; c > best {
				best, winner = c, f
			}
		}
	}
	for _, f := range found {
		if f != winner {
			candidates = append(candidates, f)
		}
	}
	return envOf[winner], winner, candidates
}

// countSourceFiles counts source files per environment under root,
// depth-limited, skipping vendor/, node_modules/, and dot-directories.
// WalkDir does not follow symlinks and only regular files are counted.
func countSourceFiles(root string) map[string]int {
	counts := map[string]int{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			name := d.Name()
			if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if rel, rerr := filepath.Rel(root, path); rerr != nil || strings.Count(rel, string(filepath.Separator)) >= sourceCountMaxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			if env, ok := environmentExtensions[strings.ToLower(filepath.Ext(d.Name()))]; ok {
				counts[env]++
			}
		}
		return nil
	})
	return counts
}
