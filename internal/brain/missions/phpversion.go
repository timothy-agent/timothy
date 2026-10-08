package missions

// Baked PHP minor selection (D-127, D-139): composer constraints are
// matched against the minors the php image bakes, newest first, so a
// mission runs the newest PHP that composer.json and composer.lock both
// allow.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// composerLockMaxBytes caps the composer.lock read; a lock is larger
// than readMarker's 1 MiB cap often enough to matter.
const composerLockMaxBytes = 16 << 20

var (
	composerOpSpaceRe = regexp.MustCompile(`(>=|<=|!=|==|>|<|=|\^|~)\s+`)
	composerClauseRe  = regexp.MustCompile(`^(>=|<=|!=|==|>|<|=|\^|~)?v?(\d+)(?:\.(\d+|\*|x))?(?:\.(\d+|\*|x))?(?:\.\d+)?$`)
)

// phpSatisfies reports whether a baked minor ("8.3", taken at its newest
// patch) satisfies a composer constraint: "|" or "||" alternatives of
// clauses joined by "," or spaces, each "^", "~", ">=", ">", "<=", "<",
// "!=" (ignored), exact or wildcard. ok is false when any clause does
// not parse (hyphen ranges included).
func phpSatisfies(constraint, minor string) (sat, ok bool) {
	major, min, _ := strings.Cut(minor, ".")
	v := [3]int{atoi(major), atoi(min), 1 << 20}
	c := strings.TrimSpace(strings.ReplaceAll(constraint, "||", "|"))
	if c == "" {
		return false, false
	}
	for _, alt := range strings.Split(c, "|") {
		clauses := strings.Fields(strings.ReplaceAll(composerOpSpaceRe.ReplaceAllString(alt, "$1"), ",", " "))
		if len(clauses) == 0 {
			return false, false
		}
		all := true
		for _, cl := range clauses {
			s, ok := phpClause(cl, v)
			if !ok {
				return false, false
			}
			all = all && s
		}
		sat = sat || all
	}
	return sat, true
}

// phpClause matches one composer clause against v.
func phpClause(cl string, v [3]int) (sat, ok bool) {
	cl, _, _ = strings.Cut(cl, "@")
	if cl == "*" {
		return true, true
	}
	m := composerClauseRe.FindStringSubmatch(cl)
	if m == nil {
		return false, false
	}
	op := m[1]
	var lo [3]int
	n := 0
	for i, p := range m[2:5] {
		if p == "" || p == "*" || p == "x" {
			break
		}
		lo[i] = atoi(p)
		n++
	}
	switch op {
	case "^":
		up := [3]int{lo[0] + 1, 0, 0}
		if lo[0] == 0 && n >= 2 && lo[1] > 0 {
			up = [3]int{0, lo[1] + 1, 0}
		} else if lo[0] == 0 && n >= 2 {
			up = [3]int{0, 0, lo[2] + 1}
		}
		return cmpVersion(v, lo) >= 0 && cmpVersion(v, up) < 0, true
	case "~":
		up := [3]int{lo[0] + 1, 0, 0}
		if n == 3 {
			up = [3]int{lo[0], lo[1] + 1, 0}
		}
		return cmpVersion(v, lo) >= 0 && cmpVersion(v, up) < 0, true
	case ">=":
		return cmpVersion(v, lo) >= 0, true
	case ">":
		return cmpVersion(v, lo) > 0, true
	case "<=":
		return cmpVersion(v, lo) <= 0, true
	case "<":
		return cmpVersion(v, lo) < 0, true
	case "!=":
		return true, true
	}
	// Exact or wildcard: the patch of a baked minor is not known, so
	// only major and minor are compared.
	return v[0] == lo[0] && (n < 2 || v[1] == lo[1]), true
}

func cmpVersion(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// newestBakedPHP is the newest baked minor every constraint allows, ""
// when none does. A constraint that does not parse is ignored.
func newestBakedPHP(constraints ...string) string {
	for i := len(phpMinors) - 1; i >= 0; i-- {
		all := true
		for _, c := range constraints {
			if sat, ok := phpSatisfies(c, phpMinors[i]); ok && !sat {
				all = false
				break
			}
		}
		if all {
			return phpMinors[i]
		}
	}
	return ""
}

// normalizePHPVersion reduces a composer php constraint to a minor: the
// newest baked minor that satisfies it (D-139), else the minor the
// constraint names, which the install then reports as not baked. A
// constraint that does not parse is skipped.
func normalizePHPVersion(c string) (string, bool) {
	c = strings.TrimSpace(c)
	if _, ok := phpSatisfies(c, phpMinors[0]); !ok {
		return "", false
	}
	if v := newestBakedPHP(c); v != "" {
		return v, true
	}
	m := versionClauseRe.FindStringSubmatch(c)
	if m == nil {
		return "", false
	}
	major, rest, _ := strings.Cut(m[2], ".")
	minor, _, _ := strings.Cut(rest, ".")
	if minor == "" {
		minor = "0"
	}
	return major + "." + minor, true
}

// composerPHP picks the php minor for a composer project: config.platform.php
// wins; otherwise the newest baked minor that composer.json's require.php
// and every composer.lock package's require.php allow. When no baked
// minor satisfies the lock too, it falls back to composer.json alone and
// returns a note for the environment facts. "" when nothing pins php.
func composerPHP(worktree string) (version, note string) {
	var composer struct {
		Require map[string]string `json:"require"`
		Config  struct {
			Platform map[string]string `json:"platform"`
		} `json:"config"`
	}
	if json.Unmarshal([]byte(readMarker(worktree, "composer.json")), &composer) != nil {
		return "", ""
	}
	if p := composer.Config.Platform["php"]; p != "" {
		v, _ := normalizePHPVersion(p)
		return v, ""
	}
	req := composer.Require["php"]
	if _, ok := phpSatisfies(req, phpMinors[0]); !ok {
		return "", ""
	}
	locked := composerLockPHP(worktree)
	if len(locked) > 0 {
		cs := []string{req}
		for _, c := range locked {
			cs = append(cs, c)
		}
		if v := newestBakedPHP(cs...); v != "" {
			return v, ""
		}
	}
	v, _ := normalizePHPVersion(req)
	if len(locked) == 0 {
		return v, ""
	}
	var blocking []string
	for name, c := range locked {
		if sat, ok := phpSatisfies(c, v); ok && !sat {
			blocking = append(blocking, name+" ("+c+")")
		}
	}
	sort.Strings(blocking)
	if len(blocking) > 5 {
		blocking = append(blocking[:5], fmt.Sprintf("%d more", len(blocking)-5))
	}
	return v, fmt.Sprintf("No baked PHP (%s to %s) satisfies composer.json's php %q and every package in composer.lock; PHP %s satisfies composer.json alone, but composer install from the lock may fail until these are updated: %s.",
		phpMinors[0], phpMinors[len(phpMinors)-1], req, v, strings.Join(blocking, ", "))
}

// composerLockPHP maps each locked package (packages and packages-dev)
// to its require.php, nil when there is no readable composer.lock.
func composerLockPHP(worktree string) map[string]string {
	f, err := os.Open(filepath.Join(worktree, "composer.lock")) //nolint:gosec // fixed name at the worktree root
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, composerLockMaxBytes))
	if err != nil {
		return nil
	}
	type pkg struct {
		Name    string            `json:"name"`
		Require map[string]string `json:"require"`
	}
	var lock struct {
		Packages    []pkg `json:"packages"`
		PackagesDev []pkg `json:"packages-dev"`
	}
	if json.Unmarshal(b, &lock) != nil {
		return nil
	}
	out := map[string]string{}
	for _, p := range append(lock.Packages, lock.PackagesDev...) {
		if c := strings.TrimSpace(p.Require["php"]); c != "" {
			out[p.Name] = c
		}
	}
	return out
}
