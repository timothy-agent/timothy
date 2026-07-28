package session

import "testing"

// TestSensitiveToolsMatches pins the suffix semantics: exact name, or
// name ending "_"+suffix (connector namespacing), same rule as
// loop.Agent.SetForceRoute/matchGrant (D-036).
func TestSensitiveToolsMatches(t *testing.T) {
	t.Parallel()
	s := &SensitiveTools{Suffixes: []string{"gmail_read", "gmail_read_attachment"}, Route: "local"}
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
			if got := s.Matches(tc.tool); got != tc.want {
				t.Fatalf("Matches(%q) = %v, want %v", tc.tool, got, tc.want)
			}
		})
	}
}

func TestSensitiveToolsMatchesNilReceiver(t *testing.T) {
	t.Parallel()
	var s *SensitiveTools
	if s.Matches("gmail_read") {
		t.Fatal("nil SensitiveTools matched a tool, want false (feature off)")
	}
}
