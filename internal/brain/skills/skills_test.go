package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePack(t *testing.T, root, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadValidPacks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePack(t, root, "alpha-skill", strings.ReplaceAll(
		"---\nname: alpha-skill\ndescription: Does alpha things. Use when alpha work appears.\n---\n\n- rule one\n", "\r", ""))
	writePack(t, root, "beta-skill",
		"---\nname: beta-skill\ndescription: Does beta things. Use when beta work appears.\n---\n\n- rule one\n")

	packs, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(packs) != 2 {
		t.Fatalf("loaded %d packs, want 2", len(packs))
	}
	if packs[0].Name != "alpha-skill" || !strings.Contains(packs[0].Body, "rule one") {
		t.Fatalf("pack = %+v", packs[0])
	}
}

func TestLoadRejectsBrokenPacks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dir     string
		content string
		wantErr string
	}{
		{
			name: "missing trigger phrase", dir: "no-trigger",
			content: "---\nname: no-trigger\ndescription: Does things sometimes.\n---\n\n- rule\n",
			wantErr: "Use when",
		},
		{
			name: "dir name mismatch", dir: "wrong-dir",
			content: "---\nname: other-name\ndescription: X. Use when Y.\n---\n\n- rule\n",
			wantErr: "must match name",
		},
		{
			name: "no frontmatter", dir: "bare",
			content: "# just markdown\n",
			wantErr: "missing frontmatter",
		},
		{
			name: "empty body", dir: "empty-body",
			content: "---\nname: empty-body\ndescription: X. Use when Y.\n---\n",
			wantErr: "body is empty",
		},
		{
			name: "oversized description", dir: "big-desc",
			content: "---\nname: big-desc\ndescription: Use when " + strings.Repeat("x", MaxDescription) + "\n---\n\n- rule\n",
			wantErr: "max 1024",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writePack(t, root, tc.dir, tc.content)
			_, err := Load(root)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestIndexIsOneLinePerSkill(t *testing.T) {
	t.Parallel()
	idx := Index([]Skill{
		{Name: "a-skill", Description: "A. Use when a."},
		{Name: "b-skill", Description: "B. Use when b."},
	})
	if !strings.Contains(idx, "- a-skill: A. Use when a.") ||
		!strings.Contains(idx, "- b-skill: B. Use when b.") {
		t.Fatalf("index = %q", idx)
	}
	if !strings.Contains(idx, "load_skill") {
		t.Fatal("index does not tell the model how to load")
	}
	if strings.Count(idx, "\n") != 3 { // header + 2 skills
		t.Fatalf("index has extra lines:\n%s", idx)
	}
}

func TestLoadSkillTool(t *testing.T) {
	t.Parallel()
	tool := LoadSkillTool([]Skill{
		{Name: "coding", Description: "d", Body: "- plan first\n- test first"},
	}, nil)

	args, _ := json.Marshal(map[string]string{"name": "coding"})
	got, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "plan first") || !strings.Contains(got, "# Skill: coding") {
		t.Fatalf("body = %q", got)
	}

	args, _ = json.Marshal(map[string]string{"name": "nope"})
	if _, err := tool.Execute(context.Background(), args); err == nil ||
		!strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("err = %v", err)
	}
}

// A disallowed pack loads exactly like an unknown one, and the
// "available" list never leaks disallowed names.
func TestLoadSkillToolHonorsAllowlist(t *testing.T) {
	t.Parallel()
	tool := LoadSkillTool([]Skill{
		{Name: "allowed", Description: "d", Body: "a"},
		{Name: "blocked", Description: "d", Body: "b"},
	}, func(_ context.Context, name string) bool { return name == "allowed" })

	args, _ := json.Marshal(map[string]string{"name": "blocked"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("err = %v, want unknown-skill refusal", err)
	}
	if strings.Contains(err.Error(), "blocked") && strings.Contains(err.Error(), "available: allowed, blocked") {
		t.Fatalf("available list leaks disallowed pack: %v", err)
	}

	args, _ = json.Marshal(map[string]string{"name": "allowed"})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("allowed pack refused: %v", err)
	}
}

// The repo's real packs must always load.
func TestRepoSkillsAreValid(t *testing.T) {
	t.Parallel()
	packs, err := Load("../../../skills")
	if err != nil {
		t.Fatalf("repo skills invalid: %v", err)
	}
	if len(packs) < 4 {
		t.Fatalf("expected the 4 seed packs, got %d", len(packs))
	}
	for _, s := range packs {
		for _, section := range []string{"Anti-rationalization", "Red flags", "Evidence required"} {
			if !strings.Contains(s.Body, section) {
				t.Errorf("%s missing section %q", s.Name, section)
			}
		}
	}
}

func TestAllowedFiltersOptIn(t *testing.T) {
	packs := []Skill{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	if got := Allowed(packs, nil); got != nil {
		t.Fatalf("empty allowlist must admit nothing, got %v", got)
	}
	got := Allowed(packs, []string{"c", "a", "nope"})
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("Allowed = %v, want [a c] in pack order", got)
	}
}
