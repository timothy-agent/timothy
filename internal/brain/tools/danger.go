package tools

import (
	"regexp"
)

// DangerLevel classifies a shell command's blast radius.
type DangerLevel int

const (
	DangerSafe DangerLevel = iota
	DangerDestructive
)

func (d DangerLevel) String() string {
	if d == DangerDestructive {
		return "destructive"
	}
	return "safe"
}

// dangerThreshold: a command whose matched rule scores sum to this or
// more is destructive and always requires the interactive prompt.
const dangerThreshold = 3

// dangerRule is one scored pattern. Rules are additive: several
// medium-risk matches can add up to destructive.
type dangerRule struct {
	name    string
	score   int
	pattern *regexp.Regexp
}

// dangerRules is the classifier table. Extend here — never inline
// regexes at call sites. Score 3 = destructive on its own; score 2 =
// destructive only in combination.
var dangerRules = []dangerRule{
	{name: "rm", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])rm(\s|$)`)},
	{name: "rmdir", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])rmdir(\s|$)`)},
	{name: "git-push", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])git\s+push(\s|$)`)},
	{name: "git-reset-hard", score: 3, pattern: regexp.MustCompile(`git\s+reset\s+--hard`)},
	{name: "git-clean", score: 3, pattern: regexp.MustCompile(`git\s+clean(\s|$)`)},
	{name: "sudo", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])sudo(\s|$)`)},
	{name: "docker", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])docker(\s|$)`)},
	{name: "truncate", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])truncate(\s|$)`)},
	{name: "dd", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])dd(\s|$)`)},
	{name: "mkfs", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])mkfs`)},
	{name: "shred", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])shred(\s|$)`)},
	{name: "pipe-to-shell", score: 3, pattern: regexp.MustCompile(`(curl|wget)[^|;]*\|\s*(ba|z|da)?sh`)},
	{name: "pkg-install", score: 3, pattern: regexp.MustCompile(`(apk\s+add|apt(-get)?\s+install|yum\s+install|dnf\s+install|brew\s+install|npm\s+i(nstall)?\s+(-g|--global)|pip3?\s+install|gem\s+install|cargo\s+install)`)},
	{name: "redirect-overwrite", score: 3, pattern: regexp.MustCompile(`[^>]>[^>]|[^>]>$`)},
	{name: "chmod-recursive", score: 2, pattern: regexp.MustCompile(`(chmod|chown)\s+(-[a-zA-Z]*R|--recursive)`)},
	{name: "kill", score: 2, pattern: regexp.MustCompile(`(^|[\s;|&(])kill(all)?(\s|$)`)},
	{name: "mv", score: 2, pattern: regexp.MustCompile(`(^|[\s;|&(])mv(\s|$)`)},
	{name: "append-redirect", score: 2, pattern: regexp.MustCompile(`>>`)},
}

// harmlessRedirects are stream-plumbing forms that look like the
// overwrite redirect but destroy nothing: fd duplication (2>&1, >&2)
// and writes to /dev/null. They are blanked before scoring.
var harmlessRedirects = regexp.MustCompile(`[0-9]?>>?\s*/dev/null|[0-9]?>&[0-9]`)

// ClassifyCommand scores a shell command against the danger table and
// returns the level plus the names of matched rules (for the
// permission prompt's rationale).
func ClassifyCommand(command string) (DangerLevel, []string) {
	command = harmlessRedirects.ReplaceAllString(command, " ")
	score := 0
	var matched []string
	for _, r := range dangerRules {
		if r.pattern.MatchString(command) {
			score += r.score
			matched = append(matched, r.name)
		}
	}
	if score >= dangerThreshold {
		return DangerDestructive, matched
	}
	return DangerSafe, matched
}
