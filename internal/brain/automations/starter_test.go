package automations

import (
	"slices"
	"testing"
)

// TestIntersectToolAllowlist pins issue #857's ceiling: a trigger entry
// survives only when the agent's Tools grant it, exactly or by
// connector suffix, or when it names a note tool.
func TestIntersectToolAllowlist(t *testing.T) {
	t.Parallel()
	agent := []string{"shell", "search_web", "gmail_search_mail"}
	cases := []struct {
		name    string
		trigger []string
		want    []string
	}{
		{"empty trigger list", nil, nil},
		{"subset", []string{"shell"}, []string{"shell"}},
		{"superset drops what the agent lacks", []string{"shell", "search_web", "fetch_url"}, []string{"shell", "search_web"}},
		{"unknown names dropped", []string{"no_such_tool"}, nil},
		{"suffix entry kept", []string{"search_mail"}, []string{"search_mail"}},
		{"empty intersection", []string{"write_file"}, nil},
		{"note tools pass the agent ceiling", []string{"read_note", "write_note", "fetch_url"}, []string{"read_note", "write_note"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := intersectToolAllowlist(tc.trigger, agent); !slices.Equal(got, tc.want) {
				t.Fatalf("intersectToolAllowlist(%v) = %v, want %v", tc.trigger, got, tc.want)
			}
		})
	}
	if got := intersectToolAllowlist([]string{"shell"}, nil); got != nil {
		t.Fatalf("agent without tools = %v, want nil", got)
	}
}
