package missions

// Environment facts (issue #1008): one Go-rendered block of what the
// harness knows about the repo, the sandbox and delivery, appended to
// every mission phase prompt so the model stops guessing.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EnvFacts is the probed and scanned part of the facts block, collected
// once at provisioning (and again after a sandbox recreate) and stored
// on missions.env_facts. Repo URL, branch and base commit render from
// the mission's own fields.
type EnvFacts struct {
	BaseBranch   string            `json:"base_branch,omitempty"`
	Destinations []DestinationFact `json:"destinations,omitempty"`
	// Manifests are worktree-relative slash paths, sorted.
	Manifests []string `json:"manifests,omitempty"`
	// Tools are sorted by name; an empty Version means not installed.
	Tools []ToolFact `json:"tools,omitempty"`
	Gaps  []string   `json:"gaps,omitempty"`
	// Prepare is the prepare step's outcome (D-130), nil until it ran.
	Prepare *PrepareFacts `json:"prepare,omitempty"`
	// Lockfile is the latest lockfile evidence (D-140), nil while the
	// diff changes no lockfile.
	Lockfile *LockfileEvidence `json:"lockfile_evidence,omitempty"`
}

// DestinationFact is one repo destination the result phase delivers to.
type DestinationFact struct {
	Kind string `json:"kind"`
	Mode string `json:"mode,omitempty"`
}

// ToolFact is one probed sandbox tool.
type ToolFact struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Sandbox limits as sandboxd applies them (internal/sandboxd/manager.go:
// sandboxMemoryBytes, sandboxNanoCPUs, sandboxPidsLimit, the tmpfs
// sizes, sandboxFsizeLimit). Brain does not import sandboxd; keep these
// in sync when a limit changes there.
const (
	sandboxLimitMemory  = "2 GiB"
	sandboxLimitCPUs    = 2
	sandboxLimitPids    = 256
	sandboxLimitHome    = "1 GiB"
	sandboxLimitTmp     = "512 MiB"
	sandboxLimitFileMax = "256 MiB"
)

// manifestMaxDepth bounds the manifest walk: files up to three
// directories below the worktree root.
const manifestMaxDepth = 3

// manifestCap bounds how many manifest paths are stored and rendered.
const manifestCap = 60

// manifestScanBytes bounds how much of one manifest the gap scan reads.
const manifestScanBytes = 256 << 10

// manifestNames are the manifest and lockfile basenames the walk lists.
var manifestNames = map[string]bool{
	"composer.json": true, "composer.lock": true,
	"package.json": true, "package-lock.json": true, "npm-shrinkwrap.json": true,
	"yarn.lock": true, "pnpm-lock.yaml": true, "bun.lock": true, "bun.lockb": true,
	"go.mod": true, "go.sum": true, "go.work": true,
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true,
	"settings.gradle": true, "settings.gradle.kts": true, "gradle.lockfile": true,
	"pyproject.toml": true, "requirements.txt": true, "setup.py": true, "setup.cfg": true,
	"Pipfile": true, "Pipfile.lock": true, "poetry.lock": true, "uv.lock": true,
	"Gemfile": true, "Gemfile.lock": true,
	"Cargo.toml": true, "Cargo.lock": true,
	"mise.toml": true, ".mise.toml": true, ".tool-versions": true,
}

// dotnetExts are .NET project file extensions; a repo whose manifests
// are only these and include a .sln is reported as Windows-only.
var dotnetExts = map[string]bool{".sln": true, ".csproj": true, ".vbproj": true, ".fsproj": true}

// probedTools are the image tools the facts probe asks for a version,
// sorted by name. args is what prints the version.
var probedTools = []struct{ name, args string }{
	{"cargo", "--version"},
	{"composer", "--version"},
	{"docker", "--version"},
	{"gh", "--version"},
	{"git", "--version"},
	{"go", "version"},
	{"java", "--version"},
	{"mise", "--version"},
	{"mvn", "--version"},
	{"node", "--version"},
	{"npm", "--version"},
	{"php", "--version"},
	{"pnpm", "--version"},
	{"python3", "--version"},
	{"ruby", "--version"},
	{"yarn", "--version"},
}

// toolProbeTimeout bounds the whole probe exec.
const toolProbeTimeout = 60 * time.Second

// toolVersionCap bounds one stored version line.
const toolVersionCap = 80

// walkManifests lists manifest and lockfile paths under root to
// manifestMaxDepth, skipping vendor/, node_modules/ and dot dirs.
// Symlinks are not followed. Sorted, capped at manifestCap.
func walkManifests(root string) []string {
	if root == "" {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			name := d.Name()
			if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if strings.Count(rel, string(filepath.Separator)) >= manifestMaxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		if manifestNames[name] || dotnetExts[strings.ToLower(filepath.Ext(name))] {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	if len(out) > manifestCap {
		out = out[:manifestCap]
	}
	return out
}

// detectEnvGaps names what the sandbox cannot do for this repo:
// Testcontainers (needs a Docker daemon) and a Windows-only .NET
// solution with no other manifest.
func detectEnvGaps(root string, manifests []string) []string {
	var gaps []string
	var tc []string
	hasSln, onlyDotnet := false, len(manifests) > 0
	for _, rel := range manifests {
		ext := strings.ToLower(path.Ext(rel))
		if ext == ".sln" {
			hasSln = true
		}
		if !dotnetExts[ext] {
			onlyDotnet = false
		}
		if strings.HasSuffix(rel, ".lock") || strings.HasSuffix(rel, ".sum") || strings.HasSuffix(rel, "-lock.json") || strings.HasSuffix(rel, "lock.yaml") {
			continue
		}
		if fileMentions(filepath.Join(root, filepath.FromSlash(rel)), "testcontainers") {
			tc = append(tc, rel)
		}
	}
	if len(tc) > 0 {
		gaps = append(gaps, "Testcontainers is declared in "+strings.Join(tc, ", ")+"; it needs a Docker daemon and the sandbox has none, so tests that start containers cannot run here.")
	}
	if hasSln && onlyDotnet {
		gaps = append(gaps, "The only project files are a .NET solution ("+strings.Join(manifests, ", ")+") with no other manifest; the sandbox is Linux with no .NET SDK, so a Windows-only build cannot run here.")
	}
	return gaps
}

// fileMentions reports whether the first manifestScanBytes of file
// contain needle, case-insensitively.
func fileMentions(file, needle string) bool {
	f, err := os.Open(file) //nolint:gosec // file is a manifest path found by walkManifests under the mission worktree
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, manifestScanBytes))
	if err != nil {
		return false
	}
	return bytes.Contains(bytes.ToLower(b), []byte(needle))
}

// buildToolProbeCmd prints one "name<TAB>version" line per probed tool,
// an empty version when the tool is not on PATH. Each version call is
// bounded by timeout and reads no stdin.
func buildToolProbeCmd() string {
	var b strings.Builder
	for _, t := range probedTools {
		fmt.Fprintf(&b, "if command -v %[1]s >/dev/null 2>&1; then printf '%%s\\t%%s\\n' %[1]s \"$(timeout 10 %[1]s %[2]s </dev/null 2>&1 | awk 'NF{print;exit}')\"; else printf '%%s\\t\\n' %[1]s; fi\n", t.name, t.args)
	}
	return b.String()
}

// parseToolProbe reads buildToolProbeCmd's output into ToolFacts in
// probedTools order. A tool missing from the output is left out.
func parseToolProbe(out string) []ToolFact {
	seen := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, version, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		version = strings.TrimSpace(version)
		if len(version) > toolVersionCap {
			version = version[:toolVersionCap]
		}
		seen[name] = version
	}
	var facts []ToolFact
	for _, t := range probedTools {
		if v, ok := seen[t.name]; ok {
			facts = append(facts, ToolFact{Name: t.name, Version: v})
		}
	}
	return facts
}

// collectEnvFacts gathers a coding mission's facts: base branch, repo
// destinations, manifests, gaps and probed tools, and stores them.
// Best effort: a failed probe leaves Tools empty, a failed store is
// logged. Returns the facts it collected.
func (p *provisioner) collectEnvFacts(ctx context.Context, m Mission, workRoot, baseBranch string) *EnvFacts {
	if m.Kind != KindCoding {
		return nil
	}
	facts := &EnvFacts{BaseBranch: baseBranch, Destinations: p.repoDestinations(ctx, m)}
	if wt := m.WorktreePath(); wt != "" {
		facts.Manifests = walkManifests(wt)
		facts.Gaps = detectEnvGaps(wt, facts.Manifests)
	}
	if p.sandboxExec != nil {
		var out bytes.Buffer
		code, err := p.sandboxExec(ctx, m.ID, m.Environment, workRoot, buildToolProbeCmd(), toolProbeTimeout, &out)
		if err != nil || code != 0 {
			p.log.Warn("driver: tool probe failed; facts carry no tool versions", "mission_id", m.ID, "exit_code", code, "error", err)
		} else {
			facts.Tools = parseToolProbe(out.String())
		}
	}
	if err := p.store.SetEnvFacts(ctx, m.ID, *facts); err != nil {
		p.log.Warn("driver: store environment facts failed", "mission_id", m.ID, "error", err)
	}
	return facts
}

// repoDestinations lists the mission's repo destinations with kind and
// mode, as the resolver reports them; an entry with only a repo_url is
// kind "repo" with no mode.
func (p *provisioner) repoDestinations(ctx context.Context, m Mission) []DestinationFact {
	var out []DestinationFact
	for _, e := range m.Destinations {
		if e.DestinationID != "" && p.resolveGitHubPolicy != nil {
			if pol, ok, err := p.resolveGitHubPolicy(ctx, e.DestinationID); err == nil && ok {
				out = append(out, DestinationFact{Kind: pol.Kind, Mode: pol.Mode})
				continue
			}
		}
		if e.RepoURL != "" {
			out = append(out, DestinationFact{Kind: "repo"})
		}
	}
	return out
}

// defaultBaseBranch is the cloned default branch name (origin/HEAD),
// "" when there is no clone or it cannot be read.
func defaultBaseBranch(ctx context.Context, worktree string) string {
	if worktree == "" {
		return ""
	}
	out, err := runGit(ctx, worktree, "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(out), "origin/")
}

// deliveryText is the fixed delivery sentence for one destination mode.
func deliveryText(mode, branch string) string {
	b := "`" + branch + "`"
	switch mode {
	case "push_pr":
		return "the harness commits each unit, pushes " + b + " and opens the PR after all units pass; do not plan, script or verify a push or PR; never create or switch branches."
	case "push":
		return "the harness commits each unit and pushes " + b + " after all units pass; do not plan, script or verify a push or PR; never create or switch branches."
	default:
		return "the harness commits each unit and delivers " + b + " after all units pass; do not plan, script or verify a push or PR; never create or switch branches."
	}
}

// renderEnvFacts renders the facts block for m: deterministic, sorted,
// every repo- or sandbox-derived string through NeutralizeSlot.
func renderEnvFacts(m Mission) string {
	var b strings.Builder
	b.WriteString("\n\nEnvironment facts (set by the harness; treat them as true and do not re-check them):\n")
	var facts EnvFacts
	if m.EnvFacts != nil {
		facts = *m.EnvFacts
	}
	if repo := m.RepoURL(); repo != "" {
		fmt.Fprintf(&b, "- Repository: %s", NeutralizeSlot(repo))
		if facts.BaseBranch != "" {
			fmt.Fprintf(&b, ", base branch %s", NeutralizeSlot(facts.BaseBranch))
		}
		if m.BaseCommit != "" {
			fmt.Fprintf(&b, ", base commit %s", NeutralizeSlot(shortSHA(m.BaseCommit)))
		}
		b.WriteString(".\n")
	}
	if m.Branch != "" {
		fmt.Fprintf(&b, "- Mission branch: %s, already checked out in the worktree.\n", NeutralizeSlot(m.Branch))
		if m.EnvFacts != nil && len(facts.Destinations) == 0 {
			fmt.Fprintf(&b, "- Delivery: no repository destination. The harness commits each unit on `%s`; never push, never create or switch branches.\n", NeutralizeSlot(m.Branch))
		}
		for _, d := range facts.Destinations {
			mode := d.Mode
			if mode == "" {
				mode = "unknown"
			}
			fmt.Fprintf(&b, "- Destination %s, mode %s: %s\n", NeutralizeSlot(d.Kind), NeutralizeSlot(mode), deliveryText(d.Mode, NeutralizeSlot(m.Branch)))
		}
	}
	b.WriteString("- Credentials: the sandbox has no git or registry credentials; git push, gh and authenticated registry calls cannot work.\n")
	if len(facts.Manifests) > 0 {
		b.WriteString("- Manifests and lockfiles (to depth 3, by directory):\n")
		b.WriteString(renderManifestDirs(facts.Manifests))
	}
	if len(m.Toolchains) > 0 {
		fmt.Fprintf(&b, "- Toolchains the repo pins (the harness installs them through mise before discover): %s.\n", NeutralizeSlot(toolchainSummary(m.Toolchains)))
	}
	if len(facts.Tools) > 0 {
		var have, absent []string
		for _, t := range facts.Tools {
			if t.Version == "" {
				absent = append(absent, t.Name)
				continue
			}
			have = append(have, t.Name+" ("+NeutralizeSlot(t.Version)+")")
		}
		if len(have) > 0 {
			fmt.Fprintf(&b, "- Installed tools: %s.\n", strings.Join(have, "; "))
		}
		if len(absent) > 0 {
			fmt.Fprintf(&b, "- Not installed: %s.\n", strings.Join(absent, ", "))
		}
	}
	b.WriteString(renderPrepareFacts(facts.Prepare))
	fmt.Fprintf(&b, "- Sandbox limits: unprivileged user (no root, no sudo); read-only root filesystem, writable only in the workspace, HOME (tmpfs %s; package caches in ~/.cache are on disk) and /tmp (tmpfs %s); memory %s including swap; %d CPUs; %d processes; %s per file; network on; no Docker socket.\n",
		sandboxLimitHome, sandboxLimitTmp, sandboxLimitMemory, sandboxLimitCPUs, sandboxLimitPids, sandboxLimitFileMax)
	for _, g := range facts.Gaps {
		fmt.Fprintf(&b, "- Gap: %s If the goal depends on this, submit the plan as infeasible.\n", NeutralizeSlot(g))
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderManifestDirs groups manifest paths by directory, one line per
// directory in sorted order.
func renderManifestDirs(manifests []string) string {
	byDir := map[string][]string{}
	var dirs []string
	for _, rel := range manifests {
		dir, name := path.Split(rel)
		dir = strings.TrimSuffix(dir, "/")
		if dir == "" {
			dir = "."
		}
		if _, ok := byDir[dir]; !ok {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], name)
	}
	sort.Strings(dirs)
	var b strings.Builder
	for _, dir := range dirs {
		names := byDir[dir]
		sort.Strings(names)
		fmt.Fprintf(&b, "  - %s: %s\n", NeutralizeSlot(dir), NeutralizeSlot(strings.Join(names, ", ")))
	}
	return b.String()
}

// shortSHA trims a commit id to 12 characters.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
