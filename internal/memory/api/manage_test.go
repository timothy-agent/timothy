package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/store"
)

type fakeManager struct {
	memories   map[string]store.Memory
	listed     []store.Memory
	promoted   []string
	rejected   []string
	inserted   []store.Memory
	superseded map[string]string
	nextID     int
}

func newFakeManager() *fakeManager {
	return &fakeManager{memories: map[string]store.Memory{}, superseded: map[string]string{}}
}

func (f *fakeManager) ListByStatus(_ context.Context, status store.Status, types ...store.MemoryType) ([]store.Memory, error) {
	return f.listed, nil
}

func (f *fakeManager) Insert(_ context.Context, m store.Memory) (string, error) {
	f.nextID++
	m.ID = "new-" + strings.Repeat("x", f.nextID)
	f.inserted = append(f.inserted, m)
	return m.ID, nil
}

func (f *fakeManager) Promote(_ context.Context, id string) error {
	f.promoted = append(f.promoted, id)
	return nil
}

func (f *fakeManager) Reject(_ context.Context, id string) error {
	f.rejected = append(f.rejected, id)
	return nil
}

func (f *fakeManager) Supersede(_ context.Context, oldID, newID string) error {
	f.superseded[oldID] = newID
	return nil
}

func (f *fakeManager) Chain(_ context.Context, id string) ([]store.Memory, error) {
	if m, ok := f.memories[id]; ok {
		return []store.Memory{m}, nil
	}
	return []store.Memory{{ID: id, Type: store.TypeSemantic, CreatedAt: time.Now()}}, nil
}

func manageAPI(m Manager) *API {
	return &API{store: m, embed: &fakeEmbedder{},
		log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestListDefaultsToPendingQueue(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.listed = []store.Memory{{ID: "p1", Type: store.TypeSemantic, Content: "fact", Status: store.StatusPending, CreatedAt: time.Now()}}
	req := httptest.NewRequest(http.MethodGet, "/v1/memories", nil)
	rec := httptest.NewRecorder()
	manageAPI(fm).handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Memories []memoryJSON `json:"memories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Memories) != 1 || out.Memories[0].ID != "p1" {
		t.Fatalf("out = %+v", out)
	}
}

func TestAddStoresUserExplicit(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"remember I use colima","type":"procedural"}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(fm.inserted) != 1 {
		t.Fatalf("inserted = %d", len(fm.inserted))
	}
	m := fm.inserted[0]
	if m.Actor != store.ActorUser || m.Type != store.TypeProcedural || len(m.Embedding) == 0 {
		t.Fatalf("inserted = %+v", m)
	}
}

func TestAddRejectsEmptyContent(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/v1/memories", strings.NewReader(`{"content":"  "}`))
	rec := httptest.NewRecorder()
	manageAPI(newFakeManager()).handleAdd(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func resolve(t *testing.T, fm *fakeManager, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/memories/"+id, strings.NewReader(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	manageAPI(fm).handleResolve(rec, req)
	return rec
}

func TestResolveConfirm(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	rec := resolve(t, fm, "m1", `{"action":"confirm"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(fm.promoted) != 1 || fm.promoted[0] != "m1" {
		t.Fatalf("promoted = %v", fm.promoted)
	}
}

func TestResolveReject(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	rec := resolve(t, fm, "m1", `{"action":"reject"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(fm.rejected) != 1 {
		t.Fatalf("rejected = %v", fm.rejected)
	}
}

func TestResolveEditSupersedes(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.memories["m1"] = store.Memory{
		ID: "m1", Type: store.TypeSemantic, Content: "user lives in Berlin",
		Status: store.StatusPending, SourceSession: "s9", CreatedAt: time.Now(),
	}
	rec := resolve(t, fm, "m1", `{"action":"confirm","content":"user lives in Porto"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(fm.inserted) != 1 {
		t.Fatalf("inserted = %d, want the corrected fact", len(fm.inserted))
	}
	corrected := fm.inserted[0]
	if corrected.Content != "user lives in Porto" || corrected.Actor != store.ActorUser {
		t.Fatalf("corrected = %+v", corrected)
	}
	if corrected.SourceSession != "s9" {
		t.Fatalf("provenance lost: %+v", corrected)
	}
	if fm.superseded["m1"] == "" {
		t.Fatal("original not superseded")
	}
	if len(fm.promoted) != 0 {
		t.Fatal("edited confirm must not ALSO promote the original")
	}
}

func TestResolveUnknownAction(t *testing.T) {
	t.Parallel()
	rec := resolve(t, newFakeManager(), "m1", `{"action":"archive"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestChainEndpoint(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.memories["m1"] = store.Memory{ID: "m1", Type: store.TypeSemantic, Content: "v1", Status: store.StatusArchived, SupersededBy: "m2", CreatedAt: time.Now()}
	req := httptest.NewRequest(http.MethodGet, "/v1/memories/m1/chain", nil)
	req.SetPathValue("id", "m1")
	rec := httptest.NewRecorder()
	manageAPI(fm).handleChain(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"superseded_by":"m2"`) {
		t.Fatalf("body = %s", rec.Body)
	}
}
