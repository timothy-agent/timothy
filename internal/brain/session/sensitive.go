package session

import (
	"context"
	"encoding/json"
	"strings"
)

// SensitiveTools names the connectors whose tools mark a turn/session
// sensitive, and the route side-calls must fall back to when they are.
// The loop pins the TURN's own route once a matching tool runs
// (Agent.SetForceRouteByConnector), but side-calls — memory extraction,
// session compaction — re-send turn/conversation content to their own
// routes LATER, on their own schedule; without this they'd bypass the
// same floor the in-turn steps already honor and ship raw sensitive
// content (e.g. email text) to a cloud model. ConnectorNames is the
// user-controlled input (a connector's own "sensitive" flag, set from
// the connectors settings UI). Two ways a tool call can be covered:
// suffix+"_" prefix against an MCP-style namespaced name
// ("<connector-name>_<tool-name>", connectors.Manager.Tools' MCP
// passthrough), or, for a unified aggregate tool (connectors.Manager's
// mail_search etc., which carries no connector name in its own name),
// AccountConnector resolving the call's actual account to a sensitive
// connector. AccountConnector is nil-safe (not every caller wires it,
// and non-connector tools never resolve). All three funcs re-resolve on
// every call, not cached, so toggling a connector's sensitive flag (or
// the configured route) applies to the next side-call without a
// restart; an empty Route result means the feature is currently off
// (callers keep their own default route).
type SensitiveTools struct {
	ConnectorNames   func(context.Context) []string
	AccountConnector func(ctx context.Context, toolName string, args json.RawMessage) string
	Route            func(context.Context) string
}

// Matches reports whether a call to toolName (with args, for unified
// aggregate tools' account resolution) is covered: suffix+"_" prefix
// against ConnectorNames, or AccountConnector resolving the call to a
// name in ConnectorNames.
func (s *SensitiveTools) Matches(ctx context.Context, toolName string, args json.RawMessage) bool {
	if s == nil || s.ConnectorNames == nil {
		return false
	}
	names := s.ConnectorNames(ctx)
	for _, name := range names {
		if strings.HasPrefix(toolName, name+"_") {
			return true
		}
	}
	if s.AccountConnector == nil {
		return false
	}
	connector := s.AccountConnector(ctx, toolName, args)
	if connector == "" {
		return false
	}
	for _, name := range names {
		if name == connector {
			return true
		}
	}
	return false
}

// SessionSensitive reports whether any tool_execution event anywhere in
// events matches s — scoped to the WHOLE session, not just a span about
// to be summarized or the current turn, since content downstream of a
// sensitive tool call (e.g. quoted email text) can carry forward into
// later turns and later compaction spans alike (D-007 residue). Shared
// by the compactor (which span to summarize/extract on) and chat
// (which route to serve the next turn on) so both apply the same
// verdict. nil sensitive (feature off) always reports false.
func (s *SensitiveTools) SessionSensitive(ctx context.Context, events []Event) bool {
	if s == nil {
		return false
	}
	for _, ev := range events {
		if ev.Kind != KindToolExecution {
			continue
		}
		var te ToolExecution
		if decode(ev, &te) != nil {
			continue
		}
		if s.Matches(ctx, te.Name, json.RawMessage(te.Args)) {
			return true
		}
	}
	return false
}
