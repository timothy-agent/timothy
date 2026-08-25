package connectors

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// searchTool builds a minimal mail_search-shaped tool whose Execute
// reports which account (by connector name) actually ran it, so a test
// can assert routing without a real Google/Microsoft source.
func searchTool(connector string, readOnly bool) *tools.Tool {
	return &tools.Tool{
		Name:        "mail_search",
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

	tl := toolNamed(t, m.Tools(), "mail_search")
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

	tl := toolNamed(t, m.Tools(), "mail_search")
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

	tl := toolNamed(t, m.Tools(), "mail_search")
	_, err := tl.Execute(t.Context(), json.RawMessage(`{"query":"x","account":"nope"}`))
	if err == nil || !strings.Contains(err.Error(), "nope") ||
		!strings.Contains(err.Error(), "personal") || !strings.Contains(err.Error(), "work") {
		t.Fatalf("Execute(unknown account) = %v, want an error naming nope and listing personal/work", err)
	}
}

// TestAggregateDescriptionListsConnectedAccounts pins the description
// shape: base description plus one line per account naming the
// connector, its email when known, its kind, and mail_search's
// provider-specific syntax hint.
func TestAggregateDescriptionListsConnectedAccounts(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"personal": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("personal", true)}}, kind: "google", email: "me@personal.com"},
		"work":     &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("work", true)}}, kind: "microsoft"},
	}

	desc := toolNamed(t, m.Tools(), "mail_search").Description
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
// that a kind's mail_search guidance block only renders when that kind
// actually contributes an account: a google-only deployment must never
// see microsoft's Graph $search note, and vice versa.
func TestAggregateDescriptionKindGuidanceOnlyForContributingKinds(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"personal": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("personal", true)}}, kind: "google"},
	}

	desc := toolNamed(t, m.Tools(), "mail_search").Description
	if !strings.Contains(desc, "For google accounts") {
		t.Fatalf("description = %q, want the google guidance block", desc)
	}
	if strings.Contains(desc, "For microsoft accounts") {
		t.Fatalf("description = %q, must not contain microsoft guidance with no microsoft account connected", desc)
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

	tl := toolNamed(t, m.Tools(), "mail_search")
	if tl.ReadOnly {
		t.Fatal("aggregate ReadOnly=true, want false when any contributing account's tool is not ReadOnly")
	}

	// ReadOnlyTools must also drop the whole aggregate: only "a"'s copy
	// is ReadOnly, but the unified tool covers both accounts and can't
	// selectively deny "b" mid-call.
	for _, tl := range m.ReadOnlyTools() {
		if tl.Name == "mail_search" {
			t.Fatal("ReadOnlyTools must not include mail_search when one account's tool is not ReadOnly")
		}
	}
}

// TestAggregateMCPPassthroughUnaffected pins that MCP sources keep
// their existing "<connector>_<tool>" namespacing, appended alongside
// the aggregated non-MCP surface: MCP tool names are external and
// can't be unified.
func TestAggregateMCPPassthroughUnaffected(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"gmail": &fakeAccountSource{fakeSource: fakeSource{tools: []*tools.Tool{searchTool("gmail", true)}}, kind: "google"},
		"slack": &mcpSource{name: "slack", toolList: []*tools.Tool{{Name: "read_channel", ReadOnly: true}}},
	}

	names := map[string]bool{}
	for _, tl := range m.Tools() {
		names[tl.Name] = true
	}
	if !names["mail_search"] {
		t.Fatal("aggregated mail_search missing")
	}
	if !names["slack_read_channel"] {
		t.Fatal("MCP tool must keep its connector-prefixed name")
	}
	if names["read_channel"] {
		t.Fatal("MCP tool must not also appear un-namespaced")
	}
}
