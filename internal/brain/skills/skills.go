// Package skills loads SKILL.md capability packs (D-008): versioned
// definition files whose descriptions carry a "Use when …" trigger.
// The system prompt gets a one-line-per-skill index only; bodies load
// on demand through the load_skill tool, so idle skills cost zero
// context.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill is one loaded pack.
type Skill struct {
	Name        string
	Description string
	Body        string
	Dir         string
}

// MaxDescription bounds frontmatter descriptions (D-008).
const MaxDescription = 1024

// maxBodyLines: longer bodies must split details into references/.
const maxBodyLines = 500

// Load reads every */SKILL.md under root, validates, and returns the
// packs sorted by directory walk order. Any invalid pack fails the
// whole load — callers degrade health rather than crash.
func Load(root string) ([]Skill, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("skills: read %s: %w", root, err)
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name(), "SKILL.md")
		raw, err := os.ReadFile(path) //nolint:gosec // path is constructed from the configured skills root
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("skills: read %s: %w", path, err)
		}
		s, err := Parse(string(raw))
		if err != nil {
			return nil, fmt.Errorf("skills: %s: %w", path, err)
		}
		s.Dir = e.Name()
		if err := Validate(s); err != nil {
			return nil, fmt.Errorf("skills: %s: %w", path, err)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("skills: no packs under %s", root)
	}
	return out, nil
}

// Parse splits YAML-ish frontmatter (name, description — flat string
// keys only) from the markdown body.
func Parse(raw string) (Skill, error) {
	const fence = "---"
	trimmed := strings.TrimLeft(raw, "\uFEFF\n\r ")
	if !strings.HasPrefix(trimmed, fence) {
		return Skill{}, fmt.Errorf("missing frontmatter")
	}
	rest := trimmed[len(fence):]
	end := strings.Index(rest, "\n"+fence)
	if end < 0 {
		return Skill{}, fmt.Errorf("unterminated frontmatter")
	}
	front, body := rest[:end], rest[end+len(fence)+1:]

	s := Skill{Body: strings.TrimSpace(body)}
	for line := range strings.Lines(front) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Skill{}, fmt.Errorf("bad frontmatter line %q", line)
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			s.Name = value
		case "description":
			s.Description = value
		}
	}
	return s, nil
}

// Validate enforces the D-008 pack contract.
func Validate(s Skill) error {
	switch {
	case s.Name == "":
		return fmt.Errorf("frontmatter must set name")
	case s.Dir != "" && s.Dir != s.Name:
		return fmt.Errorf("directory %q must match name %q", s.Dir, s.Name)
	case !isKebab(s.Name):
		return fmt.Errorf("name %q must be kebab-case", s.Name)
	case s.Description == "":
		return fmt.Errorf("frontmatter must set description")
	case len(s.Description) > MaxDescription:
		return fmt.Errorf("description is %d chars; max %d", len(s.Description), MaxDescription)
	case !strings.Contains(s.Description, "Use when"):
		return fmt.Errorf(`description must contain a "Use when …" trigger phrase`)
	case s.Body == "":
		return fmt.Errorf("body is empty")
	}
	if n := strings.Count(s.Body, "\n") + 1; n > maxBodyLines {
		return fmt.Errorf("body is %d lines; max %d — split details into references/", n, maxBodyLines)
	}
	return nil
}

func isKebab(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return !strings.HasPrefix(s, "-") && !strings.HasSuffix(s, "-") && s != ""
}

// Index renders the one-line-per-skill system prompt section. Bodies
// never appear here — that is the entire point.
// Allowed filters packs to those named in names — the per-agent skill
// allowlist semantics (empty means none, opt-in only, same contract as
// agents.Agent.Tools). Order follows packs, not names.
func Allowed(packs []Skill, names []string) []Skill {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	out := make([]Skill, 0, len(names))
	for _, p := range packs {
		if set[p.Name] {
			out = append(out, p)
		}
	}
	return out
}

func Index(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Skills available via the load_skill tool (load one BEFORE starting a task its description matches):\n")
	for _, s := range skills {
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
	}
	return b.String()
}
