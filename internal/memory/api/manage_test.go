package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/extract"
	"github.com/SumonMSelim/timothy/internal/memory/store"
)

type fakeManager struct {
	memories      map[string]store.Memory
	listed        []store.Memory
	promoted      []string
	rejected      []string
	inserted      []store.Memory
	superseded    map[string]string
	nextID        int
	entities      []store.Entity
	edges         []store.EntityEdge
	entityMems    map[string][]store.Memory
	nearestID     string
	nearestSim    float64
	nearestStatus store.Status
	nearestFound  bool
	nearestErr    error
	confirmed     []string
	confirmErr    error
	insertErr     error
}

func newFakeManager() *fakeManager {
	return &fakeManager{memories: map[string]store.Memory{}, superseded: map[string]string{}}
}

func (f *fakeManager) ListByStatus(_ context.Context, status store.Status, types ...store.MemoryType) ([]store.Memory, error) {
	return f.listed, nil
}

func (f *fakeManager) Insert(_ context.Context, m store.Memory) (string, error) {
	if f.insertErr != nil {
		return "", f.insertErr
	}
	f.nextID++
	m.ID = "new-" + strings.Repeat("x", f.nextID)
	f.inserted = append(f.inserted, m)
	return m.ID, nil
}

func (f *fakeManager) NearestActive(_ context.Context, _ store.Vector) (string, float64, store.Status, bool, error) {
	return f.nearestID, f.nearestSim, f.nearestStatus, f.nearestFound, f.nearestErr
}

func (f *fakeManager) Confirm(_ context.Context, id string) error {
	f.confirmed = append(f.confirmed, id)
	return f.confirmErr
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

type addMemoryEmbedder struct {
	vectors [][]float32
	err     error
}

func (e addMemoryEmbedder) Embed(context.Context, []string, string) ([][]float32, string, error) {
	return e.vectors, "add-memory", e.err
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
	if m.Actor != store.ActorUser || m.Type != store.TypeProcedural || len(m.Embedding) == 0 || m.RequireReview {
		t.Fatalf("inserted = %+v", m)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode add result: %v", err)
	}
	if out["id"] == "" || out["status"] != string(store.StatusActive) {
		t.Fatalf("add result = %v, want active id", out)
	}
}

func TestAddTaintedFactRequiresReview(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"weekly reports go to reports@example.com","require_review":true}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(fm.inserted) != 1 || !fm.inserted[0].RequireReview {
		t.Fatalf("inserted = %+v, want review-required memory", fm.inserted)
	}
	if !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("result = %s, want pending status", rec.Body)
	}
}

func TestAddSensitiveFactRequiresReview(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"The user always wants weekly reports to be emailed."}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(fm.inserted) != 1 || !fm.inserted[0].RequireReview {
		t.Fatalf("inserted = %+v, want sensitive memory held for review", fm.inserted)
	}
	if !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("result = %s, want pending status", rec.Body)
	}
}

func TestAddRejectedNearDuplicateIsDroppedAndLogged(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "rejected-1", 0.95, store.StatusRejected, true
	var log bytes.Buffer
	a := manageAPI(fm)
	a.log = slog.New(slog.NewTextHandler(&log, nil))
	content := "User lives in Porto."
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"`+content+`"}`))
	rec := httptest.NewRecorder()
	a.handleAdd(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(fm.inserted) != 0 || len(fm.confirmed) != 0 {
		t.Fatalf("inserted=%d confirmed=%v, want dropped", len(fm.inserted), fm.confirmed)
	}
	if !strings.Contains(log.String(), "memory dropped as near-duplicate of rejected fact") {
		t.Fatalf("log = %q, want rejected-duplicate record", log.String())
	}
	if strings.Contains(log.String(), content) {
		t.Fatalf("log contains memory content: %q", log.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"dropped"`) {
		t.Fatalf("result = %s, want dropped status", rec.Body)
	}
}

func TestAddPendingNearDuplicateReusesReviewItem(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "pending-1", 0.96, store.StatusPending, true
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto."}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK || len(fm.inserted) != 0 {
		t.Fatalf("status=%d inserted=%d body=%s", rec.Code, len(fm.inserted), rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"id":"pending-1"`) ||
		!strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("result = %s, want existing pending id", rec.Body)
	}
}

func TestAddActiveNearDuplicateConfirmsExistingMemory(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "active-1", 0.96, store.StatusActive, true
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto."}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK || len(fm.inserted) != 0 {
		t.Fatalf("status=%d inserted=%d body=%s", rec.Code, len(fm.inserted), rec.Body)
	}
	if len(fm.confirmed) != 1 || fm.confirmed[0] != "active-1" {
		t.Fatalf("confirmed = %v, want [active-1]", fm.confirmed)
	}
	if !strings.Contains(rec.Body.String(), `"status":"active"`) {
		t.Fatalf("result = %s, want active status", rec.Body)
	}
}

func TestAddReviewRequiredActiveNearDuplicateStaysPending(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "active-1", 0.96, store.StatusActive, true
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto.","require_review":true}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK || len(fm.inserted) != 1 {
		t.Fatalf("status=%d inserted=%d body=%s", rec.Code, len(fm.inserted), rec.Body)
	}
	if len(fm.confirmed) != 0 || !fm.inserted[0].RequireReview {
		t.Fatalf("confirmed=%v inserted=%+v, want pending insertion without confirming active row", fm.confirmed, fm.inserted)
	}
	if !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("result = %s, want pending status", rec.Body)
	}
}

func TestAddDedupFailureDoesNotInsert(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestErr = errors.New("database unavailable")
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto."}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(fm.inserted) != 0 {
		t.Fatalf("inserted = %d, want fail-closed dedup", len(fm.inserted))
	}
}

func TestAddInsertFailureReturnsServerError(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.insertErr = errors.New("database unavailable")
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto."}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "insert_failed") {
		t.Fatalf("status=%d body=%s, want insert_failed", rec.Code, rec.Body)
	}
	if len(fm.inserted) != 0 {
		t.Fatalf("inserted = %d, want 0", len(fm.inserted))
	}
}

func TestAddContinuesWhenEmbeddingIsUnavailable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		emb  addMemoryEmbedder
	}{
		{name: "embedding error", emb: addMemoryEmbedder{err: errors.New("route unavailable")}},
		{name: "empty embedding result", emb: addMemoryEmbedder{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fm := newFakeManager()
			a := manageAPI(fm)
			a.embed = tc.emb
			req := httptest.NewRequest(http.MethodPost, "/v1/memories",
				strings.NewReader(`{"content":"User lives in Porto."}`))
			rec := httptest.NewRecorder()
			a.handleAdd(rec, req)
			if rec.Code != http.StatusOK || len(fm.inserted) != 1 {
				t.Fatalf("status=%d inserted=%d body=%s", rec.Code, len(fm.inserted), rec.Body)
			}
			if len(fm.inserted[0].Embedding) != 0 || fm.inserted[0].RequireReview {
				t.Fatalf("inserted = %+v, want clean memory without embedding", fm.inserted[0])
			}
			if !strings.Contains(rec.Body.String(), `"status":"active"`) {
				t.Fatalf("result = %s, want active status", rec.Body)
			}
		})
	}
}

func TestAddDuplicateFallbacks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		similarity float64
		status     store.Status
	}{
		{name: "below threshold", similarity: extract.NearDupSimilarity - 0.01, status: store.StatusActive},
		{name: "unknown duplicate status", similarity: 1, status: "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fm := newFakeManager()
			fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "existing", tc.similarity, tc.status, true
			req := httptest.NewRequest(http.MethodPost, "/v1/memories",
				strings.NewReader(`{"content":"User lives in Porto."}`))
			rec := httptest.NewRecorder()
			manageAPI(fm).handleAdd(rec, req)
			if rec.Code != http.StatusOK || len(fm.inserted) != 1 {
				t.Fatalf("status=%d inserted=%d body=%s", rec.Code, len(fm.inserted), rec.Body)
			}
			if len(fm.confirmed) != 0 {
				t.Fatalf("confirmed = %v, want no confirmation", fm.confirmed)
			}
		})
	}
}

func TestAddDuplicateConfirmationFailureStillSucceeds(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "active-1", 0.96, store.StatusActive, true
	fm.confirmErr = errors.New("confirmation unavailable")
	var log bytes.Buffer
	a := manageAPI(fm)
	a.log = slog.New(slog.NewTextHandler(&log, nil))
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto."}`))
	rec := httptest.NewRecorder()
	a.handleAdd(rec, req)
	if rec.Code != http.StatusOK || len(fm.inserted) != 0 {
		t.Fatalf("status=%d inserted=%d body=%s", rec.Code, len(fm.inserted), rec.Body)
	}
	if len(fm.confirmed) != 1 || fm.confirmed[0] != "active-1" {
		t.Fatalf("confirmed = %v, want [active-1]", fm.confirmed)
	}
	if !strings.Contains(rec.Body.String(), `"status":"active"`) ||
		!strings.Contains(log.String(), "confirm on duplicate failed; fact still dropped") {
		t.Fatalf("result=%s log=%q, want active response and warning", rec.Body, log.String())
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
