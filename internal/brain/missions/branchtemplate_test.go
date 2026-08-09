package missions

import "testing"

func TestValidateBranchPattern(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{"default pattern", DefaultBranchPattern, false},
		{"empty", "", true},
		{"unknown placeholder", "{oops}/{slug}", true},
		{"login placeholder", "{type}/{login}/{slug}", false},
		{"date placeholder", "{date}-{slug}", false},
		{"all placeholders", "{type}/{login}/{date}/{slug}", false},
		{"leading traversal", "../{slug}", true},
		{"embedded traversal", "{type}/../{slug}", true},
		{"leading slash literal", "/{type}/{slug}", false}, // collapses to type/slug
		{"disallowed char space", "{type} /{slug}", true},
		{"disallowed char tilde", "{type}~/{slug}", true},
		{"disallowed char caret", "{type}^/{slug}", true},
		{"disallowed char colon", "{type}:{slug}", true},
		{"disallowed char question mark", "{type}?/{slug}", true},
		{"disallowed char asterisk", "{type}*/{slug}", true},
		{"disallowed char bracket", "{type}[x]/{slug}", true},
		{"disallowed char backslash", `{type}\{slug}`, true},
		{"no placeholders, plain literal", "always-this-branch", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBranchPattern(tc.pattern)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateBranchPattern(%q) error = %v, wantErr %v", tc.pattern, err, tc.wantErr)
			}
		})
	}
}

func TestExpandBranchPattern(t *testing.T) {
	cases := []struct {
		name                   string
		pattern                string
		typ, slug, login, date string
		want                   string
	}{
		{
			"default shape",
			DefaultBranchPattern,
			"feat", "add-dark-mode", "", "20260101",
			"feat/add-dark-mode",
		},
		{
			"login present",
			"{type}/{login}/{slug}",
			"fix", "login-bug", "octocat", "20260101",
			"fix/octocat/login-bug",
		},
		{
			"login empty collapses the double slash",
			"{type}/{login}/{slug}",
			"fix", "login-bug", "", "20260101",
			"fix/login-bug",
		},
		{
			"login empty at the start collapses leading slash",
			"{login}/{type}/{slug}",
			"fix", "login-bug", "", "20260101",
			"fix/login-bug",
		},
		{
			"date placeholder substitutes",
			"{date}-{type}-{slug}",
			"chore", "bump-deps", "", "20260315",
			"20260315-chore-bump-deps",
		},
		{
			"every placeholder used",
			"{login}/{date}/{type}/{slug}",
			"docs", "readme-update", "octocat", "20260315",
			"octocat/20260315/docs/readme-update",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandBranchPattern(tc.pattern, tc.typ, tc.slug, tc.login, tc.date)
			if got != tc.want {
				t.Fatalf("ExpandBranchPattern(%q, ...) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestValidateCommitStyle(t *testing.T) {
	cases := []struct {
		style   string
		wantErr bool
	}{
		{"", false},
		{CommitStyleConventional, false},
		{CommitStylePlain, false},
		{"loud", true},
		{"CONVENTIONAL", true},
	}
	for _, tc := range cases {
		t.Run(tc.style, func(t *testing.T) {
			err := ValidateCommitStyle(tc.style)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateCommitStyle(%q) error = %v, wantErr %v", tc.style, err, tc.wantErr)
			}
		})
	}
}
