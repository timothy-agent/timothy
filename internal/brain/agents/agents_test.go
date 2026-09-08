package agents

import (
	"strings"
	"testing"
)

// TestValidateName covers the plain-text name rule (issue #615):
// trimmed, 1..64 runes, no control characters. Spaces and capitals are
// accepted; the old lowercase-slug pattern is gone.
func TestValidateName(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"plain lowercase", "researcher", "researcher", false},
		{"spaces and capitals", "Research Bot", "Research Bot", false},
		{"punctuation", "Bot! (v2)", "Bot! (v2)", false},
		{"trims surrounding whitespace", "  Research Bot  ", "Research Bot", false},
		{"empty rejected", "", "", true},
		{"blank rejected", "   ", "", true},
		{"exactly 64 runes accepted", strings.Repeat("a", 64), strings.Repeat("a", 64), false},
		{"65 runes rejected", strings.Repeat("a", 65), "", true},
		{"control character rejected", "bad\tname", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := validateName(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateName(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			}
			if err == nil && got != c.want {
				t.Fatalf("validateName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
