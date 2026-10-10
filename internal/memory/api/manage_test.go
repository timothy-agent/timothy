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
	memories             map[string]store.Memory
	listed               []store.Memory
	listStatus           store.Status
	listPage             store.Page
	listTypes            []store.MemoryType
	count                int
	countStatus          store.Status
	entityPage           store.Page
	promoted            []string
	rejected             []string
	inserted             []store.Memory
	superseded           map[string]string
	confirmedSuperseding []string
	correctedSuperseding []store.Memory
	nextID               int
	entities             []store.Entity
	edges                []store.EntityEdge
	entityMems           map[string][]store.Memory
	nearestID            string
	nearestSim           float64
	nearestStatus        store.Status
	nearestFound         bool
	nearestErr           error
	confirmed            []string
	confirmErr           error
	insertErr            error
	contentsCalls        int
}

func newFakeManager() *fakeManager {
	return &fakeManager{memories: map[string]store.Memory{}, superseded: map[string]string{}}
}

func (f *fakeManager) ListByStatus(_ context.Context, status store.Status, page store.Page, types ...store.MemoryType) ([]store.Memory, error) {
	f.listStatus, f.listPage, f.listTypes = status, page, types
	return f.listed, nil
}

func (f *fakeManager) CountByStatus(_ context.Context, status store.Status) (int, error) {
	f.countStatus = status
	return f.count, nil
}

func (f *fakeManager) Get(_ context.Context, id string) (store.Memory, error) {
	if m, ok := f.memories[id]; ok {
		return m, nil
	}
	return store.Memory{}, store.ErrNotFound
}

func (f *fakeManager) Contents(_ context.Context, ids []string) (map[string]string, error) {
	f.contentsCalls++
	out := map[string]string{}
	for _, id := range ids {
		if m, ok := f.memories[id]; ok {
			out[id] = m.Content
		}
	}
	return out, nil
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

func (f *fakeManager) ConfirmSuperseding(_ context.Context, id string) error {
	f.confirmedSuperseding = append(f.confirmedSuperseding, id)
	return nil
}

func (f *fakeManager) CorrectSuperseding(_ context.Context, id string, m store.Memory) (string, error) {
	f.correctedSuperseding = append(f.correctedSuperseding, m)
	return "corrected-" + id, nil
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
	if fm.listStatus != store.StatusPending || fm.listPage.Limit != 50 || !fm.listPage.Before.IsZero() {
		t.Fatalf("listStatus=%q page=%+v, want pending first page", fm.listStatus, fm.listPage)
	}
}

func TestListPassesFiltersAndCursor(t *testing.T) {
	t.Parallel()
	const before = "2026-10-01T12:00:00.123456Z"
	const beforeID = "22222222-2222-4222-8222-222222222222"
	fm := newFakeManager()
	rec := httptest.NewRecorder()
	manageAPI(fm).handleList(rec, httptest.NewRequest(http.MethodGet,
		"/v1/memories?status=active&types=episodic,semantic&limit=10&before="+before+"&before_id="+beforeID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	want, _ := time.Parse(time.RFC3339Nano, before)
	if fm.listStatus != store.StatusActive || len(fm.listTypes) != 2 || fm.listTypes[0] != store.TypeEpisodic ||
		fm.listPage.Limit != 10 || fm.listPage.BeforeID != beforeID || !fm.listPage.Before.Equal(want) {
		t.Fatalf("status=%q types=%v page=%+v", fm.listStatus, fm.listTypes, fm.listPage)
	}
}

func TestListRejectsBadPageParams(t *testing.T) {
	t.Parallel()
	for _, q := range []string{
		"before=2026-10-01T12:00:00Z",
		"before_id=22222222-2222-4222-8222-222222222222",
		"before=yesterday&before_id=22222222-2222-4222-8222-222222222222",
		"before=2026-10-01T12:00:00Z&before_id=nope",
		"limit=0",
		"limit=201",
	} {
		rec := httptest.NewRecorder()
		manageAPI(newFakeManager()).handleList(rec, httptest.NewRequest(http.MethodGet, "/v1/memories?"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}

func TestListEncodesFullPrecisionCreatedAt(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.listed = []store.Memory{{ID: "p1", Status: store.StatusPending,
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC)}}
	rec := httptest.NewRecorder()
	manageAPI(fm).handleList(rec, httptest.NewRequest(http.MethodGet, "/v1/memories", nil))
	if !strings.Contains(rec.Body.String(), `"created_at":"2026-10-01T12:00:00.123456Z"`) {
		t.Fatalf("body = %s, want microsecond created_at for the cursor", rec.Body)
	}
}

func TestCountByStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		query string
		want  store.Status
	}{
		{"", store.StatusPending},
		{"?status=active", store.StatusActive},
	} {
		fm := newFakeManager()
		fm.count = 7
		rec := httptest.NewRecorder()
		manageAPI(fm).handleCount(rec, httptest.NewRequest(http.MethodGet, "/v1/memories/count"+tc.query, nil))
		if rec.Code != http.StatusOK || fm.countStatus != tc.want {
			t.Fatalf("%q: status=%d countStatus=%q", tc.query, rec.Code, fm.countStatus)
		}
		if strings.TrimSpace(rec.Body.String()) != `{"count":7}` {
			t.Fatalf("%q: body = %s", tc.query, rec.Body)
		}
	}
}

func TestListIncludesSupersededFactForQueueComparison(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.listed = []store.Memory{{
		ID: "candidate", Type: store.TypeSemantic, Content: "User lives in Berlin.",
		Status: store.StatusPending, Supersedes: "old", CreatedAt: time.Now(),
	}}
	fm.memories["old"] = store.Memory{ID: "old", Content: "User lives in Amsterdam."}
	req := httptest.NewRequest(http.MethodGet, "/v1/memories", nil)
	rec := httptest.NewRecorder()
	manageAPI(fm).handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var out struct {
		Memories []memoryJSON `json:"memories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Memories) != 1 || out.Memories[0].Supersedes == nil ||
		out.Memories[0].Supersedes.ID != "old" || out.Memories[0].Supersedes.Content != "User lives in Amsterdam." {
		t.Fatalf("memories = %+v, want the prior fact attached", out.Memories)
	}
}

func TestAddStoresUserExplicit(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"Remember I prefer dark mode.","type":"semantic","trusted":true}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(fm.inserted) != 1 {
		t.Fatalf("inserted = %d", len(fm.inserted))
	}
	m := fm.inserted[0]
	if m.Actor != store.ActorUser || m.Type != store.TypeSemantic || len(m.Embedding) == 0 || m.RequireReview {
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

func TestAddWithoutTrustedSignalRequiresReview(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"The user lives in Porto."}`))
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

func TestAddTrustedCredentialFactRequiresReview(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"The staging API token is stored in the vault.","trusted":true}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(fm.inserted) != 1 || !fm.inserted[0].RequireReview {
		t.Fatalf("inserted = %+v, want credential memory held for review", fm.inserted)
	}
	if !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("result = %s, want pending status", rec.Body)
	}
}

// D-011: a clean, trusted add of the user's own standing instruction
// activates; only credential phrasing holds it.
func TestAddTrustedDirectiveFactActivates(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		"Remember I prefer dark mode.",
		"The user always wants weekly reports to be emailed.",
	} {
		t.Run(content, func(t *testing.T) {
			t.Parallel()
			fm := newFakeManager()
			req := httptest.NewRequest(http.MethodPost, "/v1/memories",
				strings.NewReader(`{"content":"`+content+`","trusted":true}`))
			rec := httptest.NewRecorder()
			manageAPI(fm).handleAdd(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body %s", rec.Code, rec.Body)
			}
			if len(fm.inserted) != 1 || fm.inserted[0].RequireReview {
				t.Fatalf("inserted = %+v, want active memory", fm.inserted)
			}
			if !strings.Contains(rec.Body.String(), `"status":"active"`) {
				t.Fatalf("result = %s, want active status", rec.Body)
			}
		})
	}
}

func TestAddRejectedNearDuplicateFromUntrustedSourceIsDroppedAndLogged(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "rejected-1", 0.95, store.StatusRejected, true
	fm.memories["rejected-1"] = store.Memory{ID: "rejected-1", Content: "User lives in Porto.", Status: store.StatusRejected}
	var log bytes.Buffer
	a := manageAPI(fm)
	a.log = slog.New(slog.NewTextHandler(&log, nil))
	content := "User lives in Porto."
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"`+content+`","trusted":false}`))
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

func TestAddTrustedRestatementOfRejectedFactInsertsActive(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "rejected-1", 0.95, store.StatusRejected, true
	fm.memories["rejected-1"] = store.Memory{ID: "rejected-1", Content: "User lives in Porto.", Status: store.StatusRejected}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto.","trusted":true}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(fm.inserted) != 1 || fm.inserted[0].RequireReview {
		t.Fatalf("inserted = %+v, want one active memory", fm.inserted)
	}
	if !strings.Contains(rec.Body.String(), `"status":"active"`) {
		t.Fatalf("result = %s, want active status", rec.Body)
	}
}

func TestAddTrustedCredentialRestatementOfRejectedFactIsDropped(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "rejected-1", 0.95, store.StatusRejected, true
	fm.memories["rejected-1"] = store.Memory{ID: "rejected-1", Content: "User lives in Porto.", Status: store.StatusRejected}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"The staging password is hunter2.","trusted":true}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK || len(fm.inserted) != 0 {
		t.Fatalf("status=%d inserted=%d body=%s, want dropped", rec.Code, len(fm.inserted), rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"status":"dropped"`) {
		t.Fatalf("result=%s, want dropped status", rec.Body)
	}
}

func TestAddPendingNearDuplicateReusesReviewItem(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "pending-1", 0.96, store.StatusPending, true
	fm.memories["pending-1"] = store.Memory{ID: "pending-1", Content: "User lives in Porto.", Status: store.StatusPending}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto.","trusted":false}`))
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

func TestAddCleanNearDuplicatePromotesPendingMemory(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "pending-1", 0.96, store.StatusPending, true
	fm.memories["pending-1"] = store.Memory{ID: "pending-1", Content: "User lives in Porto.", Status: store.StatusPending}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto.","trusted":true}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK || len(fm.inserted) != 0 {
		t.Fatalf("status=%d inserted=%d body=%s", rec.Code, len(fm.inserted), rec.Body)
	}
	if len(fm.promoted) != 1 || fm.promoted[0] != "pending-1" {
		t.Fatalf("promoted = %v, want [pending-1]", fm.promoted)
	}
	if !strings.Contains(rec.Body.String(), `"status":"active"`) {
		t.Fatalf("result = %s, want active status", rec.Body)
	}
}

func TestAddActiveNearDuplicateConfirmsExistingMemory(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "active-1", 0.96, store.StatusActive, true
	fm.memories["active-1"] = store.Memory{ID: "active-1", Content: "User lives in Porto.", Status: store.StatusActive}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto.","trusted":true}`))
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
	fm.memories["active-1"] = store.Memory{ID: "active-1", Content: "User lives in Porto.", Status: store.StatusActive}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto.","trusted":false}`))
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
		strings.NewReader(`{"content":"User lives in Porto.","trusted":true}`))
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
				strings.NewReader(`{"content":"User lives in Porto.","trusted":true}`))
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
				strings.NewReader(`{"content":"User lives in Porto.","trusted":true}`))
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
	fm.memories["active-1"] = store.Memory{ID: "active-1", Content: "User lives in Porto.", Status: store.StatusActive}
	fm.confirmErr = errors.New("confirmation unavailable")
	var log bytes.Buffer
	a := manageAPI(fm)
	a.log = slog.New(slog.NewTextHandler(&log, nil))
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Porto.","trusted":true}`))
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
	fm.memories["m1"] = store.Memory{ID: "m1", Status: store.StatusPending}
	rec := resolve(t, fm, "m1", `{"action":"confirm"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(fm.promoted) != 1 || fm.promoted[0] != "m1" {
		t.Fatalf("promoted = %v", fm.promoted)
	}
}

func TestResolveSupersedingConfirmationUsesAtomicTransition(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.memories["candidate"] = store.Memory{ID: "candidate", Status: store.StatusPending, Supersedes: "old"}
	rec := resolve(t, fm, "candidate", `{"action":"confirm"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(fm.confirmedSuperseding) != 1 || fm.confirmedSuperseding[0] != "candidate" || len(fm.promoted) != 0 {
		t.Fatalf("atomic confirms = %v, ordinary promotes = %v", fm.confirmedSuperseding, fm.promoted)
	}
}

func TestResolveEditedSupersedingCorrectionUsesAtomicTransition(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.memories["candidate"] = store.Memory{
		ID: "candidate", Type: store.TypeSemantic, Content: "User lives in Berlin.",
		Status: store.StatusPending, Supersedes: "old", CreatedAt: time.Now(),
	}
	rec := resolve(t, fm, "candidate", `{"action":"confirm","content":"User lives in Lisbon."}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if len(fm.correctedSuperseding) != 1 || fm.correctedSuperseding[0].Content != "User lives in Lisbon." ||
		fm.correctedSuperseding[0].Actor != store.ActorUser || len(fm.inserted) != 0 {
		t.Fatalf("corrections = %+v inserted = %+v", fm.correctedSuperseding, fm.inserted)
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

// A trusted, clean correction of an active fact supersedes it at once:
// inserted pending, then confirmed through the supersede transaction.
func TestAddTrustedCorrectionSupersedesActiveFact(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "active-1", 0.96, store.StatusActive, true
	fm.memories["active-1"] = store.Memory{ID: "active-1", Content: "User lives in Amsterdam.", Status: store.StatusActive}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Berlin.","trusted":true}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK || len(fm.inserted) != 1 {
		t.Fatalf("status=%d inserted=%d body=%s", rec.Code, len(fm.inserted), rec.Body)
	}
	if fm.inserted[0].Supersedes != "active-1" || !fm.inserted[0].RequireReview {
		t.Fatalf("inserted = %+v, want pending row superseding active-1", fm.inserted[0])
	}
	if len(fm.confirmedSuperseding) != 1 || len(fm.confirmed) != 0 {
		t.Fatalf("confirmedSuperseding=%v confirmed=%v, want one supersede and no reinforce", fm.confirmedSuperseding, fm.confirmed)
	}
	if !strings.Contains(rec.Body.String(), `"status":"active"`) {
		t.Fatalf("result = %s, want active status", rec.Body)
	}
}

func TestAddUntrustedCorrectionQueuesSupersede(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "active-1", 0.96, store.StatusActive, true
	fm.memories["active-1"] = store.Memory{ID: "active-1", Content: "User lives in Amsterdam.", Status: store.StatusActive}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Berlin.","trusted":false}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK || len(fm.inserted) != 1 || fm.inserted[0].Supersedes != "active-1" {
		t.Fatalf("status=%d inserted=%+v body=%s", rec.Code, fm.inserted, rec.Body)
	}
	if len(fm.confirmedSuperseding) != 0 || !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("confirmedSuperseding=%v body=%s, want pending for review", fm.confirmedSuperseding, rec.Body)
	}
}

// Promoting a pending correction from the add path supersedes rather
// than leaving two active facts.
func TestAddTrustedMatchOfPendingCorrectionSupersedes(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.nearestID, fm.nearestSim, fm.nearestStatus, fm.nearestFound = "pending-1", 0.96, store.StatusPending, true
	fm.memories["pending-1"] = store.Memory{ID: "pending-1", Content: "User lives in Berlin.", Status: store.StatusPending, Supersedes: "active-1"}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"content":"User lives in Berlin.","trusted":true}`))
	rec := httptest.NewRecorder()
	manageAPI(fm).handleAdd(rec, req)
	if rec.Code != http.StatusOK || len(fm.promoted) != 0 || len(fm.confirmedSuperseding) != 1 {
		t.Fatalf("status=%d promoted=%v confirmedSuperseding=%v", rec.Code, fm.promoted, fm.confirmedSuperseding)
	}
}

// Review point: the queue resolves every supersede comparison in one
// store call, not one Get per row.
func TestListLoadsSupersededContentsInOneCall(t *testing.T) {
	t.Parallel()
	fm := newFakeManager()
	fm.memories["old-1"] = store.Memory{ID: "old-1", Content: "User lives in Amsterdam."}
	fm.memories["old-2"] = store.Memory{ID: "old-2", Content: "User has 2 cats."}
	fm.listed = []store.Memory{
		{ID: "p1", Content: "User lives in Berlin.", Status: store.StatusPending, Supersedes: "old-1"},
		{ID: "p2", Content: "User has 3 cats.", Status: store.StatusPending, Supersedes: "old-2"},
		{ID: "p3", Content: "User likes tea.", Status: store.StatusPending},
	}
	rec := httptest.NewRecorder()
	manageAPI(fm).handleList(rec, httptest.NewRequest(http.MethodGet, "/v1/memories", nil))
	if rec.Code != http.StatusOK || fm.contentsCalls != 1 {
		t.Fatalf("status=%d contentsCalls=%d", rec.Code, fm.contentsCalls)
	}
	for _, want := range []string{`"content":"User lives in Amsterdam."`, `"content":"User has 2 cats."`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("list body missing %s: %s", want, rec.Body)
		}
	}
}
