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
type SensitiveTools struct {
	Suffixes []string
	Route    func(context.Context) string
}

// Matches reports whether toolName ends a sensitive suffix: exact
// match, or toolName ends with "_"+suffix.
func (s *SensitiveTools) Matches(toolName string) bool {
	if s == nil {
		return false
	}
	for _, suffix := range s.Suffixes {
		if toolName == suffix || strings.HasSuffix(toolName, "_"+suffix) {
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
func (s *SensitiveTools) SessionSensitive(events []Event) bool {
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
		if s.Matches(te.Name) {
			return true
		}
	}
	return false
}
