package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
)

// TestRuntimeToolsInCatalog keeps builtin.Catalog (the manifest's tool
// list) a superset of what brain registers, so a new tool cannot ship
// undocumented.
func TestRuntimeToolsInCatalog(t *testing.T) {
	t.Parallel()
	catalog := map[string]bool{}
	for _, tool := range builtin.Catalog() {
		catalog[tool.Name] = true
	}
	packs := []skills.Skill{{Name: "fixture", Description: "Use when testing."}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, _, _, set, _, err := buildAgent(nil, nil, nil, t.TempDir(), "http://searxng:8080", "", packs, nil, nil, nil, log, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range set {
		if tool.Name != "load_skill" {
			names = append(names, tool.Name)
		}
	}
	// Registered later in main() behind env-gated dependencies, so
	// buildAgent cannot reach them: deliver, mission tools, share_file,
	// generate_pdf. Chat and mission turns add the kb, memory and
	// write_file tools themselves.
	names = append(names, "deliver", "list_missions", "get_mission", "followup_mission",
		"create_mission", "push_mission_branch", "share_file", "generate_pdf",
		"search_kb", "read_kb", "writing_samples", "search_memory", "write_file")
	for _, name := range names {
		if !catalog[name] {
			t.Errorf("runtime tool %q missing from builtin.Catalog", name)
		}
	}
}
