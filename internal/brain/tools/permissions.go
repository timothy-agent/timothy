package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Decision is the outcome of the permission chain for one tool call.
type Decision int

const (
	// DecisionAllow: execute without asking.
	DecisionAllow Decision = iota
	// DecisionDeny: hard refusal (policy guard); reported to the
	// model as tool feedback, never overridable.
	DecisionDeny
	// DecisionAsk: park the turn and ask the user interactively.
	DecisionAsk
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	default:
		return "ask"
	}
}

// Resolution carries the decision plus what the permission prompt (or
// the denial message) needs to explain itself.
type Resolution struct {
	Decision Decision
	// Subject is the matched call material: the shell command, the
	// fetched URL, or the tool name.
	Subject string
	// Danger is non-safe only for danger-classified shell commands.
	Danger DangerLevel
	// Rationale names the rule that produced the decision.
	Rationale string
}

// Permissions resolves the D-010 chain, first match wins:
// policy guard (hard deny) → danger classifier (forces the prompt) →
// project allowlist → session grants → interactive prompt.
type Permissions struct {
	db            *pgpool.Pool
	workspaceRoot string
	// tools that never need permission: pure reads with their own
	// guards (webfetch's SSRF blocklist) or no side effects at all.
	exempt map[string]bool
}

func NewPermissions(db *pgpool.Pool, workspaceRoot string) *Permissions {
	return &Permissions{
		db:            db,
		workspaceRoot: workspaceRoot,
		exempt: map[string]bool{
			"current_time":    true,
			"convert_time":    true,
			"calculate":       true,
			"web_fetch":       true,
			"retrieve_output": true,
			"load_skill":      true,
			// remember fires only on the user's explicit ask — a
			// prompt would demand consent for consent. The write is
			// visible and reversible in the memory browser.
			"remember": true,
		},
	}
}

// Resolve runs the chain for one call.
func (p *Permissions) Resolve(ctx context.Context, sessionID, tool string, args json.RawMessage) (Resolution, error) {
	subject := callSubject(tool, args)

	if reason := guardSubject(p.workspaceRoot, tool, subject); reason != "" {
		return Resolution{
			Decision:  DecisionDeny,
			Subject:   subject,
			Rationale: reason,
		}, nil
	}

	if p.exempt[tool] {
		return Resolution{Decision: DecisionAllow, Subject: subject, Rationale: "exempt tool"}, nil
	}

	danger := DangerSafe
	var matchedRules []string
	if tool == "shell" {
		danger, matchedRules = ClassifyCommand(subject)
		if danger == DangerDestructive {
			// Destructive commands are never auto-approved, no
			// matter what the allowlists say.
			return Resolution{
				Decision:  DecisionAsk,
				Subject:   subject,
				Danger:    danger,
				Rationale: "destructive command pattern: " + strings.Join(matchedRules, ", "),
			}, nil
		}
	}

	allowed, rule, err := p.matchGrant(ctx, sessionID, tool, subject)
	if err != nil {
		return Resolution{}, err
	}
	if allowed {
		return Resolution{Decision: DecisionAllow, Subject: subject, Danger: danger, Rationale: rule}, nil
	}

	return Resolution{
		Decision:  DecisionAsk,
		Subject:   subject,
		Danger:    danger,
		Rationale: "no standing grant",
	}, nil
}

// Grant records an "allow for this session" answer.
func (p *Permissions) Grant(ctx context.Context, sessionID, tool, pattern string, ttl time.Duration) error {
	db, err := p.db.Get()
	if err != nil {
		return fmt.Errorf("tools: grant: %w", err)
	}
	if _, err := db.Exec(ctx,
		"INSERT INTO session_grants (session_id, tool, pattern, expires) VALUES ($1, $2, $3, now() + $4::interval)",
		sessionID, tool, pattern, fmt.Sprintf("%d seconds", int64(ttl.Seconds())),
	); err != nil {
		return fmt.Errorf("tools: grant: %w", err)
	}
	return nil
}

// matchGrant checks the project allowlist, then unexpired session
// grants, glob-matching each pattern against the subject.
func (p *Permissions) matchGrant(ctx context.Context, sessionID, tool, subject string) (bool, string, error) {
	db, err := p.db.Get()
	if err != nil {
		return false, "", fmt.Errorf("tools: match grant: %w", err)
	}
	rows, err := db.Query(ctx, `
		SELECT pattern, 'project allowlist' FROM project_allowlist WHERE tool = $2
		UNION ALL
		SELECT pattern, 'session grant' FROM session_grants
		WHERE session_id = $1 AND tool = $2 AND expires > now()`,
		sessionID, tool)
	if err != nil {
		return false, "", fmt.Errorf("tools: match grant: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pattern, source string
		if err := rows.Scan(&pattern, &source); err != nil {
			return false, "", fmt.Errorf("tools: match grant: %w", err)
		}
		if globMatch(pattern, subject) {
			return true, source + " " + pattern, nil
		}
	}
	return false, "", rows.Err()
}

// globMatch matches shell-style patterns. A trailing "*" also matches
// across separators ("git status*" covers "git status --short"), which
// path.Match alone would not.
func globMatch(pattern, subject string) bool {
	if ok, err := path.Match(pattern, subject); err == nil && ok {
		return true
	}
	if prefix, found := strings.CutSuffix(pattern, "*"); found && !strings.ContainsAny(prefix, "*?[") {
		return strings.HasPrefix(subject, prefix)
	}
	return false
}

// callSubject extracts what grants and guards match against.
func callSubject(tool string, args json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return tool
	}
	switch tool {
	case "shell":
		if c, ok := m["command"].(string); ok {
			return c
		}
	case "web_fetch":
		if u, ok := m["url"].(string); ok {
			return u
		}
	}
	return tool
}

// guardPatterns are the hard-deny policy guard (chain step 1): paths
// and names that no grant can unlock. Matched against the call
// subject, case-insensitively.
var guardPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{name: "env files", pattern: regexp.MustCompile(`(?i)(^|[\s/'"=])\.env(\.[A-Za-z0-9._-]+)?([\s'"/]|$)`)},
	{name: "ssh keys", pattern: regexp.MustCompile(`(?i)\.ssh(/|\b)|id_(rsa|ed25519|ecdsa|dsa)\b`)},
	{name: "key material", pattern: regexp.MustCompile(`(?i)\.(pem|key|p12|pfx|keystore)\b`)},
	{name: "credential stores", pattern: regexp.MustCompile(`(?i)(^|/|\s)(credentials?|secrets?)(/|\.|\s|$)|\.aws(/|\b)|\.kube(/|\b)|\.gnupg(/|\b)|\.netrc\b|\.npmrc\b`)},
	{name: "home dotfiles", pattern: regexp.MustCompile(`~/\.[A-Za-z]`)},
	{name: "system dirs", pattern: regexp.MustCompile(`(^|[\s'"=])/(etc|root|proc|sys|dev|boot|var/(run|lib))(/|\s|$)`)},
}

// allowedAbsPrefixes are absolute paths a shell command may name even
// though they sit outside the workspace: stream plumbing only.
var allowedAbsPrefixes = []string{"/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr"}

// guardSubject applies the policy guard to shell commands. Other
// tools have no path-bearing arguments yet; web_fetch has its own
// network guard.
func guardSubject(root, tool, subject string) string {
	if tool != "shell" {
		return ""
	}
	// Blank the allowed stream-plumbing paths first so /dev/null
	// doesn't trip the system-dirs rule.
	for _, p := range allowedAbsPrefixes {
		subject = strings.ReplaceAll(subject, p, " ")
	}
	for _, g := range guardPatterns {
		if g.pattern.MatchString(subject) {
			return "policy guard: " + g.name + " are off-limits"
		}
	}
	if root != "" {
		for _, tok := range commandTokens(subject) {
			// A parent-directory reference in any path-like token can
			// climb out of the workspace once the shell resolves it —
			// the lexical check below can't see where it lands, so a
			// token containing ".." is refused outright.
			if tok == ".." || strings.HasPrefix(tok, "../") ||
				strings.Contains(tok, "/../") || strings.HasSuffix(tok, "/..") {
				return fmt.Sprintf("policy guard: %q uses .. to leave the workspace — use paths under %s", tok, root)
			}
			if !strings.HasPrefix(tok, "/") {
				continue
			}
			if pathWithin(root, tok) {
				continue
			}
			return fmt.Sprintf("policy guard: %s is outside the workspace %s — use paths under the workspace", tok, root)
		}
	}
	return ""
}

// pathWithin is a purely lexical containment check (the workspace may
// not exist where brain's tests run; symlink resolution happens in
// WithinRoot at execution time).
func pathWithin(root, p string) bool {
	cleaned := path.Clean(p)
	return cleaned == root || strings.HasPrefix(cleaned, root+"/")
}

// commandTokens splits a command line the cheap way — whitespace,
// with quotes and redirect prefixes stripped — enough to spot
// absolute paths. It intentionally over-matches: a false hit returns
// corrective feedback, and the model retries with workspace paths.
func commandTokens(command string) []string {
	// '=' splits too, so --output=/abs/path exposes its path part.
	fields := strings.Fields(strings.ReplaceAll(command, "=", " "))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, `'";|&()<>0123456789`)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
