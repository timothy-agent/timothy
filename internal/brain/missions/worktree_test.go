package missions

import "testing"

func TestSlug(t *testing.T) {
	cases := []struct {
		name, goal, id, want string
	}{
		{"normal goal", "Fix the login bug", "abc12345-full-id", "fix-the-login-bug"},
		{"punctuation and unicode collapse to hyphens", "Add caché! (v2.0)", "abc12345-full-id", "add-cach-v2-0"},
		{
			"exactly at the cap",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 40 a's
			"abc12345-full-id",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			"over the cap truncates and trims a trailing hyphen",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbb", // 40 a's then more
			"abc12345-full-id",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{"empty goal falls back to id prefix", "", "abc12345-full-id", "m-abc12345"},
		{"all-punctuation goal falls back to id prefix", "!!!???...", "abc12345-full-id", "m-abc12345"},
		{"short id is used whole in the fallback", "", "abc", "m-abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Slug(tc.goal, tc.id)
			if got != tc.want {
				t.Fatalf("Slug(%q, %q) = %q, want %q", tc.goal, tc.id, got, tc.want)
			}
			if len(got) == 0 {
				t.Fatal("Slug returned empty string")
			}
		})
	}
}
