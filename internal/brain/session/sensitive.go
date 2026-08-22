package session

import (
	"context"
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
// the connectors settings UI): connectors are namespaced
// "<connector-name>_<tool-name>" (connectors.Manager.Tools), so a
// connector's own name is the PREFIX of every tool it serves. Both
// ConnectorNames and Route are funcs, not static values, so toggling a
// connector's sensitive flag (or the configured route) applies to the
// next side-call without a restart; an empty Route result means the
// feature is currently off (callers keep their own default route).
type SensitiveTools struct {
	ConnectorNames func(context.Context) []string
	Route          func(context.Context) string
}

// Matches reports whether toolName is covered: suffix+"_" prefix (a
// whole connector's namespace) against ConnectorNames.
func (s *SensitiveTools) Matches(ctx context.Context, toolName string) bool {
	if s == nil || s.ConnectorNames == nil {
		return false
	}
	for _, name := range s.ConnectorNames(ctx) {
		if strings.HasPrefix(toolName, name+"_") {
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
		if s.Matches(ctx, te.Name) {
			return true
		}
	}
	return false
}
