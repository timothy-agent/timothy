package session

import (
	"context"
	"testing"
)

// TestSensitiveToolsMatches pins the suffix semantics: exact name, or
// name ending "_"+suffix (connector namespacing), same rule as
// loop.Agent.SetForceRoute/matchGrant (D-036).
func TestSensitiveToolsMatches(t *testing.T) {
	t.Parallel()
	s := &SensitiveTools{
		Suffixes: func(context.Context) []string { return []string{"gmail_read", "gmail_read_attachment"} },
		Route:    func(context.Context) string { return "local" },
	}
	tests := []struct {
		name string
		tool string
		want bool
	}{
		{name: "exact match", tool: "gmail_read", want: true},
		{name: "connector-namespaced match", tool: "personal_gmail_read", want: true},
		{name: "other suffix connector-namespaced", tool: "work_gmail_read_attachment", want: true},
		{name: "unrelated tool", tool: "shell", want: false},
		{name: "prefix only, not suffix", tool: "gmail_read_something_else", want: false},
		{name: "partial suffix without underscore boundary", tool: "xgmail_read", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := s.Matches(context.Background(), tc.tool); got != tc.want {
				t.Fatalf("Matches(%q) = %v, want %v", tc.tool, got, tc.want)
			}
		})
	}
}

func TestSensitiveToolsMatchesNilReceiver(t *testing.T) {
	t.Parallel()
	var s *SensitiveTools
	if s.Matches(context.Background(), "gmail_read") {
		t.Fatal("nil SensitiveTools matched a tool, want false (feature off)")
	}
}

// TestSensitiveToolsMatchesConnectorPrefix pins the connector-level
// case: a connector's own name is the NAMESPACE PREFIX of every tool it
// serves ("<connector-name>_<tool-name>", connectors.Manager.Tools),
// never a suffix — so marking a whole connector sensitive adds its bare
// name to ConnectorNames, matched via a prefix check that is separate
// from Suffixes' tool-name suffix rule (mixing the two in one list
// would make a floor entry like "gmail_read" falsely prefix-match an
// unrelated "gmail_read_all_labels" tool).
func TestSensitiveToolsMatchesConnectorPrefix(t *testing.T) {
	t.Parallel()
	s := &SensitiveTools{
		Suffixes:       func(context.Context) []string { return []string{"gmail_read"} },
		ConnectorNames: func(context.Context) []string { return []string{"slack"} },
		Route:          func(context.Context) string { return "local" },
	}
	tests := []struct {
		name string
		tool string
		want bool
	}{
		{name: "connector-prefixed tool", tool: "slack_read_channel", want: true},
		{name: "another tool under the same sensitive connector", tool: "slack_send_message", want: true},
		{name: "unrelated connector", tool: "calendar_list_events", want: false},
		{name: "connector name embedded but not as a namespace boundary", tool: "backslack_read", want: false},
		{name: "floor suffix does not falsely prefix-match", tool: "gmail_read_all_labels", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := s.Matches(context.Background(), tc.tool); got != tc.want {
				t.Fatalf("Matches(%q) = %v, want %v", tc.tool, got, tc.want)
			}
		})
	}
}

// TestSensitiveToolsMatchesConnectorNamesNil pins that a nil
// ConnectorNames func (not every caller wires it) never panics —
// Matches falls back to Suffixes alone.
func TestSensitiveToolsMatchesConnectorNamesNil(t *testing.T) {
	t.Parallel()
	s := &SensitiveTools{
		Suffixes: func(context.Context) []string { return []string{"gmail_read"} },
		Route:    func(context.Context) string { return "local" },
	}
	if s.Matches(context.Background(), "slack_read_channel") {
		t.Fatal("Matches true with nil ConnectorNames, want false")
	}
}

// TestSensitiveToolsMatchesDynamicConnectorNames pins that
// ConnectorNames is read fresh on every call, not cached at
// construction: a connector marked sensitive after SensitiveTools is
// built (e.g. a settings toggle) must still be caught on the next
// Matches/SessionSensitive call without a restart, same reasoning as
// Route.
func TestSensitiveToolsMatchesDynamicConnectorNames(t *testing.T) {
	t.Parallel()
	sensitiveConnector := false
	s := &SensitiveTools{
		Suffixes: func(context.Context) []string { return []string{"gmail_read"} },
		ConnectorNames: func(context.Context) []string {
			if sensitiveConnector {
				return []string{"slack"}
			}
			return nil
		},
		Route: func(context.Context) string { return "local" },
	}

	if s.Matches(context.Background(), "slack_read_channel") {
		t.Fatal("Matches true before connector marked sensitive, want false")
	}

	sensitiveConnector = true
	if !s.Matches(context.Background(), "slack_read_channel") {
		t.Fatal("Matches false after connector marked sensitive, want true")
	}

	log := newMemLog()
	if _, err := log.Append(context.Background(), "s1", KindToolExecution, ToolExecution{
		CallID: "c1", Name: "slack_read_channel", Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	events, err := log.Events(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !s.SessionSensitive(context.Background(), events) {
		t.Fatal("SessionSensitive false for a connector-namespaced sensitive tool, want true")
	}
}
