package destinations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

func TestRender(t *testing.T) {
	m := missions.Mission{ID: "m1", Goal: "ship the thing", Name: "Ship it"}

	t.Run("no web base url, no branch, no pr", func(t *testing.T) {
		p := Render(m, "", nil)
		if p.MissionID != "m1" || p.Name != "Ship it" || p.Goal != "ship the thing" || p.Body != "Mission complete: Ship it" {
			t.Fatalf("unexpected payload: %+v", p)
		}
		if len(p.Links) != 0 {
			t.Fatalf("expected no links, got %v", p.Links)
		}
	})

	t.Run("with web base url", func(t *testing.T) {
		p := Render(m, "https://timothy.example.lan/", nil)
		if len(p.Links) != 1 || p.Links[0] != "https://timothy.example.lan/missions/m1" {
			t.Fatalf("unexpected links: %v", p.Links)
		}
	})

	t.Run("with branch", func(t *testing.T) {
		coding := m
		coding.Branch = "feat/ship-it"
		p := Render(coding, "", nil)
		if len(p.Links) != 1 || p.Links[0] != "branch: feat/ship-it" {
			t.Fatalf("unexpected links: %v", p.Links)
		}
	})

	t.Run("with pr opened event", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]any{"url": "https://github.com/org/repo/pull/1", "number": 1})
		events := []missions.Event{{Kind: "mission.pr_opened", Payload: payload}}
		p := Render(m, "", events)
		if len(p.Links) != 1 || p.Links[0] != "https://github.com/org/repo/pull/1" {
			t.Fatalf("unexpected links: %v", p.Links)
		}
	})

	t.Run("branch and pr and web base url together", func(t *testing.T) {
		coding := m
		coding.Branch = "feat/ship-it"
		payload, _ := json.Marshal(map[string]any{"url": "https://github.com/org/repo/pull/1"})
		events := []missions.Event{{Kind: "mission.pr_opened", Payload: payload}}
		p := Render(coding, "https://timothy.example.lan", events)
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
		p := Render(noName, "", nil)
		if p.Name != "ship the thing" {
			t.Fatalf("Name = %q, want fallback to goal", p.Name)
		}
		if p.Body != "Mission complete: ship the thing" {
			t.Fatalf("Body = %q, want completion line using the goal fallback", p.Body)
		}
	})

	t.Run("renders declared markdown artifacts inline, not attached", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "report.md"), []byte("report"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		withArtifact := m
		withArtifact.Workspace = root
		withArtifact.Spec = missions.Spec{Units: []missions.PlanUnit{{Artifacts: []string{"report.md"}}}}
		p := Render(withArtifact, "", nil)
		if len(p.Files) != 0 {
			t.Fatalf("expected report.md NOT attached as a file, got %+v", p.Files)
		}
		if len(p.TextArtifacts) != 1 || p.TextArtifacts[0].Name != "report.md" || p.TextArtifacts[0].Content != "report" {
			t.Fatalf("expected report.md rendered inline, got %+v", p.TextArtifacts)
		}
	})

	t.Run("still attaches non-markdown declared artifact files", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "data.csv"), []byte("a,b\n1,2\n"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		withArtifact := m
		withArtifact.Workspace = root
		withArtifact.Spec = missions.Spec{Units: []missions.PlanUnit{{Artifacts: []string{"data.csv"}}}}
		p := Render(withArtifact, "", nil)
		if len(p.TextArtifacts) != 0 {
			t.Fatalf("expected data.csv NOT rendered inline, got %+v", p.TextArtifacts)
		}
		if len(p.Files) != 1 || p.Files[0].Name != "data.csv" {
			t.Fatalf("expected data.csv attached, got %+v", p.Files)
		}
	})

	t.Run("light mission body is the final output, not a completion line", func(t *testing.T) {
		light := m
		light.Light = true
		light.FinalOutput = "here is the complete deliverable"
		p := Render(light, "", nil)
		if p.Body != "here is the complete deliverable" {
			t.Fatalf("Body = %q, want the light mission's final output", p.Body)
		}
	})

	t.Run("light mission with no final output falls back to the completion line", func(t *testing.T) {
		light := m
		light.Light = true
		p := Render(light, "", nil)
		if p.Body != "Mission complete: Ship it" {
			t.Fatalf("Body = %q, want the completion-line fallback", p.Body)
		}
	})

	t.Run("CompletedAt carries the mission's UpdatedAt in UTC", func(t *testing.T) {
		withTime := m
		parsed, err := time.Parse(time.RFC3339, "2026-08-21T20:30:00+02:00")
		if err != nil {
			t.Fatalf("parse fixture time: %v", err)
		}
		withTime.UpdatedAt = parsed
		p := Render(withTime, "", nil)
		if p.CompletedAt.IsZero() {
			t.Fatal("CompletedAt is zero, want the mission's UpdatedAt")
		}
		if got, want := p.CompletedAt.UTC().Format("2006-01-02T15:04:05Z"), "2026-08-21T18:30:00Z"; got != want {
			t.Fatalf("CompletedAt = %q, want %q", got, want)
		}
	})
}
