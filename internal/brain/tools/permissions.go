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
			"get_current_time": true,
			"convert_time":     true,
			"calculate":        true,
			"fetch_url":        true,
			"search_web":       true,
			"retrieve_output":  true,
			"load_skill":       true,
			// remember fires only on the user's explicit ask — a
			// prompt would demand consent for consent. The write is
			// visible and reversible in the memory browser.
			"remember": true,
			// Mission protocol sentinels: pure argument parsing, zero
			// side effects — their Execute just records a verdict for
			// the harness. Asking a human to approve the harness's own
			// protocol parked every mission's first turn for nothing.
			"mission_status": true,
			"review_verdict": true,
			"submit_plan":    true,
			"discover_notes": true,
			// ask_user (D-088) is the same class: its Execute only
			// records the question and parks the mission for the
			// operator, who IS the permission authority. Routing it
			// through the permission chain double-parks the mission on
			// a prompt about asking a question.
			"ask_user": true,
			// write_file is root-confined by construction (relative
			// paths only, .. rejected, root fixed at registration) —
			// there is nothing for a prompt to guard that the tool
			// doesn't already enforce harder.
			"write_file": true,
			// list_missions/get_mission are pure reads over the missions
			// store (list / status snapshot) — zero side effects, same
			// reasoning as search_web. push_mission_branch is
			// deliberately NOT here: it must always ask (see
			// PushMissionBranch's doc comment).
			"list_missions": true,
			"get_mission":   true,
			// search_kb is a pure read scoped to collections bound in Go
			// at construction (D-060), never model input — same reasoning
			// as search_web/missions.
			"search_kb": true,
			// read_kb is the same pure read, one document at a time,
			// with the collection allowlist bound the same way.
			"read_kb": true,
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
		// D-050: an opaque command (interpreter -c/-e, command
		// substitution, eval, ...) is unclassifiable to the lexical
		// scorer, not proven destructive — outside a sandbox the safe
		// default is still to ask, but a session with a REGISTERED
		// SANDBOX (a mission's per-mission Docker container; see
		// SandboxGrantTool) already confines whatever the opaque command
		// turns out to do to resource-capped, workspace-only execution.
		// The container is the actual confinement boundary there, not
		// this classifier's ability to read the command — so opacity
		// alone no longer forces a human prompt for that session, and
		// the command reclassifies to safe and falls through to the
		// mission's own standing grant (AutoApproveTools' "shell" grant)
		// exactly like any other safe command. Chat sessions never
		// register a sandbox, so this never changes chat's behavior.
		// Explicit destructive patterns (rm -rf, git push, chmod -R,
		// ...) are NOT opaque and are untouched by this branch — they
		// keep going through sandboxAllows' narrower, path-scoped
		// downgrade below, sandboxed or not.
		if danger == DangerDestructive && IsOpaqueRationale(matchedRules) && p.sandboxFor(ctx, sessionID) != "" {
			danger, matchedRules = DangerSafe, nil
		}
		if danger == DangerDestructive && !p.sandboxAllows(ctx, sessionID, subject, matchedRules) {
			// Destructive commands are never auto-approved, no matter
			// what the allowlists say — UNLESS the session has a
			// registered sandbox and the command's destruction is
			// provably confined to it (see sandboxAllows).
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

// SandboxGrantTool is the reserved session_grants tool name whose
// pattern column carries a session's sandbox root directory instead
// of a command pattern. A mission's hidden session registers its own
// workspace here (Grant(sessionID, SandboxGrantTool, root, ttl)):
// destructive shell commands whose blast radius is provably confined
// to that root skip the interactive prompt and fall through to normal
// grant matching. Reusing session_grants keeps the mapping shared
// across Permissions instances and process restarts with no schema
// change; the "__" prefix keeps it from ever colliding with a real
// tool name.
const SandboxGrantTool = "__sandbox__"

// sandboxDowngradeable names the danger rules whose destruction is
// file-scoped — confined to whatever paths the command names. Rules
// NOT here (sudo, docker, git-push, pipe-to-shell, pkg-install, dd,
// mkfs, opaque forms...) always keep the prompt: their blast radius
// is not a path inside the sandbox.
var sandboxDowngradeable = map[string]bool{
	"redirect-overwrite": true,
	"append-redirect":    true,
	"rm":                 true,
	"rmdir":              true,
	"mv":                 true,
	"truncate":           true,
	"shred":              true,
	"find-exec":          true,
	"chmod-recursive":    true,
}

// sandboxAllows reports whether a destructive-classified shell
// command may skip the interactive prompt because (1) the session has
// a registered sandbox root, (2) every matched danger rule is
// file-scoped, and (3) every absolute path the command names sits
// inside that root — relative paths resolve against the sandbox
// itself, since a sandboxed session's shell runs rooted there. The
// policy guard (.. rejection, off-limits paths) already ran before
// this is consulted.
func (p *Permissions) sandboxAllows(ctx context.Context, sessionID, subject string, matchedRules []string) bool {
	for _, r := range matchedRules {
		if !sandboxDowngradeable[r] {
			return false
		}
	}
	root := p.sandboxFor(ctx, sessionID)
	if root == "" {
		return false
	}
	for _, tok := range CommandTokens(subject) {
		if strings.HasPrefix(tok, "/") && !pathWithin(root, tok) {
			return false
		}
	}
	return true
}

// sandboxFor returns the session's registered sandbox root, or "".
func (p *Permissions) sandboxFor(ctx context.Context, sessionID string) string {
	if p.db == nil {
		return ""
	}
	db, err := p.db.Get()
	if err != nil {
		return ""
	}
	var root string
	err = db.QueryRow(ctx,
		"SELECT pattern FROM session_grants WHERE session_id = $1 AND tool = $2 AND expires > now() ORDER BY expires DESC LIMIT 1",
		sessionID, SandboxGrantTool).Scan(&root)
	if err != nil {
		return ""
	}
	return root
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
//
// D-036: a grant row's tool also matches when the call's tool name
// ENDS WITH "_"+rowTool — connector tools are namespaced
// "<connector-name>_<tool-name>" (connectors.Manager.Tools), so an
// allowlist entry like "list_calendar_events" (agent-authored, before
// any connector name is known) still hits
// "google-calendar_list_calendar_events" at call time. Same suffix
// semantics as loop.Agent.SetForceRoute, for the same reason. The
// SandboxGrantTool sentinel ("__sandbox__") is excluded — it is never
// a real tool call, only a stored sandbox root, and its leading "__"
// can never be a legitimate "_"-boundary suffix match target anyway
// since no call tool ends with "__sandbox__".
func (p *Permissions) matchGrant(ctx context.Context, sessionID, tool, subject string) (bool, string, error) {
	db, err := p.db.Get()
	if err != nil {
		return false, "", fmt.Errorf("tools: match grant: %w", err)
	}
	rows, err := db.Query(ctx, `
		SELECT tool, pattern, 'project allowlist' FROM project_allowlist
		UNION ALL
		SELECT tool, pattern, 'session grant' FROM session_grants
		WHERE session_id = $1 AND expires > now()`,
		sessionID)
	if err != nil {
		return false, "", fmt.Errorf("tools: match grant: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rowTool, pattern, source string
		if err := rows.Scan(&rowTool, &pattern, &source); err != nil {
			return false, "", fmt.Errorf("tools: match grant: %w", err)
		}
		if !ToolMatches(tool, rowTool) {
			continue
		}
		if globMatch(pattern, subject) {
			return true, source + " " + pattern, nil
		}
	}
	return false, "", rows.Err()
}

// ToolMatches reports whether a grant/allowlist row named rowTool
// covers a call to tool: exact match, or tool ends with "_"+rowTool
// (connector namespacing — see matchGrant's D-036 note). rowTool ==
// SandboxGrantTool never suffix-matches; it is a stored sandbox root,
// not a grantable tool name. Exported so loop.filterDefs can apply the
// same suffix semantics to an agent's ToolAllow list — the two layers
// must never disagree about what a config-authored name refers to.
func ToolMatches(tool, rowTool string) bool {
	if tool == rowTool {
		return true
	}
	if rowTool == SandboxGrantTool {
		return false
	}
	return strings.HasSuffix(tool, "_"+rowTool)
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
	case "fetch_url":
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

// AllowedAbsPrefixes are absolute paths a shell command may name even
// though they sit outside the workspace: stream plumbing only.
var AllowedAbsPrefixes = []string{"/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr"}

// guardSubject applies the policy guard to shell commands. Other
// tools have no path-bearing arguments yet; fetch_url has its own
// network guard.
func guardSubject(root, tool, subject string) string {
	if tool != "shell" {
		return ""
	}
	// Blank the allowed stream-plumbing paths first so /dev/null
	// doesn't trip the system-dirs rule.
	for _, p := range AllowedAbsPrefixes {
		subject = strings.ReplaceAll(subject, p, " ")
	}
	for _, g := range guardPatterns {
		if g.pattern.MatchString(subject) {
			return "policy guard: " + g.name + " are off-limits"
		}
	}
	if root != "" {
		for _, qt := range commandTokensQuoted(subject) {
			tok := qt.text
			// A quoted token can't shell-expand (brace, glob-via-regex
			// chars), so a regex/pattern argument like '/^#{1,6}/' or
			// '/tmp/{a,b}' looks path-like but never resolves to a real
			// path — skip it. Unquoted, the same text is still live
			// shell syntax (e.g. /x/{a,b} brace-expands to real paths)
			// and must stay checked.
			if qt.quoted && strings.ContainsAny(tok, "^$[]{}\\+|") {
				continue
			}
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

// CommandTokens splits a command line the cheap way — whitespace,
// with quotes and redirect prefixes stripped — enough to spot
// absolute paths. It intentionally over-matches: a false hit returns
// corrective feedback, and the model retries with workspace paths.
func CommandTokens(command string) []string {
	qts := commandTokensQuoted(command)
	out := make([]string, 0, len(qts))
	for _, qt := range qts {
		out = append(out, qt.text)
	}
	return out
}

// quotedToken is a command token plus whether it was single- or
// double-quoted in the original command — quoting suppresses shell
// expansion, which guardSubject uses to tell a real path from a
// pattern argument (see commandTokensQuoted).
type quotedToken struct {
	text   string
	quoted bool
}

// commandTokensQuoted is CommandTokens plus per-token quoting info.
func commandTokensQuoted(command string) []quotedToken {
	// '=' splits too, so --output=/abs/path exposes its path part.
	fields := strings.Fields(strings.ReplaceAll(command, "=", " "))
	out := make([]quotedToken, 0, len(fields))
	for _, f := range fields {
		// Redirect/fd syntax (>, <, 2>) only ever prefixes a path, never
		// trails it — trimming from both ends would also strip a
		// literal "<tag>" down to "/tag", a false absolute-path hit.
		f = strings.TrimLeft(f, "<>0123456789")
		trimmed := strings.Trim(f, `'";|&()`)
		if trimmed == "" {
			continue
		}
		quoted := (strings.HasPrefix(f, "'") || strings.HasPrefix(f, `"`)) && trimmed != f
		out = append(out, quotedToken{text: trimmed, quoted: quoted})
	}
	return out
}
