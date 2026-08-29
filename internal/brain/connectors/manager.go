package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// Resolve turns a credential_ref into its secret value at build time.
// Wired to the secret store in production, a fake in tests.
type Resolve func(ctx context.Context, ref string) (string, error)

// Source is one built connector: a live handle on the third-party
// service.
type Source interface {
	// Tools returns the source's tools, un-namespaced; fetched at
	// build time and immutable for the source's lifetime (reloads
	// rebuild sources).
	Tools() []*tools.Tool
	// Test checks connectivity and auth without side effects.
	Test(ctx context.Context) error
	Close() error
}

// Builder constructs a Source for one connector kind ("mcp",
// "google"). Builders are registered at startup; a kind without a
// builder is configured but dormant.
type Builder func(ctx context.Context, c Connector, resolve Resolve) (Source, error)

const testTimeout = 20 * time.Second

// rowSource is the slice of Store the manager reads; a fake satisfies
// it in unit tests.
type rowSource interface {
	List(ctx context.Context) ([]Connector, error)
	Get(ctx context.Context, id string) (Connector, error)
}

// Manager owns the built sources: Reload loads enabled rows, builds
// each through its kind's builder, and swaps the set atomically —
// admin writes serve without restarts, mirroring the gateway's
// snapshot store.
type Manager struct {
	store    *Store
	rows     rowSource
	resolve  Resolve
	builders map[string]Builder
	log      *slog.Logger

	onReload func(context.Context)

	mu      sync.RWMutex
	sources map[string]Source // by connector name

	// ready closes after the first successful Reload — including its
	// onReload hook (D-043): a chat turn that waits on it is guaranteed
	// the agent's tool surface already reflects connector tools, not
	// just that Reload's DB read finished.
	ready     chan struct{}
	readyOnce sync.Once
}

func NewManager(store *Store, resolve Resolve, log *slog.Logger) *Manager {
	m := &Manager{
		store:    store,
		rows:     store,
		resolve:  resolve,
		builders: map[string]Builder{},
		log:      log,
		sources:  map[string]Source{},
		ready:    make(chan struct{}),
	}
	store.SetOnChange(func(ctx context.Context) {
		if err := m.Reload(ctx); err != nil {
			log.Warn("connector reload failed; keeping previous sources", "error", err)
		}
	})
	return m
}

// Store exposes the CRUD layer for the HTTP handlers.
func (m *Manager) Store() *Store { return m.store }

// RegisterBuilder wires one kind's builder. Startup-time only.
func (m *Manager) RegisterBuilder(kind string, b Builder) {
	m.builders[kind] = b
}

// Reload builds sources for every enabled connector and swaps the set.
// A connector whose build fails is skipped with a log — one bad row
// must not take down the others — and the previous set is closed only
// after the swap.
func (m *Manager) Reload(ctx context.Context) error {
	rows, err := m.rows.List(ctx)
	if err != nil {
		return err
	}
	fresh := map[string]Source{}
	for _, c := range rows {
		if !c.Enabled {
			continue
		}
		b, ok := m.builders[c.Kind]
		if !ok {
			m.log.Warn("connector kind has no builder; skipping", "connector", c.Name, "kind", c.Kind)
			continue
		}
		src, err := b(ctx, c, m.resolve)
		if err != nil {
			m.log.Warn("connector build failed; skipping", "connector", c.Name, "error", err)
			continue
		}
		fresh[c.Name] = src
	}

	m.mu.Lock()
	old := m.sources
	m.sources = fresh
	m.mu.Unlock()

	for name, src := range old {
		if err := src.Close(); err != nil {
			m.log.Warn("connector close failed", "connector", name, "error", err)
		}
	}
	if m.onReload != nil {
		m.onReload(ctx)
	}
	// Closed AFTER onReload, not before: ready must mean "the agent's
	// tool surface already includes this load's connector tools", not
	// merely "sources swapped" — a waiter that saw ready but raced
	// onReload would still get a stale (builtins-only) tool snapshot.
	m.readyOnce.Do(func() { close(m.ready) })
	return nil
}

// toolNameSanitizer strips characters providers reject in tool names.
var toolNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// Tools returns the agent's whole tool surface: every source (MCP
// included) joins the raw-name aggregation aggregateTools performs,
// with an "account" argument selecting the connector once more than
// one contributes the same raw name — same as the unified mail/
// calendar surface. reserved names (the agent's non-connector tools,
// e.g. builtins) never aggregate under their raw form: a connector
// tool sharing one falls back to "<connector>_<tool>" instead, so a
// remote MCP server can never shadow a tool the operator didn't wire
// through a connector. Two MCP connectors serving the same raw name
// with non-identical schemas also fall back to namespaced form for
// that name (see aggregateTools): unlike Timothy's own connector
// kinds, an external MCP server's schema can't be assumed to match.
func (m *Manager) Tools(reserved map[string]bool) []*tools.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return aggregateTools(m.sources, false, reserved)
}

// ReadOnlyTools returns the aggregated ReadOnly-marked non-MCP tool
// surface, for mission turns, which must see connector reads (mail
// search, calendar list) despite BuiltinsOnly but never connector
// writes (see missions.nativeRunner's connector reads resolver). MCP
// sources are excluded unconditionally, marker or not: a remote MCP
// server's tool can change behavior between builds, and nothing here
// can verify a "read-only" claim actually holds, unlike google/
// microsoft's tool constructors, which are Timothy's own code.
func (m *Manager) ReadOnlyTools() []*tools.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return aggregateTools(m.sources, true, nil)
}

// AccountConnector resolves which connector name a unified tool call
// (toolName plus its args) would actually route to: the same account
// resolution aggregateTool's Execute applies, exposed standalone so
// callers that only need to know WHICH connector a call touches (e.g.
// connector-level sensitivity, session.SensitiveTools) don't have to
// re-run the call to find out. Every source, MCP included, contributes
// here through groupByRawName's SAME merge decision Tools uses: an MCP
// tool that actually merged into the raw-name surface routes through
// account resolution, so a sensitive MCP connector's raw-named tool
// call still resolves back to it; an MCP tool that stayed namespaced
// (Guard A/B) never reaches here under its raw name, since it isn't
// exposed that way. reserved is nil: sensitivity resolution runs after
// the agent's tool surface is already fixed, so it only needs to know
// what's ACTUALLY unified, which groupByRawName reports regardless of
// reserved (a nil reserved set only affects the split, never whether a
// name that did merge is resolvable). Returns "" when toolName isn't a
// currently aggregated tool, or when args' account doesn't resolve
// (missing-but-required, or unknown); callers fall back to their own
// default in that case, same as any non-connector tool call.
func (m *Manager) AccountConnector(toolName string, args json.RawMessage) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	merged, _ := groupByRawName(m.sources, nil)
	accounts, ok := merged[toolName]
	if !ok {
		return ""
	}
	acc, _, err := resolveAccount(accounts, args)
	if err != nil {
		return ""
	}
	return acc.connector
}

// namespacedTools clones ts renamed "<name>_<tool>", sanitized and
// length-capped: MCP's tool-naming scheme, kept for a name that stays
// split (see groupByRawName).
func namespacedTools(name string, ts []*tools.Tool) []*tools.Tool {
	out := make([]*tools.Tool, 0, len(ts))
	for _, t := range ts {
		full := toolNameSanitizer.ReplaceAllString(name+"_"+t.Name, "_")
		if len(full) > 128 {
			full = full[:128]
		}
		clone := *t
		clone.Name = full
		out = append(out, &clone)
	}
	return out
}

// accountInfo is the optional Source capability that reports the
// source's kind and connected account email (google-, microsoft-kind
// today): the input aggregateTools groups tools around. A source that
// doesn't implement it (mcp included) reports empty kind/email but
// still joins the raw-name grouping.
type accountInfo interface {
	AccountInfo() (kind, email string)
}

// toolAccount is one source's contribution to an aggregated tool: its
// connector name/kind/email (for account matching and the description's
// connected-accounts list) plus its own (un-namespaced) tool.
type toolAccount struct {
	connector string
	kind      string
	email     string
	tool      *tools.Tool
}

// groupByRawName is the merge decision Tools, ReadOnlyTools, and
// AccountConnector all share: every source's tools grouped by their
// RAW (un-namespaced) name, split into what actually aggregates under
// that name and what must stay namespaced. Two guards pull a name (or
// one connector's copy of it) out of the merged set and into split:
//
//   - reserved: a name the agent could already have from somewhere
//     else (builtins, chat-only mission tools) never aggregates,
//     regardless of source — a connector must never shadow a tool the
//     operator didn't wire through a connector. nil means nothing is
//     reserved (safe default for callers, like AccountConnector, that
//     run after the actual surface is already fixed).
//   - schema mismatch among MCP contributors: non-MCP sources sharing a
//     raw name are Timothy's own code and assumed schema-identical by
//     construction (TestGoogleMicrosoftSharedToolSchemasMatch); an
//     external MCP server can't be trusted the same way. If any two
//     MCP connectors serving the same raw name (or an MCP connector and
//     a non-MCP one already occupying that name) disagree on
//     InputSchema, every MCP contributor at that name splits instead of
//     merging — the non-MCP contributors, if any, still merge among
//     themselves.
//
// A name with zero merged contributors after splitting simply doesn't
// appear in merged; its split contributors still get their namespaced
// form from the caller.
func groupByRawName(sources map[string]Source, reserved map[string]bool) (merged map[string][]toolAccount, split map[string][]toolAccount) {
	byName := map[string][]toolAccount{}
	for name, src := range sources {
		var kind, email string
		if info, ok := src.(accountInfo); ok {
			kind, email = info.AccountInfo()
		}
		for _, t := range src.Tools() {
			byName[t.Name] = append(byName[t.Name], toolAccount{connector: name, kind: kind, email: email, tool: t})
		}
	}

	merged = map[string][]toolAccount{}
	split = map[string][]toolAccount{}
	for name, accounts := range byName {
		if reserved[name] {
			split[name] = accounts
			continue
		}
		mcpSchemasMatch := true
		var mcpSchema json.RawMessage
		for _, a := range accounts {
			if _, isMCP := sources[a.connector].(*mcpSource); !isMCP {
				continue
			}
			if mcpSchema == nil {
				mcpSchema = a.tool.InputSchema
				continue
			}
			if !schemasEqual(mcpSchema, a.tool.InputSchema) {
				mcpSchemasMatch = false
				break
			}
		}
		// An MCP contributor also has to match a non-MCP occupant of the
		// same name, not just other MCP contributors, or joining would
		// silently pick whichever schema aggregateTool happens to render.
		if mcpSchemasMatch && mcpSchema != nil {
			for _, a := range accounts {
				if _, isMCP := sources[a.connector].(*mcpSource); isMCP {
					continue
				}
				if !schemasEqual(mcpSchema, a.tool.InputSchema) {
					mcpSchemasMatch = false
					break
				}
			}
		}
		if mcpSchemasMatch {
			merged[name] = accounts
			continue
		}
		// Mismatch: only the MCP contributors split off; any non-MCP
		// tool at this name is Timothy's own code and keeps merging.
		for _, a := range accounts {
			if _, isMCP := sources[a.connector].(*mcpSource); isMCP {
				split[name] = append(split[name], a)
			} else {
				merged[name] = append(merged[name], a)
			}
		}
	}
	return merged, split
}

// schemasEqual reports whether two tool input schemas are the same
// JSON value, ignoring key order — an MCP server's exact byte
// formatting is never guaranteed to match another's or to be stable
// build to build.
func schemasEqual(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// aggregateTools groups every source's tools by their RAW (un-
// namespaced) name and returns one tool per name for whatever merges
// (see groupByRawName), plus "<connector>_<tool>" for whatever splits.
// A future connector kind (or a raw-named MCP tool) joins the unified
// surface automatically just by giving its tools the same raw name;
// accountInfo is read opportunistically for kind/email metadata (empty
// when a source doesn't implement it, mcp included) and never gates
// whether a source's tools can merge. readOnlyOnly mirrors the old
// ReadOnlyTools' filter: a WHOLE merged aggregate is dropped unless
// every contributing account's tool is ReadOnly, and every split (MCP)
// tool is dropped outright — a remote MCP server's ReadOnly claim can't
// be verified, unlike google/microsoft's tool constructors, which are
// Timothy's own code.
func aggregateTools(sources map[string]Source, readOnlyOnly bool, reserved map[string]bool) []*tools.Tool {
	merged, split := groupByRawName(sources, reserved)

	if readOnlyOnly {
		// MCP never contributes to ReadOnlyTools, merged or not: a
		// remote server's ReadOnly claim can't be verified, unlike
		// google/microsoft's tool constructors (Timothy's own code).
		// Strip MCP accounts out of each merged group rather than
		// trusting groupByRawName's ordinary merge/split split, which
		// answers a different question (does this name unify safely),
		// not this one (is this account's claim trustworthy).
		filtered := map[string][]toolAccount{}
		for name, accounts := range merged {
			var kept []toolAccount
			for _, a := range accounts {
				if _, isMCP := sources[a.connector].(*mcpSource); !isMCP {
					kept = append(kept, a)
				}
			}
			if len(kept) > 0 {
				filtered[name] = kept
			}
		}
		merged = filtered
	}

	order := make([]string, 0, len(merged))
	for name := range merged {
		order = append(order, name)
	}
	sort.Strings(order)
	out := make([]*tools.Tool, 0, len(order))
	for _, name := range order {
		accounts := merged[name]
		sort.Slice(accounts, func(i, j int) bool { return accounts[i].connector < accounts[j].connector })
		agg := aggregateTool(name, accounts)
		if readOnlyOnly && !agg.ReadOnly {
			continue
		}
		out = append(out, agg)
	}
	if readOnlyOnly {
		return out
	}

	splitNames := make([]string, 0, len(split))
	for name := range split {
		splitNames = append(splitNames, name)
	}
	sort.Strings(splitNames)
	for _, name := range splitNames {
		accounts := split[name]
		sort.Slice(accounts, func(i, j int) bool { return accounts[i].connector < accounts[j].connector })
		byConnector := map[string][]*tools.Tool{}
		var connectorOrder []string
		for _, a := range accounts {
			if _, seen := byConnector[a.connector]; !seen {
				connectorOrder = append(connectorOrder, a.connector)
			}
			byConnector[a.connector] = append(byConnector[a.connector], a.tool)
		}
		for _, connector := range connectorOrder {
			out = append(out, namespacedTools(connector, byConnector[connector])...)
		}
	}
	return out
}

// aggregateTool builds one aggregated tool from every account serving
// raw name: the model sees exactly this shape regardless of which or
// how many connectors/kinds contribute.
func aggregateTool(name string, accounts []toolAccount) *tools.Tool {
	readOnly := true
	for _, a := range accounts {
		if !a.tool.ReadOnly {
			readOnly = false
			break
		}
	}
	return &tools.Tool{
		Name:        name,
		ReadOnly:    readOnly,
		Description: aggregateDescription(accounts),
		InputSchema: injectAccountProperty(accounts[0].tool.InputSchema, len(accounts) > 1),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			acc, stripped, err := resolveAccount(accounts, args)
			if err != nil {
				return "", err
			}
			return acc.tool.Execute(ctx, stripped)
		},
	}
}

// aggregateDescription is the shared tool's own description (every
// account's copy is schema-identical by construction, see
// TestGoogleMicrosoftSharedToolSchemasMatch) plus a connected-accounts
// list naming which account argument reaches which connector, its
// kind, its email when known, and, for search_mail specifically, the
// provider's query syntax, since that's where google and microsoft
// diverge and the shared schema has nowhere else to say so.
func aggregateDescription(accounts []toolAccount) string {
	var b strings.Builder
	b.WriteString(accounts[0].tool.Description)
	b.WriteString("\n\nConnected accounts:\n")
	for _, a := range accounts {
		fmt.Fprintf(&b, "- %s", a.connector)
		if a.email != "" {
			fmt.Fprintf(&b, " (%s)", a.email)
		}
		fmt.Fprintf(&b, ": %s\n", a.kind)
	}
	if len(accounts) == 1 {
		fmt.Fprintf(&b, "\naccount is optional here, omit it to use %s.", accounts[0].connector)
	} else {
		b.WriteString("\naccount is required: pass one of the connector names or emails above.")
	}
	if accounts[0].tool.Name == "search_mail" {
		b.WriteString(mailSearchKindGuidance(accounts))
	}
	return strings.TrimRight(b.String(), "\n")
}

// mailSearchGuidanceByKind holds search_mail's per-provider query
// syntax guidance, rendered by mailSearchKindGuidance only for kinds
// actually contributing an account. Keyed by accountInfo's kind
// string. The google entry keeps the zero-result broaden-retry
// heuristics: they are valuable operational guidance, scoped here to
// the accounts they actually apply to instead of living in the base,
// provider-neutral tool description.
var mailSearchGuidanceByKind = map[string]string{
	"google": `
For google accounts: query uses Gmail search syntax (from:, subject:,
is:unread, newer_than:7d, after:YYYY/MM/DD, ...).

A zero-result search does NOT mean the email doesn't exist: Gmail's
from: matching is stricter than it looks, and a query combining from:
with keyword/subject terms narrows twice, compounding a near-miss into
zero. If a targeted search returns nothing, retry BROADER before
concluding the email isn't there:
1. Drop keyword/subject filters, keep only from: and a date range.
2. If from: with a bare domain (e.g. from:example.com) misses, try
   the FULL sender address you're looking for, or a shorter substring
   of the domain, or just the company name as a plain keyword with no
   from: operator at all.
3. Widen the date range (after:/before:/newer_than:), a mistaken
   assumption about when an email arrived is a common miss.
4. Try in:anywhere if you suspect it's archived or in another label.`,
	"microsoft": `
For microsoft accounts: query matches subject, body, and sender across
messages (Graph's $search); plain keywords, no operator syntax.`,
	"imap": `
For imap accounts: query is plain keywords only, ANDed server-side
against message text, no Gmail/Graph operator syntax (from:, subject:,
$search, etc. are not understood). Keep queries to 1-3 significant
words; if a search returns nothing, broaden by dropping a word rather
than adding more.`,
}

// mailSearchKindGuidance renders one guidance block per KIND actually
// contributing an account to accounts, in a fixed order (google,
// microsoft, imap) so the description is stable regardless of map
// iteration or account sort order.
func mailSearchKindGuidance(accounts []toolAccount) string {
	seen := map[string]bool{}
	for _, a := range accounts {
		seen[a.kind] = true
	}
	var b strings.Builder
	for _, kind := range []string{"google", "microsoft", "imap"} {
		if !seen[kind] {
			continue
		}
		b.WriteString("\n")
		b.WriteString(mailSearchGuidanceByKind[kind])
	}
	return b.String()
}

// injectAccountProperty adds an "account" string property to base (a
// tool's InputSchema, always {"type":"object","properties":{...},
// "required":[...],"additionalProperties":false} by construction, see
// tools.Tool's builtin/connector constructors); required only when
// several accounts serve the capability, since a lone account has an
// unambiguous default.
func injectAccountProperty(base json.RawMessage, accountRequired bool) json.RawMessage {
	var schema map[string]any
	// The base schema is always valid JSON built by our own tool
	// constructors; a decode failure here would be a programming error,
	// not a runtime condition to recover from gracefully. Fall back to
	// returning base unmodified rather than panicking.
	if err := json.Unmarshal(base, &schema); err != nil {
		return base
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	props["account"] = map[string]any{
		"type":        "string",
		"description": "which connected account to use: a connector name or its email (see the tool description's Connected accounts list)",
	}
	schema["properties"] = props
	if accountRequired {
		req, _ := schema["required"].([]any)
		schema["required"] = append(req, "account")
	}
	out, err := json.Marshal(schema)
	if err != nil {
		return base
	}
	return out
}

// resolveAccount picks which account's tool serves one call: args'
// "account" (connector name or email, case-insensitive) when several
// accounts are available, or the sole account by default when there's
// only one. Returns args with "account" stripped, since the underlying
// tool's own schema never declared that property. An unknown or
// missing (when required) account errors listing the valid names.
func resolveAccount(accounts []toolAccount, args json.RawMessage) (toolAccount, json.RawMessage, error) {
	var in map[string]json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return toolAccount{}, nil, err
		}
	}
	var account string
	if raw, ok := in["account"]; ok {
		if err := json.Unmarshal(raw, &account); err != nil {
			return toolAccount{}, nil, fmt.Errorf("account must be a string")
		}
		delete(in, "account")
	}
	stripped, err := json.Marshal(in)
	if err != nil {
		return toolAccount{}, nil, err
	}
	if account == "" {
		if len(accounts) == 1 {
			return accounts[0], stripped, nil
		}
		return toolAccount{}, nil, fmt.Errorf("account is required (%d connected accounts): %s", len(accounts), accountNames(accounts))
	}
	for _, a := range accounts {
		if strings.EqualFold(a.connector, account) || (a.email != "" && strings.EqualFold(a.email, account)) {
			return a, stripped, nil
		}
	}
	return toolAccount{}, nil, fmt.Errorf("unknown account %q; valid accounts: %s", account, accountNames(accounts))
}

// accountNames renders every account's connector name (plus email when
// known) for an error message's option list.
func accountNames(accounts []toolAccount) string {
	names := make([]string, 0, len(accounts))
	for _, a := range accounts {
		if a.email != "" {
			names = append(names, fmt.Sprintf("%s (%s)", a.connector, a.email))
		} else {
			names = append(names, a.connector)
		}
	}
	return strings.Join(names, ", ")
}

// SetOnReload registers a hook that fires after every successful
// Reload — the agent's tool set rebuilds from it. Startup-time only.
func (m *Manager) SetOnReload(fn func(context.Context)) {
	m.onReload = fn
}

// Ready returns a channel that closes once the first successful Reload
// (and its onReload hook) has completed. A manager with zero
// configured connectors still becomes ready — ready means "initial
// load ran", not "connectors exist".
func (m *Manager) Ready() <-chan struct{} {
	return m.ready
}

// WaitReady blocks until Ready() closes or ctx ends, whichever comes
// first.
func (m *Manager) WaitReady(ctx context.Context) error {
	select {
	case <-m.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Names returns the currently-built connector names (unordered).
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.sources))
	for n := range m.sources {
		names = append(names, n)
	}
	return names
}

// SensitiveNames returns the names of every enabled connector marked
// sensitive: the input to session.SensitiveTools.ConnectorNames
// (D-036): a connector named "personal-gmail" being sensitive means
// "personal-gmail" joins the set, caught either via Matches' namespace-
// prefix check (an MCP tool actually namespaced that way) or via
// AccountConnector resolving a unified aggregate call's account (e.g.
// search_mail) to that same connector name. Reads rows fresh each call
// (no restart needed for a settings toggle to take effect), same
// reasoning as sensitiveRoute.
func (m *Manager) SensitiveNames(ctx context.Context) ([]string, error) {
	rows, err := m.rows.List(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, c := range rows {
		if c.Enabled && c.Sensitive {
			names = append(names, c.Name)
		}
	}
	return names, nil
}

// Test builds the connector fresh — enabled or not — and runs its
// connectivity check, so a connector can be verified before it is
// switched on. The ephemeral source is closed either way.
func (m *Manager) Test(ctx context.Context, id string) error {
	_, err := m.TestIdentity(ctx, id)
	return err
}

// identifier is the optional Source capability that reports who a
// credential authenticates as (github-, microsoft-, and google-kind
// today).
// Type-asserted so kinds without an identity concept (mcp) are
// untouched. microsoftSource reuses GitHubIdentity's shape rather than
// a parallel type (see its Identity method).
type identifier interface {
	Identity(ctx context.Context) (GitHubIdentity, error)
}

// TestIdentity is Test plus, for a kind that can report one (github,
// microsoft, google), the resolved identity — the evidence a working
// credential was configured, since a github connector serves no tools
// to prove itself with otherwise (microsoft's tools require a scope
// the operator may not have granted, so the identity check is useful
// there too). identity is nil for kinds with no identity concept.
func (m *Manager) TestIdentity(ctx context.Context, id string) (*GitHubIdentity, error) {
	c, err := m.rows.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	b, ok := m.builders[c.Kind]
	if !ok {
		return nil, fmt.Errorf("connector kind %s has no builder yet: %w", c.Kind, ErrUnsupported)
	}
	tctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	src, err := b(tctx, c, m.resolve)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := src.Close(); err != nil {
			m.log.Warn("connector close failed", "connector", c.Name, "error", err)
		}
	}()

	idr, ok := src.(identifier)
	if !ok {
		return nil, src.Test(tctx)
	}
	identity, err := idr.Identity(tctx)
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

// repoSource is the optional Source capability that lists/creates
// GitHub repos and opens pull requests (github-kind today).
// Type-asserted so kinds without a repo concept (mcp, google) are
// untouched, mirroring identifier.
type repoSource interface {
	ListRepos(ctx context.Context) ([]GitHubRepo, error)
	CreateRepo(ctx context.Context, name string, private bool) (GitHubRepo, error)
	GetRepo(ctx context.Context, owner, repo string) (GitHubRepo, error)
	CreatePR(ctx context.Context, owner, repo, title, head, base, body string) (GitHubPR, error)
	PRMerged(ctx context.Context, owner, repo string, number int) (bool, error)
}

// buildRepoSource resolves connector id, builds it fresh (same shape as
// TestIdentity), and returns it asserted to repoSource — ErrUnsupported
// for an unknown kind or a kind with no repo concept.
func (m *Manager) buildRepoSource(ctx context.Context, id string) (repoSource, func(), error) {
	c, err := m.rows.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	b, ok := m.builders[c.Kind]
	if !ok {
		return nil, nil, fmt.Errorf("connector kind %s has no builder yet: %w", c.Kind, ErrUnsupported)
	}
	tctx, cancel := context.WithTimeout(ctx, testTimeout)
	src, err := b(tctx, c, m.resolve)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	closeFn := func() {
		cancel()
		if err := src.Close(); err != nil {
			m.log.Warn("connector close failed", "connector", c.Name, "error", err)
		}
	}
	rs, ok := src.(repoSource)
	if !ok {
		closeFn()
		return nil, nil, fmt.Errorf("connector kind %s has no repos to list: %w", c.Kind, ErrUnsupported)
	}
	return rs, closeFn, nil
}

// ListRepos lists every repo the connector's credential can see.
func (m *Manager) ListRepos(ctx context.Context, id string) ([]GitHubRepo, error) {
	rs, closeFn, err := m.buildRepoSource(ctx, id)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	return rs.ListRepos(ctx)
}

// CreateRepo creates a new repo through the connector's credential.
func (m *Manager) CreateRepo(ctx context.Context, id, name string, private bool) (GitHubRepo, error) {
	rs, closeFn, err := m.buildRepoSource(ctx, id)
	if err != nil {
		return GitHubRepo{}, err
	}
	defer closeFn()
	return rs.CreateRepo(ctx, name, private)
}

// GetRepo resolves owner/repo's metadata through the connector's
// credential — the PR flow's default_branch lookup.
func (m *Manager) GetRepo(ctx context.Context, id, owner, repo string) (GitHubRepo, error) {
	rs, closeFn, err := m.buildRepoSource(ctx, id)
	if err != nil {
		return GitHubRepo{}, err
	}
	defer closeFn()
	return rs.GetRepo(ctx, owner, repo)
}

// CreatePR opens a pull request through the connector's credential.
func (m *Manager) CreatePR(ctx context.Context, id, owner, repo, title, head, base, body string) (GitHubPR, error) {
	rs, closeFn, err := m.buildRepoSource(ctx, id)
	if err != nil {
		return GitHubPR{}, err
	}
	defer closeFn()
	return rs.CreatePR(ctx, owner, repo, title, head, base, body)
}

// PRMerged reports whether owner/repo pull request number has been
// merged, through the connector's credential.
func (m *Manager) PRMerged(ctx context.Context, id, owner, repo string, number int) (bool, error) {
	rs, closeFn, err := m.buildRepoSource(ctx, id)
	if err != nil {
		return false, err
	}
	defer closeFn()
	return rs.PRMerged(ctx, owner, repo, number)
}
