package missions

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultBranchPattern is the built-in branch template — the exact
// shape Provision always used before this existed ("<type>/<slug>").
const DefaultBranchPattern = "{type}/{slug}"

// maxBranchLen caps an expanded branch pattern the same as maxSlugLen
// bounded the old fixed "<type>/<slug>" shape: type (<=7 chars) + "/" +
// slug (<=40 chars) never exceeded ~50 chars, so a template with more
// placeholders (login, date) gets a generous but still bounded cap.
const maxBranchLen = 200

// branchRefPattern is the charset a fully-expanded branch name must
// satisfy — a conservative subset of what git actually allows,
// intentionally excluding "~^:?*[\" and control/space characters that
// are technically legal in some git ref forms but never desirable in a
// generated branch name.
var branchRefPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// branchPlaceholder matches any {word} token so ValidateBranchPattern
// can reject unknown placeholders before one ever reaches expansion.
var branchPlaceholder = regexp.MustCompile(`\{[a-z]+\}`)

// knownBranchPlaceholders is the exhaustive set ExpandBranchPattern
// understands. {type} is the CommitType heuristic, {slug} the existing
// Slug, {login} the mission connection's GitHub login (empty for a
// non-github mission), {date} the mission's creation date (YYYYMMDD).
var knownBranchPlaceholders = map[string]bool{
	"{type}": true, "{slug}": true, "{login}": true, "{date}": true,
}

// ValidateBranchPattern rejects a template that could produce an unsafe
// or malformed branch name, checked against dummy placeholder values so
// the same rules apply whether {login} ends up empty or not. Rules:
// only known placeholders, and after expansion — a valid git ref
// fragment (charset [A-Za-z0-9._/-], no "..", no leading/trailing
// slash, within maxBranchLen).
func ValidateBranchPattern(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("branch pattern must not be empty")
	}
	for _, m := range branchPlaceholder.FindAllString(pattern, -1) {
		if !knownBranchPlaceholders[m] {
			return fmt.Errorf("branch pattern: unknown placeholder %q", m)
		}
	}
	// Two probes: {login} present and {login} empty — collapseSlashes'
	// behavior differs between the two, and both must independently
	// produce a valid ref.
	for _, login := range []string{"octocat", ""} {
		expanded := ExpandBranchPattern(pattern, "feat", "example-slug", login, "20260101")
		if err := validateExpandedRef(expanded); err != nil {
			return fmt.Errorf("branch pattern: %w (example expansion %q)", err, expanded)
		}
	}
	return nil
}

// validateExpandedRef checks one already-expanded branch name against
// the git-ref-fragment safety rules ValidateBranchPattern enforces.
func validateExpandedRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("expands to an empty branch name")
	}
	if len(ref) > maxBranchLen {
		return fmt.Errorf("expands to a branch name longer than %d chars", maxBranchLen)
	}
	if !branchRefPattern.MatchString(ref) {
		return fmt.Errorf("expands to %q, outside the allowed charset [A-Za-z0-9._/-]", ref)
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf("expands to %q, containing \"..\"", ref)
	}
	if strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") {
		return fmt.Errorf("expands to %q, with a leading or trailing slash", ref)
	}
	return nil
}

// ExpandBranchPattern substitutes {type}/{slug}/{login}/{date} into
// pattern, then collapses any run of slashes the substitution produced
// (an empty {login} is the common cause — "feat//my-slug" would
// otherwise be left in the branch name) and trims any leading/trailing
// slash left behind.
func ExpandBranchPattern(pattern, typ, slug, login, date string) string {
	r := strings.NewReplacer(
		"{type}", typ,
		"{slug}", slug,
		"{login}", login,
		"{date}", date,
	)
	expanded := r.Replace(pattern)
	return collapseSlashes(expanded)
}

// slashRun collapses "//", "///", etc. down to a single "/".
var slashRun = regexp.MustCompile(`/{2,}`)

func collapseSlashes(s string) string {
	s = slashRun.ReplaceAllString(s, "/")
	return strings.Trim(s, "/")
}

// Commit styles: "conventional" (default, "<type>: <subject>") or
// "plain" (the unit title as-is, still length/body-preserving).
const (
	CommitStyleConventional = "conventional"
	CommitStylePlain        = "plain"
)

// ValidateCommitStyle rejects anything besides the known styles; empty
// is valid and means "use the effective default."
func ValidateCommitStyle(style string) error {
	switch style {
	case "", CommitStyleConventional, CommitStylePlain:
		return nil
	default:
		return fmt.Errorf("commit style: unknown value %q", style)
	}
}
