package missions

// Package-level lockfile summaries for the review packet (D-140): the
// baseline diff leaves lockfiles out, so the reviewer sees each changed
// lockfile as package, old version, new version instead of raw hunks.
// composer.lock and npm lockfiles are parsed; any other lockfile gets a
// line count.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// lockSummaryCap bounds the package lines rendered per lockfile.
const lockSummaryCap = 40

// packageChange is one package's version change; Old or New is empty
// for an added or removed package.
type packageChange struct {
	Name, Old, New string
}

// lockPackages reads package name to version from a lockfile's content.
// ok is false for a format it does not parse.
func lockPackages(base string, raw []byte) (map[string]string, bool) {
	out := map[string]string{}
	if len(raw) == 0 {
		return out, true
	}
	switch base {
	case "composer.lock":
		var doc struct {
			Packages    []struct{ Name, Version string } `json:"packages"`
			PackagesDev []struct{ Name, Version string } `json:"packages-dev"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			return nil, false
		}
		for _, p := range append(doc.Packages, doc.PackagesDev...) {
			out[p.Name] = p.Version
		}
		return out, true
	case "package-lock.json", "npm-shrinkwrap.json":
		var doc struct {
			Packages     map[string]struct{ Version string } `json:"packages"`
			Dependencies map[string]struct{ Version string } `json:"dependencies"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			return nil, false
		}
		if len(doc.Packages) > 0 {
			for key, p := range doc.Packages {
				if name := strings.TrimPrefix(key, "node_modules/"); key != "" && name != key {
					out[name] = p.Version
				}
			}
			return out, true
		}
		for name, p := range doc.Dependencies {
			out[name] = p.Version
		}
		return out, true
	}
	return nil, false
}

// diffPackages lists the packages whose version differs, by name.
func diffPackages(before, after map[string]string) []packageChange {
	var out []packageChange
	for name, old := range before {
		if now := after[name]; now != old {
			out = append(out, packageChange{Name: name, Old: old, New: now})
		}
	}
	for name, now := range after {
		if _, ok := before[name]; !ok {
			out = append(out, packageChange{Name: name, New: now})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// summarizeLockfile renders one lockfile's change: package lines when
// both sides parse, else "lockfile changed, N lines" (lines < 0 when
// unknown or binary).
func summarizeLockfile(file string, before, after []byte, lines int) string {
	base := path.Base(file)
	old, okOld := lockPackages(base, before)
	now, okNew := lockPackages(base, after)
	if !okOld || !okNew {
		if lines < 0 {
			return fmt.Sprintf("%s: lockfile changed\n", file)
		}
		return fmt.Sprintf("%s: lockfile changed, %d lines\n", file, lines)
	}
	changes := diffPackages(old, now)
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d packages changed\n", file, len(changes))
	for i, c := range changes {
		if i == lockSummaryCap {
			fmt.Fprintf(&b, "- ... %d more\n", len(changes)-i)
			break
		}
		switch {
		case c.Old == "":
			fmt.Fprintf(&b, "- %s: added %s\n", c.Name, c.New)
		case c.New == "":
			fmt.Fprintf(&b, "- %s: removed %s\n", c.Name, c.Old)
		default:
			fmt.Fprintf(&b, "- %s: %s -> %s\n", c.Name, c.Old, c.New)
		}
	}
	return b.String()
}

// lockfileSummaries renders every changed lockfile's summary for the
// review packet, old content from baseCommit, new from the worktree.
func lockfileSummaries(ctx context.Context, wt, baseCommit string, lockfiles []string) string {
	var b strings.Builder
	for _, f := range lockfiles {
		before, _ := gitStdout(ctx, wt, "show", baseCommit+":"+f)
		after, _ := os.ReadFile(filepath.Join(wt, filepath.FromSlash(f))) //nolint:gosec // a path from git diff inside the worktree
		b.WriteString(summarizeLockfile(f, before, after, numstatLines(ctx, wt, baseCommit, f)))
	}
	return b.String()
}

// numstatLines is added plus removed lines for one file since
// baseCommit, -1 for a binary file or when git cannot answer.
func numstatLines(ctx context.Context, wt, baseCommit, file string) int {
	out, err := gitStdout(ctx, wt, "diff", "--numstat", baseCommit, "--", file)
	fields := strings.Fields(string(out))
	if err != nil || len(fields) < 2 {
		return -1
	}
	added, errA := strconv.Atoi(fields[0])
	removed, errR := strconv.Atoi(fields[1])
	if errA != nil || errR != nil {
		return -1
	}
	return added + removed
}

// gitStdout runs git in dir under gitOpTimeout and returns stdout only.
func gitStdout(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...) //nolint:gosec // fixed subcommands; paths come from git diff output, passed after -- or as a rev:path
	cmd.Dir = dir
	return cmd.Output()
}
