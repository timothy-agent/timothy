package session

import "strings"

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
type SensitiveTools struct {
	Suffixes []string
	Route    string
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
