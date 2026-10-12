package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// searchTool builds a minimal search_mail-shaped tool whose Execute
// reports which account (by connector name) actually ran it, so a test
// can assert routing without a real Google/Microsoft source.
func searchTool(connector string, readOnly bool) *tools.Tool {
	return &tools.Tool{
		Name:        "search_mail",
		ReadOnly:    readOnly,
		Description: "Search mail.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "ran on " + connector, nil
		},
	}
}

func toolNamed(t *testing.T, ts []*tools.Tool, name string) *tools.Tool {
	t.Helper()
	for _, tl := range ts {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("no aggregated tool named %q in %v", name, ts)
	return nil
}

// TestAggregateSingleAccountDefaultsWithoutAccountArg pins the single-
// account case: account is optional in the schema, and omitting it
// routes to the lone account.
func TestAggregateSingleAccountDefaultsWithoutAccountArg(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"gmail": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("gmail", true)}}, kind: "google"},
	}

	tl := toolNamed(t, m.Tools(nil), "search_mail")
	var schema map[string]any
	if err := json.Unmarshal(tl.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	req, _ := schema["required"].([]any)
	for _, r := range req {
		if r == "account" {
			t.Fatal("account must not be required with a single account")
		}
	}

	out, err := tl.Execute(t.Context(), json.RawMessage(`{"query":"x"}`))
	if err != nil || out != "ran on gmail" {
		t.Fatalf("Execute = (%q, %v), want ran on gmail", out, err)
	}
}

// TestAggregateMultiAccountRequiresAccount pins the multi-account case:
// account becomes required in the schema, and a call with no account
// errors listing the valid options.
func TestAggregateMultiAccountRequiresAccount(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"personal": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("personal", true)}}, kind: "google", email: "me@personal.com"},
		"work":     &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("work", true)}}, kind: "microsoft", email: "me@work.com"},
	}

	tl := toolNamed(t, m.Tools(nil), "search_mail")
	var schema map[string]any
	if err := json.Unmarshal(tl.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	req, _ := schema["required"].([]any)
	var hasAccount bool
	for _, r := range req {
		if r == "account" {
			hasAccount = true
		}
	}
	if !hasAccount {
		t.Fatal("account must be required with several accounts")
	}

	if _, err := tl.Execute(t.Context(), json.RawMessage(`{"query":"x"}`)); err == nil {
		t.Fatal("missing account with several candidates: want an error")
	} else if !strings.Contains(err.Error(), "personal") || !strings.Contains(err.Error(), "work") {
		t.Fatalf("error = %v, want it to list both accounts", err)
	}

	out, err := tl.Execute(t.Context(), json.RawMessage(`{"query":"x","account":"work"}`))
	if err != nil || out != "ran on work" {
		t.Fatalf("Execute(account=work) = (%q, %v), want ran on work", out, err)
	}

	// Matching by email, case-insensitively.
	out, err = tl.Execute(t.Context(), json.RawMessage(`{"query":"x","account":"ME@PERSONAL.COM"}`))
	if err != nil || out != "ran on personal" {
		t.Fatalf("Execute(account=email) = (%q, %v), want ran on personal", out, err)
	}
}

// TestAggregateUnknownAccountErrors pins that an account naming
// neither a connector nor a known email errors, listing the valid
// options.
func TestAggregateUnknownAccountErrors(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"personal": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("personal", true)}}, kind: "google"},
		"work":     &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("work", true)}}, kind: "microsoft"},
	}

	tl := toolNamed(t, m.Tools(nil), "search_mail")
	_, err := tl.Execute(t.Context(), json.RawMessage(`{"query":"x","account":"nope"}`))
	if err == nil || !strings.Contains(err.Error(), "nope") ||
		!strings.Contains(err.Error(), "personal") || !strings.Contains(err.Error(), "work") {
		t.Fatalf("Execute(unknown account) = %v, want an error naming nope and listing personal/work", err)
	}
}

// TestAggregateDescriptionListsConnectedAccounts pins the description
// shape: base description plus one line per account naming the
// connector, its email when known, its kind, and search_mail's
// provider-specific syntax hint.
func TestAggregateDescriptionListsConnectedAccounts(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"personal": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("personal", true)}}, kind: "google", email: "me@personal.com"},
		"work":     &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("work", true)}}, kind: "microsoft"},
	}

	desc := toolNamed(t, m.Tools(nil), "search_mail").Description
	for _, want := range []string{
		"Search mail.", "personal", "me@personal.com", "google",
		"work", "microsoft", "For google accounts", "Gmail search syntax",
		"For microsoft accounts", "Graph's $search",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description = %q, want it to contain %q", desc, want)
		}
	}
}

// TestAggregateDescriptionKindGuidanceOnlyForContributingKinds pins
// that a kind's search_mail guidance block only renders when that kind
// actually contributes an account: a google-only deployment must never
// see microsoft's Graph $search note, and vice versa.
func TestAggregateDescriptionKindGuidanceOnlyForContributingKinds(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"personal": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("personal", true)}}, kind: "google"},
	}

	desc := toolNamed(t, m.Tools(nil), "search_mail").Description
	if !strings.Contains(desc, "For google accounts") {
		t.Fatalf("description = %q, want the google guidance block", desc)
	}
	if strings.Contains(desc, "For microsoft accounts") {
		t.Fatalf("description = %q, must not contain microsoft guidance with no microsoft account connected", desc)
	}
}

// TestAggregateIMAPContributesToMailSearch pins that an imap-kind
// fakeAccountSource joins the unified search_mail surface exactly like
// google/microsoft, and that its search_mail guidance block only
// renders when an imap account actually contributes — mirroring
// TestAggregateDescriptionKindGuidanceOnlyForContributingKinds.
func TestAggregateIMAPContributesToMailSearch(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"mailbox": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("mailbox", true)}}, kind: "imap"},
	}

	desc := toolNamed(t, m.Tools(nil), "search_mail").Description
	if !strings.Contains(desc, "For imap accounts") {
		t.Fatalf("description = %q, want the imap guidance block", desc)
	}
	if strings.Contains(desc, "For google accounts") || strings.Contains(desc, "For microsoft accounts") {
		t.Fatalf("description = %q, must not contain google/microsoft guidance with no such account connected", desc)
	}

	out, err := toolNamed(t, m.Tools(nil), "search_mail").Execute(t.Context(), json.RawMessage(`{"query":"x"}`))
	if err != nil || out != "ran on mailbox" {
		t.Fatalf("Execute = (%q, %v), want ran on mailbox", out, err)
	}
}

// TestAggregateReadOnlyRequiresAllAccountsReadOnly pins the ReadOnly
// AND-across-accounts rule: one account's write-marked tool makes the
// aggregate not ReadOnly, even though the other account's tool is.
func TestAggregateReadOnlyRequiresAllAccountsReadOnly(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"a": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("a", true)}}, kind: "google"},
		"b": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("b", false)}}, kind: "microsoft"},
	}

	tl := toolNamed(t, m.Tools(nil), "search_mail")
	if tl.ReadOnly {
		t.Fatal("aggregate ReadOnly=true, want false when any contributing account's tool is not ReadOnly")
	}

	// ReadOnlyTools must also drop the whole aggregate: only "a"'s copy
	// is ReadOnly, but the unified tool covers both accounts and can't
	// selectively deny "b" mid-call.
	for _, tl := range m.ReadOnlyTools() {
		if tl.Name == "search_mail" {
			t.Fatal("ReadOnlyTools must not include search_mail when one account's tool is not ReadOnly")
		}
	}
}

// TestNativeToolsCarriesWritesExcludesMCP pins issue #1229's manager
// half: NativeTools is the mission worker surface — native reads AND
// writes, MCP dropped entirely (D-157's marked reads still arrive via
// ReadOnlyTools, never here).
func TestNativeToolsCarriesWritesExcludesMCP(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"gmail": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{
			searchTool("gmail", true),
			{Name: "gmail_send", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}}, kind: "google"},
		"fastmail": &mcpSource{name: "fastmail", toolList: []*tools.Tool{
			{Name: "gmail_send", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}},
	}

	names := map[string]bool{}
	for _, tl := range m.NativeTools() {
		names[tl.Name] = true
	}
	if !names["search_mail"] {
		t.Fatalf("NativeTools = %v, want native read search_mail", names)
	}
	if !names["gmail_send"] {
		t.Fatalf("NativeTools = %v, want native write gmail_send", names)
	}
	if names["fastmail_gmail_send"] {
		t.Fatalf("NativeTools = %v, MCP write leaked in", names)
	}
}

// TestAggregateMCPToolsStayOutOfNativeAggregates pins D-160: native
// kinds share one verb (search_mail), and an MCP tool never joins it,
// even under the same raw name and schema: it keeps its provider
// prefix.
func TestAggregateMCPToolsStayOutOfNativeAggregates(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	mcpSearch := *searchTool("fastmail", true)
	m.sources = map[string]Source{
		"gmail":    &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("gmail", true)}}, kind: "google"},
		"fastmail": &mcpSource{name: "fastmail", toolList: []*tools.Tool{&mcpSearch}},
		"slack": &mcpSource{name: "slack", toolList: []*tools.Tool{
			{Name: "read_channel", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}},
	}
	names := map[string]bool{}
	for _, tl := range m.Tools(nil) {
		names[tl.Name] = true
	}
	if !names["search_mail"] || !names["fastmail_search_mail"] || !names["slack_read_channel"] || len(names) != 3 {
		t.Fatalf("tools = %v", names)
	}
	out, err := toolNamed(t, m.Tools(nil), "search_mail").Execute(t.Context(), json.RawMessage(`{"query":"x"}`))
	if err != nil || out != "ran on gmail" {
		t.Fatalf("native search_mail = (%q, %v), want gmail only", out, err)
	}
}

// TestAggregateSameProviderAccountsMerge pins D-160's merge: two
// connectors of one provider serving a tool with an identical schema
// share "<provider>_<tool>" with a required account argument, while a
// different provider with the same raw name stays apart.
func TestAggregateSameProviderAccountsMerge(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)
	ran := func(s string) func(context.Context, json.RawMessage) (string, error) {
		return func(context.Context, json.RawMessage) (string, error) { return "ran on " + s, nil }
	}
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"notion-work":     &mcpSource{name: "notion-work", provider: "notion", toolList: []*tools.Tool{{Name: "search", InputSchema: schema, Execute: ran("work")}}},
		"notion-personal": &mcpSource{name: "notion-personal", provider: "notion", toolList: []*tools.Tool{{Name: "search", InputSchema: schema, Execute: ran("personal")}}},
		"linear":          &mcpSource{name: "linear", toolList: []*tools.Tool{{Name: "search", InputSchema: schema, Execute: ran("linear")}}},
	}

	tl := toolNamed(t, m.Tools(nil), "notion_search")
	var s map[string]any
	if err := json.Unmarshal(tl.InputSchema, &s); err != nil {
		t.Fatal(err)
	}
	req, _ := s["required"].([]any)
	if !slices.Contains(req, any("account")) {
		t.Fatal("account must be required once two accounts share a provider tool")
	}
	out, err := tl.Execute(t.Context(), json.RawMessage(`{"query":"x","account":"notion-personal"}`))
	if err != nil || out != "ran on personal" {
		t.Fatalf("Execute(account=notion-personal) = (%q, %v)", out, err)
	}

	names := map[string]bool{}
	for _, tl := range m.Tools(nil) {
		names[tl.Name] = true
	}
	if len(names) != 2 || !names["linear_search"] {
		t.Fatalf("tools = %v, want notion_search and linear_search only", names)
	}
}

// TestAggregateSameProviderSchemaDriftSplits pins Guard B under
// D-160: two accounts of one provider whose schemas for a tool differ
// never merge; each falls back to "<connector>_<provider>_<tool>".
func TestAggregateSameProviderSchemaDriftSplits(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"work": &mcpSource{name: "work", provider: "notion", toolList: []*tools.Tool{
			{Name: "search", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)},
		}},
		"personal": &mcpSource{name: "personal", provider: "notion", toolList: []*tools.Tool{
			{Name: "search", InputSchema: json.RawMessage(`{"type":"object","properties":{"jql":{"type":"string"}}}`)},
		}},
	}
	names := map[string]bool{}
	for _, tl := range m.Tools(nil) {
		names[tl.Name] = true
	}
	if names["notion_search"] || !names["work_notion_search"] || !names["personal_notion_search"] {
		t.Fatalf("tools = %v, want both accounts split", names)
	}
}

// TestAccountConnectorResolvesMergedMCPTool is the safety invariant
// behind session.SensitiveTools.Matches: a sensitive connector's tool
// must resolve back to that connector whatever its surface name, here
// a provider prefix that no longer starts with the connector name.
func TestAccountConnectorResolvesMergedMCPTool(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"internal-crm": &mcpSource{name: "internal-crm", provider: "crm", toolList: []*tools.Tool{
			{Name: "lookup_customer", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}},
	}
	toolNamed(t, m.Tools(nil), "crm_lookup_customer")
	if got := m.AccountConnector("crm_lookup_customer", nil); got != "internal-crm" {
		t.Fatalf("AccountConnector(crm_lookup_customer) = %q, want internal-crm", got)
	}
}

// TestAccountConnectorResolvesSplitAndLoadedNames covers the names that
// are not merged aggregates: a split tool and a tool loaded through
// load_tool each resolve to their one connector, and a raw name the
// surface never exposes resolves to nothing.
func TestAccountConnectorResolvesSplitAndLoadedNames(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"work": &mcpSource{name: "work", provider: "notion", toolList: []*tools.Tool{
			{Name: "search", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)},
		}},
		"personal": &mcpSource{name: "personal", provider: "notion", toolList: []*tools.Tool{
			{Name: "search", InputSchema: json.RawMessage(`{"type":"object","properties":{"jql":{"type":"string"}}}`)},
		}},
		"jira": &mcpSource{name: "jira", indexed: true, toolList: []*tools.Tool{{Name: "get_issue"}}},
	}
	for name, want := range map[string]string{
		"personal_notion_search": "personal",
		"work_notion_search":     "work",
		"jira_get_issue":         "jira",
		"search":                 "",
		"get_issue":              "",
	} {
		if got := m.AccountConnector(name, nil); got != want {
			t.Fatalf("AccountConnector(%s) = %q, want %q", name, got, want)
		}
	}
}

// mcpReadSource is an mcp source as the mission read gate sees it: a
// tool list with its fingerprint plus the operator's mission_reads.
func mcpReadSource(name, fingerprint string, reads *MCPMissionReads, ts ...*tools.Tool) *mcpSource {
	return &mcpSource{name: name, cfg: mcpConfig{MissionReads: reads}, fingerprint: fingerprint, toolList: ts}
}

func mcpTool(name string, schema string) *tools.Tool {
	return &tools.Tool{Name: name, Description: "remote " + name, InputSchema: json.RawMessage(schema), Execute: func(context.Context, json.RawMessage) (string, error) { return "ran " + name, nil }}
}

func readOnlyNames(m *Manager) []string {
	var names []string
	for _, t := range m.ReadOnlyTools() {
		names = append(names, t.Name)
	}
	return names
}

// TestReadOnlyToolsMCPMissionReads pins D-157: an MCP tool reaches
// missions only through the operator's mark, only while the server's
// list matches the reviewed fingerprint, and never through the
// server's own read-only claim.
func TestReadOnlyToolsMCPMissionReads(t *testing.T) {
	t.Parallel()
	const schema = `{"type":"object","properties":{"q":{"type":"string"}}}`
	cases := []struct {
		name  string
		reads *MCPMissionReads
		fp    string
		want  []string
	}{
		{name: "no review keeps the connector chat-only", reads: nil, fp: "h1"},
		{name: "marked tools pass while the fingerprint matches", reads: &MCPMissionReads{Hash: "h1", Tools: []string{"search"}}, fp: "h1", want: []string{"notion_search"}},
		{name: "a changed server drops every mark", reads: &MCPMissionReads{Hash: "h1", Tools: []string{"search", "write"}}, fp: "h2"},
		{name: "an empty hash never matches", reads: &MCPMissionReads{Hash: "", Tools: []string{"search"}}, fp: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := testManager(fakeRows{})
			search := mcpTool("search", schema)
			search.ReadOnly = true // the server's claim must not count
			m.sources = map[string]Source{"notion": mcpReadSource("notion", tc.fp, tc.reads, search, mcpTool("write", schema))}
			if got := readOnlyNames(m); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("ReadOnlyTools = %v, want %v", got, tc.want)
			}
			for _, tl := range m.ReadOnlyTools() {
				if !tl.ReadOnly {
					t.Fatalf("%s offered to missions without ReadOnly", tl.Name)
				}
			}
			// Chat still sees everything either way.
			if chat := m.Tools(nil); len(chat) != 2 {
				t.Fatalf("chat surface = %d tools, want 2", len(chat))
			}
		})
	}
}

// TestReadOnlyToolsMCPMissionReadsNamespaced covers the split path: a
// marked tool that stays split (schema drift between two accounts of
// one provider) is offered under its split name, the unmarked account
// is not, and native aggregates are untouched.
func TestReadOnlyToolsMCPMissionReadsNamespaced(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	work := mcpReadSource("work", "h1", &MCPMissionReads{Hash: "h1", Tools: []string{"search"}}, mcpTool("search", `{"type":"object","properties":{"text":{"type":"string"}}}`))
	work.provider = "notion"
	personal := mcpReadSource("personal", "h1", nil, mcpTool("search", `{"type":"object","properties":{"jql":{"type":"string"}}}`))
	personal.provider = "notion"
	m.sources = map[string]Source{
		"gmail":    &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("gmail", true)}}, kind: "google"},
		"work":     work,
		"personal": personal,
	}
	got := readOnlyNames(m)
	if fmt.Sprint(got) != fmt.Sprint([]string{"search_mail", "work_notion_search"}) {
		t.Fatalf("ReadOnlyTools = %v", got)
	}
	out, err := toolNamed(t, m.ReadOnlyTools(), "search_mail").Execute(t.Context(), json.RawMessage(`{"query":"x"}`))
	if err != nil || out != "ran on gmail" {
		t.Fatalf("native aggregate = (%q, %v)", out, err)
	}
}
