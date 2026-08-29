package connectors

import (
	"context"
	"encoding/json"
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

// TestAggregateMCPJoinsUnifiedSurfaceAlongsideNonMCP pins that an MCP
// source with no colliding raw name merges into the unified surface
// right alongside non-MCP aggregates, un-namespaced: MCP tools are no
// longer treated differently from any other source once their raw name
// is free to merge (see groupByRawName).
func TestAggregateMCPJoinsUnifiedSurfaceAlongsideNonMCP(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"gmail": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("gmail", true)}}, kind: "google"},
		"slack": &mcpSource{name: "slack", toolList: []*tools.Tool{
			{Name: "read_channel", ReadOnly: true, InputSchema: json.RawMessage(`{"type":"object"}`)},
		}},
	}

	names := map[string]bool{}
	for _, tl := range m.Tools(nil) {
		names[tl.Name] = true
	}
	if !names["search_mail"] {
		t.Fatal("aggregated search_mail missing")
	}
	if !names["read_channel"] {
		t.Fatal("MCP tool with no name collision must merge un-namespaced")
	}
	if names["slack_read_channel"] {
		t.Fatal("MCP tool must not also appear namespaced once merged")
	}
}

// TestAggregateTwoMCPConnectorsSameToolIdenticalSchemaMerge pins the
// multi-MCP-connector case: two MCP connectors serving the same raw
// tool name with an identical schema merge into one tool, "account"
// required same as any other multi-account aggregate.
func TestAggregateTwoMCPConnectorsSameToolIdenticalSchemaMerge(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"linear": &mcpSource{name: "linear", toolList: []*tools.Tool{
			{Name: "search", InputSchema: schema, Execute: func(context.Context, json.RawMessage) (string, error) { return "ran on linear", nil }},
		}},
		"jira": &mcpSource{name: "jira", toolList: []*tools.Tool{
			{Name: "search", InputSchema: schema, Execute: func(context.Context, json.RawMessage) (string, error) { return "ran on jira", nil }},
		}},
	}

	tl := toolNamed(t, m.Tools(nil), "search")
	var s map[string]any
	if err := json.Unmarshal(tl.InputSchema, &s); err != nil {
		t.Fatal(err)
	}
	req, _ := s["required"].([]any)
	var hasAccount bool
	for _, r := range req {
		if r == "account" {
			hasAccount = true
		}
	}
	if !hasAccount {
		t.Fatal("account must be required once two MCP connectors share a raw name")
	}
	out, err := tl.Execute(t.Context(), json.RawMessage(`{"query":"x","account":"jira"}`))
	if err != nil || out != "ran on jira" {
		t.Fatalf("Execute(account=jira) = (%q, %v), want ran on jira", out, err)
	}

	names := map[string]bool{}
	for _, tl := range m.Tools(nil) {
		names[tl.Name] = true
	}
	if names["linear_search"] || names["jira_search"] {
		t.Fatalf("tools = %v, must not also appear namespaced once merged", names)
	}
}

// TestAggregateTwoMCPConnectorsSameToolSchemaMismatchStaysNamespaced
// pins Guard B: two MCP connectors serving the same raw tool name with
// DIFFERENT schemas must never merge — unlike non-MCP sources sharing a
// raw name (Timothy's own code, schema-identical by construction), an
// external MCP server's schema can't be assumed to match. Both stay
// namespaced instead.
func TestAggregateTwoMCPConnectorsSameToolSchemaMismatchStaysNamespaced(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"linear": &mcpSource{name: "linear", toolList: []*tools.Tool{
			{Name: "search", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)},
		}},
		"jira": &mcpSource{name: "jira", toolList: []*tools.Tool{
			{Name: "search", InputSchema: json.RawMessage(`{"type":"object","properties":{"jql":{"type":"string"}}}`)},
		}},
	}

	names := map[string]bool{}
	for _, tl := range m.Tools(nil) {
		names[tl.Name] = true
	}
	if names["search"] {
		t.Fatalf("tools = %v, schema-mismatched MCP tools must not merge", names)
	}
	if !names["linear_search"] || !names["jira_search"] {
		t.Fatalf("tools = %v, want both to stay namespaced", names)
	}
}

// TestAccountConnectorResolvesMergedMCPTool is the safety-invariant
// test for the raw-name refactor: a sensitive MCP connector's tool,
// once merged under its raw name, must still resolve back to that
// connector through AccountConnector — the path
// session.SensitiveTools.Matches falls back to once its own namespace-
// prefix check no longer fires (the tool has no prefix any more). This
// pins that the safety property survives the refactor at the layer
// that actually implements it.
func TestAccountConnectorResolvesMergedMCPTool(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"internal-crm": &mcpSource{name: "internal-crm", toolList: []*tools.Tool{
			{Name: "lookup_customer", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}},
	}

	// The tool merged un-namespaced (lone contributor, no collision).
	tl := toolNamed(t, m.Tools(nil), "lookup_customer")
	if tl.Name != "lookup_customer" {
		t.Fatalf("tool name = %q, want lookup_customer", tl.Name)
	}

	// AccountConnector must still resolve the raw-named call back to
	// the connector — the fact a name merged doesn't erase which
	// connector actually served it.
	if got := m.AccountConnector("lookup_customer", nil); got != "internal-crm" {
		t.Fatalf("AccountConnector(lookup_customer) = %q, want internal-crm", got)
	}
}

// TestAccountConnectorLeavesNamespacedMCPToolUnresolved pins the other
// half: a tool that stayed namespaced (Guard A/B) is never resolvable
// under its RAW name through AccountConnector, since it isn't exposed
// that way — session.SensitiveTools.Matches instead catches it via its
// own namespace-prefix check, which still fires for a namespaced tool.
func TestAccountConnectorLeavesNamespacedMCPToolUnresolved(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"internal-crm": &mcpSource{name: "internal-crm", toolList: []*tools.Tool{
			{Name: "lookup_customer", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}},
	}

	if got := m.AccountConnector("lookup_customer", nil); got == "" {
		t.Fatal("sanity check: lookup_customer should resolve before reserving it")
	}
	if got := m.AccountConnector("internal-crm_lookup_customer", nil); got != "" {
		t.Fatalf("AccountConnector(namespaced name) = %q, want empty (never exposed under that name here)", got)
	}
}
