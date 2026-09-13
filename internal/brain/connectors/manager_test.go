package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

type fakeRows struct {
	rows []Connector
	err  error
}

func (f fakeRows) List(context.Context) ([]Connector, error) { return f.rows, f.err }
func (f fakeRows) Get(_ context.Context, id string) (Connector, error) {
	for _, c := range f.rows {
		if c.ID == id {
			return c, nil
		}
	}
	return Connector{}, fmt.Errorf("connector %s: %w", id, ErrNotFound)
}

type fakeSource struct {
	tools   []*tools.Tool
	testErr error
	closed  bool
	tested  bool
}

func (f *fakeSource) Tools() []*tools.Tool       { return f.tools }
func (f *fakeSource) Test(context.Context) error { f.tested = true; return f.testErr }
func (f *fakeSource) Close() error               { f.closed = true; return nil }

// fakeAccountSource is a fakeSource that also implements accountInfo:
// google/microsoft's role in aggregation tests, without the real
// OAuth/API machinery.
type fakeAccountSource struct {
	fakeSource
	kind  string
	email string
}

func (f *fakeAccountSource) AccountInfo() (string, string) { return f.kind, f.email }

func testManager(rows rowSource) *Manager {
	return &Manager{
		rows:     rows,
		resolve:  func(context.Context, string) (string, error) { return "resolved", nil },
		builders: map[string]Builder{},
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		sources:  map[string]Source{},
		ready:    make(chan struct{}),
	}
}

func TestReloadBuildsEnabledOnly(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "github", Kind: "mcp", Enabled: true},
		{ID: "2", Name: "grafana", Kind: "mcp", Enabled: false},
		{ID: "3", Name: "gmail", Kind: "google", Enabled: true}, // no builder → skipped
	}})
	m.RegisterBuilder("mcp", func(context.Context, Connector, Resolve) (Source, error) {
		return &fakeSource{}, nil
	})

	if err := m.Reload(t.Context()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	got := m.Names()
	slices.Sort(got)
	if !slices.Equal(got, []string{"github"}) {
		t.Fatalf("Names = %v, want [github]", got)
	}
}

func TestReloadSkipsFailedBuildAndClosesOld(t *testing.T) {
	t.Parallel()
	old := &fakeSource{}
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "good", Kind: "mcp", Enabled: true},
		{ID: "2", Name: "bad", Kind: "mcp", Enabled: true},
	}})
	m.sources = map[string]Source{"stale": old}
	m.RegisterBuilder("mcp", func(_ context.Context, c Connector, _ Resolve) (Source, error) {
		if c.Name == "bad" {
			return nil, errors.New("boom")
		}
		return &fakeSource{}, nil
	})

	if err := m.Reload(t.Context()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := m.Names(); !slices.Equal(got, []string{"good"}) {
		t.Fatalf("Names = %v, want [good]", got)
	}
	if !old.closed {
		t.Fatal("previous source not closed after swap")
	}
}

// TestReadOnlyToolsExcludesWritesAndMCP pins ReadOnlyTools' two-part
// filter: only ReadOnly-marked tools, and never from an MCP source —
// even one whose tool happens to carry ReadOnly, since a remote MCP
// server's claim can't be verified (see ReadOnlyTools' doc comment).
// Non-MCP tools aggregate un-namespaced (see aggregateTools).
func TestReadOnlyToolsExcludesWritesAndMCP(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"gmail": &fakeSource{tools: []*tools.Tool{
			{Name: "search", ReadOnly: true},
			{Name: "send", ReadOnly: false},
		}},
		"remote": &mcpSource{name: "remote", toolList: []*tools.Tool{
			{Name: "read", ReadOnly: true},
		}},
	}

	got := map[string]bool{}
	for _, t := range m.ReadOnlyTools() {
		got[t.Name] = true
	}
	want := map[string]bool{"search": true}
	if len(got) != len(want) || !got["search"] {
		t.Fatalf("ReadOnlyTools = %v, want %v", got, want)
	}
}

func TestReloadKeepsSetOnListError(t *testing.T) {
	t.Parallel()
	keep := &fakeSource{}
	m := testManager(fakeRows{err: errors.New("db down")})
	m.sources = map[string]Source{"keep": keep}

	if err := m.Reload(t.Context()); err == nil {
		t.Fatal("Reload with failing list: want error")
	}
	if got := m.Names(); !slices.Equal(got, []string{"keep"}) {
		t.Fatalf("Names after failed reload = %v, want [keep]", got)
	}
	if keep.closed {
		t.Fatal("kept source must not be closed on failed reload")
	}
}

// TestSensitiveNames pins that it reads rows fresh (via rowSource.List,
// not the built sources map) and filters to enabled AND sensitive: a
// disabled-but-sensitive or enabled-but-not-sensitive connector must
// not appear in the suffix set session.SensitiveTools consumes.
func TestSensitiveNames(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "gmail", Kind: "google", Enabled: true, Sensitive: true},
		{ID: "2", Name: "calendar", Kind: "google", Enabled: true, Sensitive: false},
		{ID: "3", Name: "slack", Kind: "mcp", Enabled: false, Sensitive: true},
	}})

	got, err := m.SensitiveNames(t.Context())
	if err != nil {
		t.Fatalf("SensitiveNames: %v", err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"gmail"}) {
		t.Fatalf("SensitiveNames = %v, want [gmail]", got)
	}
}

func TestSensitiveNamesPropagatesListError(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{err: errors.New("db down")})
	if _, err := m.SensitiveNames(t.Context()); err == nil {
		t.Fatal("SensitiveNames with failing list: want error")
	}
}

func TestTestBuildsEphemeralSource(t *testing.T) {
	t.Parallel()
	src := &fakeSource{testErr: errors.New("401 unauthorized")}
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "github", Kind: "mcp", Enabled: false}, // disabled still testable
	}})
	m.RegisterBuilder("mcp", func(context.Context, Connector, Resolve) (Source, error) {
		return src, nil
	})

	err := m.Test(t.Context(), "1")
	if err == nil || err.Error() != "401 unauthorized" {
		t.Fatalf("Test = %v, want the probe error", err)
	}
	if !src.tested || !src.closed {
		t.Fatalf("ephemeral source tested=%v closed=%v, want both", src.tested, src.closed)
	}
}

func TestTestUnknownKindAndID(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "gmail", Kind: "google", Enabled: true},
	}})

	if err := m.Test(t.Context(), "1"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Test without builder = %v, want ErrUnsupported", err)
	}
	if err := m.Test(t.Context(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Test unknown id = %v, want ErrNotFound", err)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	t.Parallel()
	base := Connector{Name: "github", Kind: "mcp"}

	for _, tc := range []struct {
		name   string
		mutate func(*Connector)
	}{
		{"uppercase name", func(c *Connector) { c.Name = "GitHub" }},
		{"empty name", func(c *Connector) { c.Name = "" }},
		{"space in name", func(c *Connector) { c.Name = "git hub" }},
		{"unknown kind", func(c *Connector) { c.Kind = "smtp" }},
		{"secret-looking ref", func(c *Connector) { c.CredentialRef = "sk-abc def" }},
		{"invalid config", func(c *Connector) { c.Config = []byte("{not json") }},
	} {
		c := base
		tc.mutate(&c)
		if err := validate(c); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}

	ok := base
	ok.Config = []byte(`{"endpoint":"https://api.example/mcp"}`)
	ok.CredentialRef = "GITHUB_MCP_TOKEN"
	if err := validate(ok); err != nil {
		t.Fatalf("valid connector rejected: %v", err)
	}
}

func isReady(m *Manager) bool {
	select {
	case <-m.Ready():
		return true
	default:
		return false
	}
}

func TestReadyNotClosedBeforeFirstReload(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "github", Kind: "mcp", Enabled: true},
	}})
	if isReady(m) {
		t.Fatal("Ready closed before any Reload ran")
	}
}

func TestReadyClosesAfterSuccessfulReloadAndOnReloadHook(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "github", Kind: "mcp", Enabled: true},
	}})
	m.RegisterBuilder("mcp", func(context.Context, Connector, Resolve) (Source, error) {
		return &fakeSource{}, nil
	})
	var onReloadRanBeforeReady bool
	m.SetOnReload(func(context.Context) {
		onReloadRanBeforeReady = !isReady(m)
	})

	if err := m.Reload(t.Context()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !isReady(m) {
		t.Fatal("Ready not closed after successful Reload")
	}
	if !onReloadRanBeforeReady {
		t.Fatal("onReload hook must observe Ready still open — ready closes AFTER onReload")
	}
}

func TestReadyClosesWithZeroConnectorsConfigured(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{rows: nil})

	if err := m.Reload(t.Context()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !isReady(m) {
		t.Fatal("Ready must close even with zero connectors — ready means the load ran, not that connectors exist")
	}
}

func TestFailedReloadDoesNotMarkReady(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{err: errors.New("db down")})

	if err := m.Reload(t.Context()); err == nil {
		t.Fatal("Reload with failing list: want error")
	}
	if isReady(m) {
		t.Fatal("Ready closed after a failed Reload")
	}
}

func TestWaitReadyRespectsContextCancel(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "github", Kind: "mcp", Enabled: true},
	}})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.WaitReady(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitReady after cancel = %v, want context.Canceled", err)
	}
}

func TestWaitReadyReturnsAfterReload(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{rows: nil})
	if err := m.Reload(t.Context()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := m.WaitReady(t.Context()); err != nil {
		t.Fatalf("WaitReady after Reload = %v, want nil", err)
	}
}

// TestDeferredToolsFollowsTheMergedName pins issue #729's input to
// chat: indexed mcp sources contribute their hidden tools under the
// namespaced name the load path gives them, keyed by the load_tool
// name the surface actually exposes. Every indexed connector serves
// load_tool with the same schema, so that is the single raw
// "load_tool", shared across connectors; eager sources of any kind
// contribute nothing.
func TestDeferredToolsFollowsTheMergedName(t *testing.T) {
	t.Parallel()
	loadSchema := json.RawMessage(`{"type":"object"}`)
	indexed := func(name string, hidden ...string) *mcpSource {
		src := &mcpSource{name: name, indexed: true}
		for _, h := range hidden {
			src.toolList = append(src.toolList, &tools.Tool{Name: h, InputSchema: loadSchema})
		}
		return src
	}
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"gmail":  &fakeSource{tools: []*tools.Tool{{Name: "search"}}},
		"eager":  &mcpSource{name: "eager", toolList: []*tools.Tool{{Name: "ping"}}},
		"jira":   indexed("jira", "search.issues", "get_issue"),
		"wiki":   indexed("wiki", "get_page"),
		"hidden": &mcpSource{name: "hidden", indexed: true},
	}

	got := m.DeferredTools()
	want := map[string][]string{
		"load_tool": {"jira_get_issue", "jira_search_issues", "wiki_get_page"},
	}
	if len(got) != len(want) {
		t.Fatalf("DeferredTools = %v, want %v", got, want)
	}
	for k, names := range want {
		if !slices.Equal(got[k], names) {
			t.Fatalf("DeferredTools[%s] = %v, want %v", k, got[k], names)
		}
	}
	if got := m.liveLoadTool("jira"); got != "load_tool" {
		t.Fatalf("liveLoadTool(jira) = %q, want load_tool", got)
	}
	if got := m.liveLoadTool("not-built"); got != "load_tool" {
		t.Fatalf("liveLoadTool(unbuilt) = %q, want load_tool default", got)
	}
}

// A load_tool whose schema disagrees with another connector's (only
// possible if a remote server ever ships its own tool named load_tool)
// splits, and the map keys it by the namespaced name the surface uses.
func TestDeferredToolsSplitsOnSchemaMismatch(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"jira":  &mcpSource{name: "jira", indexed: true, toolList: []*tools.Tool{{Name: "get_issue"}}},
		"other": &mcpSource{name: "other", toolList: []*tools.Tool{{Name: "load_tool", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{}}}`)}}},
	}
	got := m.DeferredTools()
	if !slices.Equal(got["jira_load_tool"], []string{"jira_get_issue"}) || len(got) != 1 {
		t.Fatalf("DeferredTools = %v, want jira_load_tool only", got)
	}
}

// TestReport counts the tools an indexed mcp source hides so the
// connector page can name the load_tool entry point (issue #729); an
// eager source reports zero.
func TestTestReportCountsDeferredTools(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{rows: []Connector{{ID: "c1", Name: "jira", Kind: "mcp"}, {ID: "c2", Name: "small", Kind: "mcp"}}})
	m.builders["mcp"] = func(_ context.Context, c Connector, _ Resolve) (Source, error) {
		if c.Name == "jira" {
			return &fakeDeferredSource{fakeSource{tools: []*tools.Tool{{Name: "load_tool"}}}, []*tools.Tool{{Name: "a"}, {Name: "b"}, {Name: "c"}}}, nil
		}
		return &fakeSource{tools: []*tools.Tool{{Name: "ping"}}}, nil
	}

	got, err := m.TestReport(t.Context(), "c1")
	if err != nil {
		t.Fatalf("TestReport(jira): %v", err)
	}
	if got.DeferredTools != 3 || got.Identity != nil || got.LoadTool != "load_tool" {
		t.Fatalf("TestReport(jira) = %+v, want 3 deferred tools behind load_tool, no identity", got)
	}
	got, err = m.TestReport(t.Context(), "c2")
	if err != nil {
		t.Fatalf("TestReport(small): %v", err)
	}
	if got.DeferredTools != 0 || got.LoadTool != "" {
		t.Fatalf("TestReport(small) = %+v, want no deferral", got)
	}
}

type fakeDeferredSource struct {
	fakeSource
	hidden []*tools.Tool
}

func (f *fakeDeferredSource) DeferredTools() []*tools.Tool { return f.hidden }
