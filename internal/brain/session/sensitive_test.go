package session

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSensitiveToolsMatchesNilReceiver(t *testing.T) {
	t.Parallel()
	var s *SensitiveTools
	if s.Matches(context.Background(), "gmail_read", nil) {
		t.Fatal("nil SensitiveTools matched a tool, want false (feature off)")
	}
}

// TestSensitiveToolsMatchesConnectorPrefix pins the connector-level
// match rule: a connector's own name is the NAMESPACE PREFIX of every
// tool it serves ("<connector-name>_<tool-name>",
// connectors.Manager.Tools) — marking a whole connector sensitive adds
// its bare name to ConnectorNames, matched via a prefix check.
func TestSensitiveToolsMatchesConnectorPrefix(t *testing.T) {
	t.Parallel()
	s := &SensitiveTools{
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
		{name: "unrelated connector", tool: "list_calendar_events", want: false},
		{name: "connector name embedded but not as a namespace boundary", tool: "backslack_read", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := s.Matches(context.Background(), tc.tool, nil); got != tc.want {
				t.Fatalf("Matches(%q) = %v, want %v", tc.tool, got, tc.want)
			}
		})
	}
}

// TestSensitiveToolsMatchesConnectorNamesNil pins that a nil
// ConnectorNames func (not every caller wires it) never panics.
func TestSensitiveToolsMatchesConnectorNamesNil(t *testing.T) {
	t.Parallel()
	s := &SensitiveTools{
		Route: func(context.Context) string { return "local" },
	}
	if s.Matches(context.Background(), "slack_read_channel", nil) {
		t.Fatal("Matches true with nil ConnectorNames, want false")
	}
}

// TestSensitiveToolsMatchesAccountConnector pins the unified-tool path:
// a call to a name carrying no connector prefix (e.g. "search_mail")
// still matches once AccountConnector resolves it to a name in
// ConnectorNames: the account the call actually routed to, not the
// tool's own name, decides sensitivity here.
func TestSensitiveToolsMatchesAccountConnector(t *testing.T) {
	t.Parallel()
	s := &SensitiveTools{
		ConnectorNames: func(context.Context) []string { return []string{"work-outlook"} },
		AccountConnector: func(_ context.Context, toolName string, args json.RawMessage) string {
			if toolName == "search_mail" && string(args) == `{"account":"work-outlook"}` {
				return "work-outlook"
			}
			return "personal-gmail"
		},
		Route: func(context.Context) string { return "local" },
	}
	if !s.Matches(context.Background(), "search_mail", json.RawMessage(`{"account":"work-outlook"}`)) {
		t.Fatal("Matches false for a call AccountConnector resolves to a sensitive connector, want true")
	}
	if s.Matches(context.Background(), "search_mail", json.RawMessage(`{"account":"other"}`)) {
		t.Fatal("Matches true for a call resolving to a non-sensitive connector, want false")
	}
}

// TestSensitiveToolsMatchesAccountConnectorNil pins that a nil
// AccountConnector (not every caller wires it) never panics and simply
// falls back to the prefix rule alone.
func TestSensitiveToolsMatchesAccountConnectorNil(t *testing.T) {
	t.Parallel()
	s := &SensitiveTools{
		ConnectorNames: func(context.Context) []string { return []string{"work-outlook"} },
		Route:          func(context.Context) string { return "local" },
	}
	if s.Matches(context.Background(), "search_mail", json.RawMessage(`{"account":"work-outlook"}`)) {
		t.Fatal("Matches true with nil AccountConnector, want false (no prefix match either)")
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
		ConnectorNames: func(context.Context) []string {
			if sensitiveConnector {
				return []string{"slack"}
			}
			return nil
		},
		Route: func(context.Context) string { return "local" },
	}

	if s.Matches(context.Background(), "slack_read_channel", nil) {
		t.Fatal("Matches true before connector marked sensitive, want false")
	}

	sensitiveConnector = true
	if !s.Matches(context.Background(), "slack_read_channel", nil) {
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
