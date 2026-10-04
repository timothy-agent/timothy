package missions

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// detectToolchainVersions reads the toolchain versions the repo pins
// (D-126, issue #991) from marker files only: nothing is executed. The
// result maps tool -> a mise-acceptable version prefix, empty when
// nothing is pinned. First hit per tool wins:
//
//	tool    markers, in order
//	python  .python-version, runtime.txt, pyproject.toml requires-python, .tool-versions, .mise.toml
//	node    .nvmrc, .node-version, package.json engines.node, .tool-versions, .mise.toml
//	go      go.mod go directive, .tool-versions, .mise.toml
//
// Other tools are ignored: mise builds most of them from source, which
// the sandbox cannot do inside the install ceiling.
//
// The language markers apply when env is that language or "" / "base";
// .tool-versions and .mise.toml apply for every env. A constraint is
// reduced to a prefix mise accepts (">=3.10,<3.11" -> "3.10", "^18" ->
// "18", "v18.20.4" -> "18.20.4"); one that cannot be reduced is skipped.
func detectToolchainVersions(worktree, env string) map[string]string {
	out := map[string]string{}
	if worktree == "" {
		return out
	}
	all := env == "" || env == "base"
	set := func(tool, constraint string) {
		if _, ok := out[tool]; ok {
			return
		}
		if v, ok := normalizeToolVersion(constraint); ok && supportedToolchains[tool] {
			out[tool] = v
		}
	}
	if all || env == "python" {
		set("python", firstLine(worktree, ".python-version"))
		if m := runtimeTxtRe.FindStringSubmatch(firstLine(worktree, "runtime.txt")); m != nil {
			set("python", m[1])
		}
		if m := requiresPythonRe.FindStringSubmatch(readMarker(worktree, "pyproject.toml")); m != nil {
			set("python", m[1])
		}
	}
	if all || env == "node" {
		set("node", firstLine(worktree, ".nvmrc"))
		set("node", firstLine(worktree, ".node-version"))
		var pkg struct {
			Engines struct {
				Node string `json:"node"`
			} `json:"engines"`
		}
		if json.Unmarshal([]byte(readMarker(worktree, "package.json")), &pkg) == nil {
			set("node", pkg.Engines.Node)
		}
	}
	if all || env == "go" {
		if m := goDirectiveRe.FindStringSubmatch(readMarker(worktree, "go.mod")); m != nil {
			set("go", m[1])
		}
	}
	for tool, v := range parseToolVersions(readMarker(worktree, ".tool-versions")) {
		set(tool, v)
	}
	for tool, v := range parseMiseToml(readMarker(worktree, ".mise.toml")) {
		set(tool, v)
	}
	return out
}

// supportedToolchains are the tools mise installs from prebuilt binaries.
var supportedToolchains = map[string]bool{"python": true, "node": true, "go": true}

var (
	finalVersionRe   = regexp.MustCompile(`^[0-9][0-9A-Za-z.\-]*$`)
	versionClauseRe  = regexp.MustCompile(`^(>=|~=|==|\^|~|=)?\s*v?(\d+(?:\.\d+){0,2})(\.[x*])?`)
	runtimeTxtRe     = regexp.MustCompile(`^python-(\S+)`)
	requiresPythonRe = regexp.MustCompile(`(?m)^\s*requires-python\s*=\s*["']([^"']+)["']`)
	goDirectiveRe    = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)\s*$`)
	miseToolLineRe   = regexp.MustCompile(`^["']?([A-Za-z0-9_-]+)["']?\s*=\s*(.+)$`)
	quotedRe         = regexp.MustCompile(`["']([^"']+)["']`)
	goalToolchainRe  = regexp.MustCompile(`(?i)\b(?:python\s*v?([23]\.\d+(?:\.\d+)?)|node(?:\.?js)?\s*v?(\d{2}(?:\.\d+){0,2})|go(?:lang)?\s*v?(1\.\d+(?:\.\d+)?))\b`)
	toolAliases      = map[string]string{"nodejs": "node", "golang": "go"}
)

// detectMissionToolchains is detectToolchainVersions with the mission goal
// as fallback: a tool no marker pins takes the version the goal names
// ("a Django app on Python 3.10", "Node 18"). Nothing pinned anywhere
// leaves the environment image's default toolchain.
func detectMissionToolchains(worktree, env, goal string) map[string]string {
	out := detectToolchainVersions(worktree, env)
	for tool, v := range goalToolchainVersions(goal) {
		if _, ok := out[tool]; !ok {
			out[tool] = v
		}
	}
	return out
}

// goalToolchainVersions returns the first version the goal names per
// tool. Patterns are strict (python 2.x/3.x and go 1.x need a minor,
// node a two-digit major) so prose like "go 2 steps" never matches.
func goalToolchainVersions(goal string) map[string]string {
	out := map[string]string{}
	for _, m := range goalToolchainRe.FindAllStringSubmatch(goal, -1) {
		var tool, v string
		switch {
		case m[1] != "":
			tool, v = "python", m[1]
		case m[2] != "":
			tool, v = "node", m[2]
		default:
			tool, v = "go", m[3]
		}
		if _, ok := out[tool]; !ok {
			out[tool] = v
		}
	}
	return out
}

// normalizeToolVersion reduces a version constraint to a prefix mise
// accepts. An operator constraint keeps at most major.minor (a lower
// bound is not an exact pin); alternatives ("||"), upper-only and
// exclusion constraints are not guessed at.
func normalizeToolVersion(c string) (string, bool) {
	c = strings.TrimSpace(c)
	if c == "" || strings.Contains(c, "||") {
		return "", false
	}
	m := versionClauseRe.FindStringSubmatch(c)
	if m == nil {
		return "", false
	}
	v := m[2]
	if (m[1] != "" && m[1] != "==" && m[1] != "=") || m[3] != "" {
		if parts := strings.Split(v, "."); len(parts) > 2 {
			v = strings.Join(parts[:2], ".")
		}
	}
	if !finalVersionRe.MatchString(v) {
		return "", false
	}
	return v, true
}

// readMarker returns a marker file's contents, "" when it is absent,
// a directory, or unreadable. Capped at 1 MiB.
func readMarker(worktree, name string) string {
	f, err := os.Open(filepath.Join(worktree, name)) //nolint:gosec // name is a fixed marker file name
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, 1<<20)
	n, _ := f.Read(b)
	return string(b[:n])
}

// firstLine returns a marker's first non-empty, non-comment line.
func firstLine(worktree, name string) string {
	sc := bufio.NewScanner(strings.NewReader(readMarker(worktree, name)))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			return l
		}
	}
	return ""
}

// parseToolVersions reads asdf-style ".tool-versions" lines ("python
// 3.10.4 3.9.1"): the first version per tool, aliases mapped to mise's
// tool names. Tools are returned in a map; callers sort when order matters.
func parseToolVersions(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		tool := strings.ToLower(f[0])
		if a, ok := toolAliases[tool]; ok {
			tool = a
		}
		if _, dup := out[tool]; !dup {
			out[tool] = f[1]
		}
	}
	return out
}

// parseMiseToml reads the [tools] table of a .mise.toml for the three
// shapes in common use: `tool = "v"`, `tool = ["v", ...]` (first
// element) and `tool = { version = "v" }`.
func parseMiseToml(s string) map[string]string {
	out := map[string]string{}
	inTools := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inTools = line == "[tools]"
			continue
		}
		if !inTools {
			continue
		}
		m := miseToolLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rest := m[2]
		if strings.HasPrefix(rest, "{") {
			i := strings.Index(rest, "version")
			if i < 0 {
				continue
			}
			rest = rest[i+len("version"):]
		}
		if q := quotedRe.FindStringSubmatch(rest); q != nil {
			tool := strings.ToLower(m[1])
			if a, ok := toolAliases[tool]; ok {
				tool = a
			}
			if _, dup := out[tool]; !dup {
				out[tool] = q[1]
			}
		}
	}
	return out
}

// buildToolchainInstallCmd is the shell command that installs and
// globally activates every detected toolchain, in sorted tool order.
// Tool and version are whitelisted at detection; each spec is still
// single-quoted.
func buildToolchainInstallCmd(toolchains map[string]string) string {
	tools := make([]string, 0, len(toolchains))
	for t := range toolchains {
		tools = append(tools, t)
	}
	sort.Strings(tools)
	var b strings.Builder
	b.WriteString("mise use --global")
	for _, t := range tools {
		b.WriteString(" " + shQuote(t+"@"+toolchains[t]))
	}
	return b.String()
}
