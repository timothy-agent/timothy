package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/skills"
)

func TestSkillsEndpointListsLoadedPacks(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerSkills(m.Handle, []skills.Skill{
		{Name: "research", Description: "Use when researching a topic"},
		{Name: "coding-task", Description: "Use when writing code"},
	})

	req := httptest.NewRequest("GET", "/v1/admin/skills", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := decodeSkillsBody(t, w.Body.Bytes())
	if len(body.Skills) != 2 || body.Skills[0].Name != "research" || body.Skills[1].Name != "coding-task" {
		t.Fatalf("skills = %+v", body.Skills)
	}
	if body.Skills[0].Description != "Use when researching a topic" {
		t.Fatalf("description = %q, want Use when researching a topic", body.Skills[0].Description)
	}
}

func TestSkillsEndpointRequiresAuth(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerSkills(m.Handle, []skills.Skill{{Name: "research", Description: "Use when researching a topic"}})

	req := httptest.NewRequest("GET", "/v1/admin/skills", nil)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Fatalf("unauthenticated status = %d, want 401", w.Code)
	}
}

func TestSkillsEndpointUnmountedWhenPacksEmpty(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerSkills(m.Handle, nil)

	req := httptest.NewRequest("GET", "/v1/admin/skills", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code == 200 {
		t.Fatal("nil packs must leave /v1/admin/skills unmounted, got 200")
	}
}

type skillsBody struct {
	Skills []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"skills"`
}

func decodeSkillsBody(t *testing.T, raw []byte) skillsBody {
	t.Helper()
	var body skillsBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}
