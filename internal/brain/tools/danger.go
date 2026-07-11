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
// A command word may appear as a bare name, with an absolute path
// prefix (/bin/rm), or after a shell metacharacter. cmdWord builds a
// pattern that matches the word as an argv[0]-ish token in any of
// those positions.
func cmdWord(word string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[\s;|&(` + "`" + `])(/[\w./-]*/)?` + word + `(\s|$)`)
}

var dangerRules = []dangerRule{
	{name: "rm", score: 3, pattern: cmdWord("rm")},
	{name: "rmdir", score: 3, pattern: cmdWord("rmdir")},
	{name: "git-push", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])git\s+push(\s|$)`)},
	{name: "git-reset-hard", score: 3, pattern: regexp.MustCompile(`git\s+reset\s+--hard`)},
	{name: "git-clean", score: 3, pattern: regexp.MustCompile(`git\s+clean(\s|$)`)},
	{name: "sudo", score: 3, pattern: cmdWord("sudo")},
	{name: "docker", score: 3, pattern: cmdWord("docker")},
	{name: "truncate", score: 3, pattern: cmdWord("truncate")},
	{name: "dd", score: 3, pattern: cmdWord("dd")},
	{name: "mkfs", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])(/[\w./-]*/)?mkfs`)},
	{name: "shred", score: 3, pattern: cmdWord("shred")},
	{name: "find-exec", score: 3, pattern: regexp.MustCompile(`(^|[\s;|&(])find\s.*-(exec|delete)`)},
	{name: "pipe-to-shell", score: 3, pattern: regexp.MustCompile(`(curl|wget|fetch)[^|;]*\|\s*(/[\w./-]*/)?(ba|z|da)?sh`)},
	{name: "pkg-install", score: 3, pattern: regexp.MustCompile(`(apk\s+add|apt(-get)?\s+install|yum\s+install|dnf\s+install|brew\s+install|npm\s+i(nstall)?\s+(-g|--global)|pip3?\s+install|gem\s+install|cargo\s+install)`)},
	// Overwriting redirect, including one that leads the command
	// (> file) — but not fd duplication or /dev/null (blanked below).
	{name: "redirect-overwrite", score: 3, pattern: regexp.MustCompile(`(^|[^>])>([^>]|$)`)},
	{name: "chmod-recursive", score: 2, pattern: regexp.MustCompile(`(chmod|chown)\s+(-[a-zA-Z]*R|--recursive)`)},
	{name: "kill", score: 2, pattern: cmdWord("kill(all)?")},
	{name: "mv", score: 2, pattern: cmdWord("mv")},
	{name: "append-redirect", score: 2, pattern: regexp.MustCompile(`>>`)},
}

// opaqueForms are constructs that hide the real command from a
// string classifier: command/process substitution, env-var command
// indirection (VAR=cmd; $VAR), eval/exec, sourcing, and interpreter
// -c one-liners. A command containing any of these is treated as
// destructive outright — the classifier cannot see what will run, so
// the safe default is to ask (D-010). This turns the classifier's
// blind spots into prompts, not silent auto-approvals.
var opaqueForms = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{name: "command-substitution", pattern: regexp.MustCompile("\\$\\(|`")},
	{name: "process-substitution", pattern: regexp.MustCompile(`<\(|>\(`)},
	// A variable used AS the command — at the start or right after a
	// separator (VAR=rm; $VAR -rf) — hides what runs. A $VAR in an
	// argument position (ls $HOME/logs) is a plain expansion and stays
	// classifiable, so it is NOT flagged here. Newline counts as a
	// separator: sh -c bodies can split commands across lines.
	{name: "variable-command", pattern: regexp.MustCompile(`(^|[;|&(\n]|&&|\|\|)\s*\$\{?[A-Za-z_]`)},
	{name: "eval-exec", pattern: regexp.MustCompile(`(^|[\s;|&(])(eval|exec|source)(\s|$)`)},
	{name: "dot-source", pattern: regexp.MustCompile(`(^|[\s;|&(])\.\s+\S`)},
	{name: "interpreter-c", pattern: regexp.MustCompile(`(^|[\s;|&(])(/[\w./-]*/)?(ba|z|da)?sh\s+-c|(python[23]?|perl|ruby|node)\s+-e?c?`)},
}

// harmlessRedirects are stream-plumbing forms that look like the
// overwrite redirect but destroy nothing: fd duplication (2>&1, >&2)
// and writes to /dev/null. They are blanked before scoring.
var harmlessRedirects = regexp.MustCompile(`[0-9]?>>?\s*/dev/null|[0-9]?>&[0-9]`)

// ifsExpansion collapses ${IFS} / $IFS (a common word-splitting
// obfuscation) back to a space so token patterns still match.
var ifsExpansion = regexp.MustCompile(`\$\{?IFS\}?`)

// ClassifyCommand scores a shell command against the danger table and
// returns the level plus the names of matched rules (for the
// permission prompt's rationale). Obfuscation the classifier cannot
// see through classifies as destructive, not safe.
func ClassifyCommand(command string) (DangerLevel, []string) {
	// Opaque forms first: if the command hides its real payload, the
	// classifier cannot reason about it — force a prompt.
	for _, f := range opaqueForms {
		if f.pattern.MatchString(command) {
			return DangerDestructive, []string{"opaque command (" + f.name + "): cannot be classified, approval required"}
		}
	}

	scored := ifsExpansion.ReplaceAllString(command, " ")
	scored = harmlessRedirects.ReplaceAllString(scored, " ")
	score := 0
	var matched []string
	for _, r := range dangerRules {
		if r.pattern.MatchString(scored) {
			score += r.score
			matched = append(matched, r.name)
		}
	}
	if score >= dangerThreshold {
		return DangerDestructive, matched
	}
	return DangerSafe, matched
}
