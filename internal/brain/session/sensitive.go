package session

import (
	"context"
	"strings"
)

// SensitiveTools names the tools whose execution marks a turn/session
// sensitive, and the route side-calls must fall back to when they are.
// The loop pins the TURN's own route once a matching tool runs
// (Agent.SetForceRoute), but side-calls — memory extraction, session
// compaction — re-send turn/conversation content to their own routes
// LATER, on their own schedule; without this they'd bypass the same
// floor the in-turn steps already honor and ship raw sensitive content
// (e.g. email text) to a cloud model. Suffix, not exact name: connector
// tools are namespaced "<connector-name>_<tool-name>" with a user-chosen
// connector name (same rule as Agent.SetForceRoute/matchGrant, D-036).
// Route is a func, not a static string, so a settings change applies to
// the next side-call without a restart; an empty result means the
// feature is currently off (callers keep their own default route).
// Suffixes is likewise a func, not a static slice: a specific tool's
// own name (e.g. "gmail_read") that must stay pinned regardless of
// which connector namespace wraps it — matched as a suffix, same rule
// as Agent.SetForceRoute/matchGrant (D-036). ConnectorNames is the
// separate, additive input for marking a WHOLE connector sensitive:
// connectors are namespaced "<connector-name>_<tool-name>"
// (connectors.Manager.Tools), so a connector's own name is the PREFIX
// of every tool it serves, never a suffix — it needs its own match
// rule, not Suffixes' suffix check, to avoid a floor entry like
// "gmail_read" also prefix-matching an unrelated "gmail_read_all"
// tool. Both are funcs, not static slices, so a settings change
// applies to the next side-call without a restart, same reasoning as
// Route.
type SensitiveTools struct {
	Suffixes       func(context.Context) []string
	ConnectorNames func(context.Context) []string
	Route          func(context.Context) string
}

// Matches reports whether toolName is covered: exact match or
// "_"+suffix suffix against Suffixes, or suffix+"_" prefix (a whole
// connector's namespace) against ConnectorNames.
func (s *SensitiveTools) Matches(ctx context.Context, toolName string) bool {
	if s == nil {
		return false
	}
	for _, suffix := range s.Suffixes(ctx) {
		if toolName == suffix || strings.HasSuffix(toolName, "_"+suffix) {
			return true
		}
	}
	if s.ConnectorNames != nil {
		for _, name := range s.ConnectorNames(ctx) {
			if strings.HasPrefix(toolName, name+"_") {
				return true
			}
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
