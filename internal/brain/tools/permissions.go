package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
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
	// loadTools returns the connector deferred-tool index entry points
	// the live surface exposes right now (see SetLoadTools); nil until
	// wired, which exempts nothing.
	loadTools func() []string
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
			// memoryd defaults writes to pending unless the caller
			// explicitly marks them trusted. Keeping this exempt lets a
			// tainted unattended mission safely enqueue a review item.
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
			// writing_samples is a pure read of the operator's own
			// writing collection, whose name is bound in Go from
			// settings, never model input.
			"writing_samples": true,
			// search_memory is the same class of read over long-term
			// memory (issue #648): the query is the only argument and
			// nothing it returns reaches a side effect.
			"search_memory": true,
			// read_note reads the running automation's own notes, the
			// automation id bound in Go at construction, never model input.
			"read_note": true,
			// A connector's deferred-tool index entry point is exempt
			// too, but its name is only known once a connector is
			// built: see SetLoadTools.
		},
	}
}

// SetLoadTools wires the live set of connector deferred-tool index
// entry points (issue #643), exempt for the same reason load_skill is:
// their Execute resolves a name against an in-process list and returns
// that tool's description and schema as text, with no remote call and
// no side effect the operator could meaningfully approve. Prompting on
// the lookup would park a turn on the act of reading an index.
//
// D-108: the exemption is an EXACT match against the names the
// connector manager exposes for its own synthetic entry points
// (connectors.Manager.LoadToolNames), never a suffix rule. A suffix
// ("ends in _load_tool") let a remote MCP server name its way out of
// the whole chain (issue #757): server-chosen tool names pass through
// the manager's namespacing verbatim, so "exfiltrate_load_tool" on
// connector "evilmcp" surfaced as "evilmcp_exfiltrate_load_tool" and
// resolved to an exempt allow before any grant check. Connector names
// are operator config; remote tool names are not. The manager owns
// the namespacing, so it hands over finished names rather than this
// package re-deriving them (and importing connectors would be a
// cycle). Read per Resolve so a connector reload applies at once.
// Loading a tool still grants nothing: the loaded tool keeps its own
// namespaced name, is absent from the exempt map, and walks the whole
// chain when the model actually calls it.
func (p *Permissions) SetLoadTools(live func() []string) {
	p.loadTools = live
}

// isLoadTool reports whether tool is one of the live index entry
// points SetLoadTools wired.
func (p *Permissions) isLoadTool(tool string) bool {
	if p.loadTools == nil {
		return false
	}
	return slices.Contains(p.loadTools(), tool)
}

func (p *Permissions) isExempt(tool string) bool {
	return p.exempt[tool] || p.isLoadTool(tool)
}

// Resolve runs the chain for one call.
func (p *Permissions) Resolve(ctx context.Context, sessionID, tool string, args json.RawMessage) (Resolution, error) {
	subject := callSubject(tool, args)

	sandbox := ""
	if tool == "shell" {
		sandbox = p.sandboxFor(ctx, sessionID)
	}
	if reason := guardSubject(p.workspaceRoot, sandbox, tool, subject); reason != "" {
		return Resolution{
			Decision:  DecisionDeny,
			Subject:   subject,
			Rationale: reason,
		}, nil
	}

	if p.isExempt(tool) {
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
// is not a path inside the sandbox. lang-pkg-install (D-129) writes
// only to the container's user-writable paths.
var sandboxDowngradeable = map[string]bool{
	"lang-pkg-install":   true,
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
// inside that root or under the container's own /tmp. Relative
// paths resolve against the sandbox itself, since a sandboxed
// session's shell runs rooted there. The
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
		if strings.HasPrefix(tok, "/") && !pathWithin(root, tok) && !sandboxWritable(tok) {
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
// and names that no grant can unlock. Matched per token against the
// path-like parts of the call subject, case-insensitively.
//
// Matching is per token, not against the whole command line, because
// a guarded name can appear inside ordinary command text that touches
// no such path: `builder.aws` in a grep pattern is not ~/.aws, and
// "rotate the credentials" in a doc search is not a credential store.
// Each pattern below therefore anchors to a whole token or to a real
// path segment within one.
// filename alone identifies these, so they match any token: a key is
// key material wherever it sits, including a bare `id_rsa` in the
// working directory.
var guardPatterns = []struct {
	name    string
	pattern *regexp.Regexp
	// exempt names harmless matches of pattern (nil = none).
	exempt *regexp.Regexp
	// hint is appended to the denial so the model can retry usefully.
	hint string
	// sandbox is the D-129 relaxation in a mission sandbox session.
	sandbox sandboxRelax
}{
	{
		name:    "env files",
		pattern: regexp.MustCompile(`(?i)(^|/)\.env(\.[A-Za-z0-9._-]+)?$`),
		// Committed templates carry variable names, not secrets.
		exempt:  regexp.MustCompile(`(?i)(^|/)\.env\.(example|sample|dist|template)$`),
		hint:    " (templates such as .env.example are readable; export variables in the command instead of writing an env file)",
		sandbox: relaxInRoot,
	},
	{name: "ssh keys", pattern: regexp.MustCompile(`(?i)(^|/)id_(rsa|ed25519|ecdsa|dsa)$`)},
	{name: "key material", pattern: regexp.MustCompile(`(?i)\.(pem|key|p12|pfx|keystore)$`), sandbox: relaxInRootRead},
}

// sedRangeAddress matches a sed/awk range address such as
// /start/,/end/p. Quoted, it is program text, never a path.
var sedRangeAddress = regexp.MustCompile(`^/([^/\\]|\\.)*/,/([^/\\]|\\.)*/!?[A-Za-z]*$`)

// guardPathPatterns need directory context to be meaningful, so they
// match only tokens carrying path syntax (see looksLikePath). Without
// that gate a quoted phrase the tokenizer split on whitespace would
// put a bare "credentials" or "secrets" up for matching.
var guardPathPatterns = []struct {
	name    string
	pattern *regexp.Regexp
	sandbox sandboxRelax
}{
	{name: "ssh keys", pattern: regexp.MustCompile(`(?i)(^|/)\.ssh(/|$)`)},
	{name: "credential stores", pattern: regexp.MustCompile(`(?i)(^|/)(credentials?|secrets?)(/|\.[A-Za-z0-9]+)?$|(^|/)\.(aws|kube|gnupg)(/|$)|(^|/)\.(netrc|npmrc)$`), sandbox: relaxInRootRead},
	{name: "home dotfiles", pattern: regexp.MustCompile(`^~/\.[A-Za-z]`)},
	{name: "system dirs", pattern: regexp.MustCompile(`^/(etc|root|proc|sys|dev|boot|var/(run|lib))(/|$)`), sandbox: relaxSystemRead},
}

// AllowedAbsPrefixes are absolute paths a shell command may name even
// though they sit outside the workspace: stream plumbing only.
var AllowedAbsPrefixes = []string{"/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr"}

// sandboxRelax names how a guard rule relaxes in a session with a
// registered mission sandbox (D-129).
//
// D-129: a mission container runs as uid 65534 on a read-only rootfs
// with no host secrets, and delegated executors (claude dontAsk, codex
// bypass, opencode allow) already do all of the below in the same
// container. The native worker gets parity: env files inside the
// worktree, reads of committed credential-looking repo files, reads of
// the container's own toolchain paths, and writes to the container's
// /tmp, a per-container tmpfs rather than a host path. Host paths (~,
// ~/.ssh, ~/.aws), ssh keys, other writes outside the worktree and chat
// sessions keep the rule.
type sandboxRelax int

const (
	// The zero value relaxes nothing.
	_ sandboxRelax = iota
	// relaxInRoot: any command, token inside the sandbox root.
	relaxInRoot
	// relaxInRootRead: read-only command, token inside the sandbox root.
	relaxInRootRead
	// relaxSystemRead: read-only command, token is a sandbox read path.
	relaxSystemRead
)

// sandboxReadPrefixes are absolute paths inside the mission container
// a read-only command may name (D-129): toolchains, scratch, and the
// non-secret HOME subdirs. HOME itself is left out so a recursive read
// cannot walk into the executor auth state.
var sandboxReadPrefixes = []string{"/usr", "/opt", "/tmp", "/home/sandbox/.mise", "/home/sandbox/.cache", "/home/sandbox/.local"}

// sandboxWritable reports whether a token lexically cleans to the
// container's /tmp, a per-container tmpfs any command may name (D-129).
func sandboxWritable(tok string) bool {
	cleaned := path.Clean(tok)
	return cleaned == "/tmp" || strings.HasPrefix(cleaned, "/tmp/")
}

// sandboxReadFiles are single files under guarded dirs that are safe
// to read in the container.
var sandboxReadFiles = []string{"/etc/os-release"}

// sandboxReadDenied stays off-limits under sandboxReadPrefixes: the
// claude CLI's subscription auth state (sandboxd executorStateMountPath).
var sandboxReadDenied = []string{"/home/sandbox/.claude"}

// sandboxReadable reports whether an absolute token names a sandbox
// read path, lexically cleaned.
func sandboxReadable(tok string) bool {
	if !strings.HasPrefix(tok, "/") {
		return false
	}
	cleaned := path.Clean(tok)
	if slices.Contains(sandboxReadFiles, cleaned) {
		return true
	}
	for _, d := range sandboxReadDenied {
		if cleaned == d || strings.HasPrefix(cleaned, d+"/") {
			return false
		}
	}
	for _, p := range sandboxReadPrefixes {
		if cleaned == p || strings.HasPrefix(cleaned, p+"/") {
			return true
		}
	}
	return false
}

// readOnlyCommands never write a file named on their command line.
// Tools with an output-file flag (sort -o, tree -o, xxd -r), a
// command-running flag (rg --pre, find -exec) or a cwd effect (cd)
// are left out on purpose.
var readOnlyCommands = map[string]bool{
	"cat": true, "head": true, "tail": true, "ls": true, "stat": true,
	"file": true, "wc": true, "grep": true, "egrep": true, "fgrep": true,
	"du": true, "readlink": true, "realpath": true, "diff": true,
	"cmp": true, "md5sum": true, "sha1sum": true, "sha256sum": true,
	"sha512sum": true, "which": true, "test": true, "strings": true,
	"od": true, "hexdump": true,
}

// readOnlyCommand reports whether a command only reads: no danger or
// opaque rule matches (so no redirect, mv, rm, substitution) and every
// pipeline or list segment starts with a readOnlyCommands word.
func readOnlyCommand(command string) bool {
	if _, matched := ClassifyCommand(command); len(matched) > 0 {
		return false
	}
	segments := commandSegments(command)
	if len(segments) == 0 {
		return false
	}
	for _, seg := range segments {
		fields := strings.Fields(seg)
		if len(fields) == 0 || !readOnlyCommands[fields[0]] {
			return false
		}
	}
	return true
}

// commandSegments splits a command on ; | & and newline outside quotes.
func commandSegments(command string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case strings.ContainsRune(";|&\n", r):
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

// inSandboxRoot reports whether a guard fragment names a path inside
// the sandbox root. Relative fragments resolve against it (the mission
// shell's cwd); the result is lexically cleaned, so ../ climbing out
// does not count. A token the shell would expand ($HOME, ~, `...`)
// never counts: where it lands is unknown here.
func inSandboxRoot(sandbox, rawTok, frag string) bool {
	if sandbox == "" || strings.ContainsAny(rawTok, "$~`") {
		return false
	}
	p := frag
	if !strings.HasPrefix(p, "/") {
		p = path.Join(sandbox, p)
	}
	return pathWithin(path.Clean(sandbox), p)
}

// sandboxRelaxes reports whether rule relax lifts a guard match on frag.
func sandboxRelaxes(relax sandboxRelax, sandbox, rawTok, frag string, readOnly func() bool) bool {
	if sandbox == "" {
		return false
	}
	switch relax {
	case relaxInRoot:
		return inSandboxRoot(sandbox, rawTok, frag)
	case relaxInRootRead:
		return inSandboxRoot(sandbox, rawTok, frag) && readOnly()
	case relaxSystemRead:
		return slices.Contains(sandboxReadFiles, path.Clean(frag)) && readOnly()
	}
	return false
}

// guardSubject applies the policy guard to shell commands. Other
// tools have no path-bearing arguments yet; fetch_url has its own
// network guard. sandbox is the session's registered sandbox root, or
// "" (chat and host sessions), which disables every D-129 relaxation.
func guardSubject(root, sandbox, tool, subject string) string {
	if tool != "shell" {
		return ""
	}
	original := subject
	readOnly := sync.OnceValue(func() bool { return readOnlyCommand(original) })
	// Blank the allowed stream-plumbing paths first so /dev/null
	// doesn't trip the system-dirs rule.
	for _, p := range AllowedAbsPrefixes {
		subject = strings.ReplaceAll(subject, p, " ")
	}
	for _, qt := range commandTokensQuoted(subject) {
		for _, tok := range guardFragments(qt.text) {
			for _, g := range guardPatterns {
				if g.pattern.MatchString(tok) && (g.exempt == nil || !g.exempt.MatchString(tok)) &&
					!sandboxRelaxes(g.sandbox, sandbox, qt.text, tok, readOnly) {
					return "policy guard: " + g.name + " are off-limits" + g.hint
				}
			}
			if !looksLikePath(tok) {
				continue
			}
			for _, g := range guardPathPatterns {
				if g.pattern.MatchString(tok) && !sandboxRelaxes(g.sandbox, sandbox, qt.text, tok, readOnly) {
					return "policy guard: " + g.name + " are off-limits"
				}
			}
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
			if qt.quoted && (strings.ContainsAny(tok, "^$[]{}\\+|") || sedRangeAddress.MatchString(tok)) {
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
			if sandbox != "" && (sandboxWritable(tok) || sandboxReadable(tok) && readOnly()) {
				continue
			}
			return fmt.Sprintf("policy guard: %s is outside the workspace %s — use paths under the workspace", tok, root)
		}
	}
	return ""
}

// looksLikePath reports whether a token carries path syntax: a slash,
// or a leading dot or tilde. Only those tokens are matched against
// the path rules, so an ordinary word that happens to be a guarded
// name ("credentials" inside a quoted phrase the tokenizer split on
// whitespace) is not mistaken for a path. `secrets/` and `.env` both
// qualify; a bare `credentials` does not.
func looksLikePath(tok string) bool {
	return strings.Contains(tok, "/") || strings.HasPrefix(tok, ".") || strings.HasPrefix(tok, "~")
}

// guardFragmentSplit are the characters that can wrap or abut a path
// inside a single whitespace-delimited token: quotes and brackets in
// a language literal (python3 -c "open('/etc/passwd')"), and the
// shell's own separators. '.', '-', '_' and '~' are absent on
// purpose, since they occur inside real path names.
const guardFragmentSplit = "()[]{},;:&|<>*?!\"'`$= \t\n"

// guardFragments splits one token into the path-shaped pieces the
// guard matches against. A path can sit inside a token rather than be
// the token: `"open('/etc/passwd').read()"` is one token, and only
// after splitting on quotes and brackets does /etc/passwd appear.
// Splitting also strips wrapping punctuation, so `"~/.aws/creds";`
// still matches.
func guardFragments(tok string) []string {
	return strings.FieldsFunc(tok, func(r rune) bool {
		return strings.ContainsRune(guardFragmentSplit, r)
	})
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
//
// It scans char-by-char tracking quote nesting rather than splitting
// on whitespace first, so a space inside a quoted fragment (an awk or
// grep program like '/R1/{ok=1} END{exit !ok}') does not get cut into
// two tokens before the quoting is seen. A token is "quoted" if any
// part of it was produced while inside a quote — nested or not — so
// the regex/pattern skip in guardSubject still applies to something
// like "awk '/^###/ ...'" wrapped in an outer sh -c "...".
func commandTokensQuoted(command string) []quotedToken {
	// '=' splits too, so --output=/abs/path exposes its path part —
	// but not inside quotes, where '=' can be literal program text.
	var quote rune
	runes := []rune(command)
	for i, r := range runes {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '=':
			runes[i] = ' '
		}
	}
	command = string(runes)

	var out []quotedToken
	var cur strings.Builder
	curQuoted := false
	quote = 0
	flush := func() {
		trimmed := strings.Trim(cur.String(), `;|&()`)
		if !curQuoted {
			// Redirect/fd syntax (>, <, 2>) only ever prefixes a path,
			// never trails it, and is never quoted literal content.
			trimmed = strings.TrimLeft(trimmed, "<>0123456789")
		}
		cur.Reset()
		if trimmed == "" {
			curQuoted = false
			return
		}
		out = append(out, quotedToken{text: trimmed, quoted: curQuoted})
		curQuoted = false
	}
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			curQuoted = true
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}
