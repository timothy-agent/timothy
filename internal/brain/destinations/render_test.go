package destinations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

func TestRender(t *testing.T) {
	m := missions.Mission{ID: "m1", Goal: "ship the thing", Name: "Ship it"}
	digest := "mission goal: ship the thing\nterminal state: done\n"

	t.Run("no web base url, no branch, no pr", func(t *testing.T) {
		p := Render(m, digest, "", nil)
		if p.MissionID != "m1" || p.Name != "Ship it" || p.Goal != "ship the thing" || p.Body != digest {
			t.Fatalf("unexpected payload: %+v", p)
		}
		if len(p.Links) != 0 {
			t.Fatalf("expected no links, got %v", p.Links)
		}
	})

	t.Run("with web base url", func(t *testing.T) {
		p := Render(m, digest, "https://timothy.example.lan/", nil)
		if len(p.Links) != 1 || p.Links[0] != "https://timothy.example.lan/missions/m1" {
			t.Fatalf("unexpected links: %v", p.Links)
		}
	})

	t.Run("with branch", func(t *testing.T) {
		coding := m
		coding.Branch = "feat/ship-it"
		p := Render(coding, digest, "", nil)
		if len(p.Links) != 1 || p.Links[0] != "branch: feat/ship-it" {
			t.Fatalf("unexpected links: %v", p.Links)
		}
	})

	t.Run("with pr opened event", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]any{"url": "https://github.com/org/repo/pull/1", "number": 1})
		events := []missions.Event{{Kind: "mission.pr_opened", Payload: payload}}
		p := Render(m, digest, "", events)
		if len(p.Links) != 1 || p.Links[0] != "https://github.com/org/repo/pull/1" {
			t.Fatalf("unexpected links: %v", p.Links)
		}
	})

	t.Run("branch and pr and web base url together", func(t *testing.T) {
		coding := m
		coding.Branch = "feat/ship-it"
		payload, _ := json.Marshal(map[string]any{"url": "https://github.com/org/repo/pull/1"})
		events := []missions.Event{{Kind: "mission.pr_opened", Payload: payload}}
		p := Render(coding, digest, "https://timothy.example.lan", events)
		want := []string{
			"https://timothy.example.lan/missions/m1",
			"branch: feat/ship-it",
			"https://github.com/org/repo/pull/1",
		}
		if len(p.Links) != len(want) {
			t.Fatalf("links = %v, want %v", p.Links, want)
		}
		for i := range want {
			if p.Links[i] != want[i] {
				t.Fatalf("links[%d] = %q, want %q", i, p.Links[i], want[i])
			}
		}
	})

	t.Run("falls back to goal when name empty", func(t *testing.T) {
		noName := m
		noName.Name = ""
		p := Render(noName, digest, "", nil)
		if p.Name != "ship the thing" {
			t.Fatalf("Name = %q, want fallback to goal", p.Name)
		}
	})

	t.Run("includes declared artifact files", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "report.md"), []byte("report"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		withArtifact := m
		withArtifact.Workspace = root
		withArtifact.Spec = missions.Spec{Units: []missions.PlanUnit{{Artifacts: []string{"report.md"}}}}
		p := Render(withArtifact, digest, "", nil)
		if len(p.Files) != 1 || p.Files[0].Name != "report.md" {
			t.Fatalf("expected report.md attached, got %+v", p.Files)
		}
	})
}
