package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/store"
)

func (f *fakeManager) ListEntities(_ context.Context) ([]store.Entity, error) {
	return f.entities, nil
}

func (f *fakeManager) EntityEdges(_ context.Context) ([]store.EntityEdge, error) {
	return f.edges, nil
}

func (f *fakeManager) ListByEntity(_ context.Context, entityID string) ([]store.Memory, error) {
	return f.entityMems[entityID], nil
}

const entityID = "11111111-1111-4111-8111-111111111111"

func TestEntityGraphShape(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.entities = []store.Entity{{ID: entityID, Type: "project", Name: "timothy", MemoryCount: 3}}
	fm.edges = []store.EntityEdge{{Src: entityID, Dst: "22222222-2222-4222-8222-222222222222", Weight: 2}}

	rec := httptest.NewRecorder()
	manageAPI(fm).handleEntityGraph(rec, httptest.NewRequest(http.MethodGet, "/v1/entities/graph", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var out struct {
		Entities []entityJSON `json:"entities"`
		Edges    []edgeJSON   `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Entities) != 1 || out.Entities[0].Name != "timothy" || out.Entities[0].MemoryCount != 3 {
		t.Fatalf("entities = %+v", out.Entities)
	}
	if len(out.Edges) != 1 || out.Edges[0].Weight != 2 {
		t.Fatalf("edges = %+v", out.Edges)
	}
}

func TestEntityGraphEmptyIsArrays(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	manageAPI(newFakeManager()).handleEntityGraph(rec, httptest.NewRequest(http.MethodGet, "/v1/entities/graph", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"entities":[]`) || !strings.Contains(body, `"edges":[]`) {
		t.Fatalf("empty graph must encode arrays, got %s", body)
	}
}

func TestEntityMemories(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.entityMems = map[string][]store.Memory{
		entityID: {{ID: "m1", Type: store.TypeSemantic, Content: "fact", Status: store.StatusActive, CreatedAt: time.Now()}},
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/entities/"+entityID+"/memories", nil)
	req.SetPathValue("id", entityID)
	rec := httptest.NewRecorder()
	manageAPI(fm).handleEntityMemories(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var out struct {
		Memories []memoryJSON `json:"memories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Memories) != 1 || out.Memories[0].ID != "m1" {
		t.Fatalf("memories = %+v", out.Memories)
	}
}

func TestEntityMemoriesRejectsBadID(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/v1/entities/not-a-uuid/memories", nil)
	req.SetPathValue("id", "not-a-uuid")
	rec := httptest.NewRecorder()
	manageAPI(newFakeManager()).handleEntityMemories(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
