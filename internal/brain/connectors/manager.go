package connectors

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
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

// Tools returns every built connector's tools, each renamed to
// "<connector>_<tool>" so names never collide across connectors or
// with the builtin set. The clones share the source's Execute.
func (m *Manager) Tools() []*tools.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*tools.Tool
	for name, src := range m.sources {
		for _, t := range src.Tools() {
			full := toolNameSanitizer.ReplaceAllString(name+"_"+t.Name, "_")
			if len(full) > 128 {
				full = full[:128]
			}
			clone := *t
			clone.Name = full
			out = append(out, &clone)
		}
	}
	return out
}

// ReadOnlyTools returns every built connector's ReadOnly-marked tools,
// namespaced exactly like Tools — for mission turns, which must see
// connector reads (gmail search, calendar list) despite BuiltinsOnly
// but never connector writes (see missions.nativeRunner's connector
// reads resolver). MCP sources are excluded unconditionally, marker or
// not: a remote MCP server's tool can change behavior between builds,
// and nothing here can verify a "read-only" claim actually holds —
// unlike google's tool constructors, which are Timothy's own code.
func (m *Manager) ReadOnlyTools() []*tools.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*tools.Tool
	for name, src := range m.sources {
		if _, isMCP := src.(*mcpSource); isMCP {
			continue
		}
		for _, t := range src.Tools() {
			if !t.ReadOnly {
				continue
			}
			full := toolNameSanitizer.ReplaceAllString(name+"_"+t.Name, "_")
			if len(full) > 128 {
				full = full[:128]
			}
			clone := *t
			clone.Name = full
			out = append(out, &clone)
		}
	}
	return out
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
// sensitive — the sole input to session.SensitiveTools.ConnectorNames
// (D-036): a connector named "gmail" being sensitive means "gmail"
// joins the set, and Matches' namespace-prefix check catches
// gmail_search/gmail_read/etc. at once. Reads rows fresh each call (no
// restart needed for a settings toggle to take effect), same reasoning
// as sensitiveRoute.
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
