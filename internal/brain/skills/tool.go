package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

type loadArgs struct {
	Name string `json:"name"`
}

// LoadSkillTool returns the load_skill tool over an already-loaded
// pack set. The body comes back as a transient tool result — it is
// never copied into the system prompt.
func LoadSkillTool(packs []Skill) *tools.Tool {
	byName := make(map[string]Skill, len(packs))
	names := make([]string, 0, len(packs))
	for _, s := range packs {
		byName[s.Name] = s
		names = append(names, s.Name)
	}
	return &tools.Tool{
		Name: "load_skill",
		Description: `Loads a skill pack: the working rules for a kind of task.

Use when the current task matches a skill's description in your skill
index — load it BEFORE starting the work, then follow its rules for
the rest of the task.

Arguments:
- name (string, required): the skill's name exactly as it appears in
  the index, e.g. "` + names[0] + `".

Returns the skill's full rule set as text. Loading is cheap; loading
the wrong skill is not — pick by the "Use when" trigger, not by the
name alone.

Example: {"name": "` + names[0] + `"} → the pack's rules.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Skill name from the index"
				}
			},
			"required": ["name"],
			"additionalProperties": false
		}`),
		Execute: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args loadArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			s, ok := byName[args.Name]
			if !ok {
				return "", fmt.Errorf("unknown skill %q — available: %s", args.Name, strings.Join(names, ", "))
			}
			return fmt.Sprintf("# Skill: %s\n\n%s", s.Name, s.Body), nil
		},
	}
}
