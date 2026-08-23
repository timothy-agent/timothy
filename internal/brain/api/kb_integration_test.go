//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

// multipartFile builds a single-field multipart body for upload tests.
func multipartFile(t *testing.T, field, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

func decodeBody(t *testing.T, raw []byte, v any) error {
	t.Helper()
	return json.Unmarshal(raw, v)
}

// testKBStore mirrors testMissionStore (missions_integration_test.go):
// a real Postgres-backed *kb.Store, migrated, with itest-prefixed
// fixtures swept on cleanup.
func testKBStore(t *testing.T) *kb.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = db.Exec(cctx, `DELETE FROM kb_collections WHERE name LIKE 'itest-%'`)
	})
	return kb.New(pool)
}

// fakeIngester never calls memoryd; tests only exercise brain's own
// CRUD/upload surface, not the memoryd round trip (covered separately
// in internal/memory/api). Ingestion runs on brain's own background
// goroutine (kb.go's startIngest), so calls needs its own lock — the
// test polls it from the main goroutine while the fake is invoked from
// that background one.
type fakeIngester struct {
	err error

	mu    sync.Mutex
	calls int
}

func (f *fakeIngester) IngestDocument(context.Context, string, string, string) (int, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return 3, f.err
}

func (f *fakeIngester) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fixedClassifier always proposes the same new collection — these
// integration tests exercise brain's own CRUD/upload surface, not the
// classify prompt itself (covered in internal/brain/chat).
func fixedClassifier(ctx context.Context, docTitle, docText string, collections []kb.Collection) chat.CollectionChoice {
	return chat.CollectionChoice{NewName: "itest-auto", NewDesc: "auto-classified in a test"}
}

func TestKBCollectionsCRUD(t *testing.T) {
	store := testKBStore(t)
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier)

	create := httptest.NewRequest("POST", "/v1/admin/kb/collections", strings.NewReader(`{"name":"itest-docs","description":"test collection"}`))
	create.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, create)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body %s", w.Code, w.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	list := httptest.NewRequest("GET", "/v1/admin/kb/collections", nil)
	list.Header.Set("Authorization", "Bearer tok")
	w = httptest.NewRecorder()
	m.ServeHTTP(w, list)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "itest-docs") {
		t.Fatalf("list status = %d body %s", w.Code, w.Body)
	}

	// PATCH renames without touching the description; empty-name and
	// empty-body PATCHes are rejected.
	patch := httptest.NewRequest("PATCH", "/v1/admin/kb/collections/"+created.ID, strings.NewReader(`{"name":"itest-docs-renamed"}`))
	patch.Header.Set("Authorization", "Bearer tok")
	w = httptest.NewRecorder()
	m.ServeHTTP(w, patch)
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d body %s", w.Code, w.Body)
	}
	renamed, err := store.GetCollection(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetCollection after rename: %v", err)
	}
	if renamed.Name != "itest-docs-renamed" || renamed.Description != "test collection" {
		t.Fatalf("after rename: name=%q description=%q", renamed.Name, renamed.Description)
	}
	for _, body := range []string{`{}`, `{"name":"  "}`} {
		bad := httptest.NewRequest("PATCH", "/v1/admin/kb/collections/"+created.ID, strings.NewReader(body))
		bad.Header.Set("Authorization", "Bearer tok")
		w = httptest.NewRecorder()
		m.ServeHTTP(w, bad)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("patch %s status = %d, want 400", body, w.Code)
		}
	}

	// A failed document counts toward failed_count but not the base
	// doc/chunk story — GetCollection picks it up via collectionColumns.
	docID, err := store.CreateDocument(context.Background(), created.ID, "Doc", "file", "doc.md", "content", 7)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	if err := store.SetFailed(context.Background(), docID, "boom"); err != nil {
		t.Fatalf("SetFailed: %v", err)
	}
	got, err := store.GetCollection(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if got.FailedCount != 1 {
		t.Fatalf("FailedCount = %d, want 1", got.FailedCount)
	}

	del := httptest.NewRequest("DELETE", "/v1/admin/kb/collections/"+created.ID, nil)
	del.Header.Set("Authorization", "Bearer tok")
	w = httptest.NewRecorder()
	m.ServeHTTP(w, del)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body %s", w.Code, w.Body)
	}

	if _, err := store.GetCollection(context.Background(), created.ID); err == nil {
		t.Fatal("expected the collection to be gone after delete")
	}
}

func TestKBDocumentUploadSkipsMarkitdownForMarkdown(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier)

	collID, err := store.CreateCollection(context.Background(), "itest-upload", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	body, contentType := multipartFile(t, "file", "notes.md", []byte("# Title\nsome content"))
	req := httptest.NewRequest("POST", "/v1/admin/kb/collections/"+collID+"/documents", body)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "some content") {
		t.Fatal("response must not include markdown")
	}

	var doc struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Status != "pending" {
		t.Fatalf("status = %q, want pending (background goroutine flips it later)", doc.Status)
	}

	// The real terminal flip (ingesting -> ready/failed) is memoryd's
	// own report via SetIngested/SetFailed (internal/memory/store/kb.go)
	// after a real ReplaceChunks — this fake ingester only stands in for
	// the memclient round trip, so the document parks at "ingesting"
	// here; poll for that and confirm the fake was actually reached with
	// the stored markdown.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ingester.callCount() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	final, err := store.GetDocument(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if final.Status != "ingesting" {
		t.Fatalf("status = %q, want ingesting", final.Status)
	}
	if got := ingester.callCount(); got != 1 {
		t.Fatalf("ingester calls = %d, want 1", got)
	}
	if final.Markdown != "# Title\nsome content" {
		t.Fatalf("stored markdown = %q", final.Markdown)
	}
}

func TestKBDocumentUploadStripsNULAndInvalidUTF8(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier)

	collID, err := store.CreateCollection(context.Background(), "itest-nul", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	// Real markitdown output has produced NUL bytes from PDFs; Postgres
	// text columns reject 0x00 and invalid UTF-8 outright.
	body, contentType := multipartFile(t, "file", "dirty.txt", []byte("clean\x00 text \xff here"))
	req := httptest.NewRequest("POST", "/v1/admin/kb/collections/"+collID+"/documents", body)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body %s", w.Code, w.Body)
	}

	var doc struct {
		ID string `json:"id"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	final, err := store.GetDocument(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if final.Markdown != "clean text � here" {
		t.Fatalf("stored markdown = %q, want NUL stripped and invalid UTF-8 replaced", final.Markdown)
	}
}

func TestKBDocumentUploadRejectsUnsupportedType(t *testing.T) {
	store := testKBStore(t)
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier)

	collID, err := store.CreateCollection(context.Background(), "itest-upload-bad", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	body, contentType := multipartFile(t, "file", "archive.zip", []byte("PK\x03\x04"))
	req := httptest.NewRequest("POST", "/v1/admin/kb/collections/"+collID+"/documents", body)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestKBDocumentFromURLIngestsFetchedMarkdown(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{}

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte("# Fetched\nbody from the web"))
	}))
	defer page.Close()

	// httptest listens on loopback, which the production netguard
	// transport refuses — swap in an unguarded one for this test.
	saved := kbFetchTransport
	kbFetchTransport = http.DefaultTransport
	defer func() { kbFetchTransport = saved }()

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier)

	collID, err := store.CreateCollection(context.Background(), "itest-url", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/admin/kb/collections/"+collID+"/documents/url",
		strings.NewReader(`{"url":"`+page.URL+`/guides/rag-primer.md","title":""}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "body from the web") {
		t.Fatal("response must not include markdown")
	}

	var doc struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		SourceRef string `json:"source_ref"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Title != "rag-primer" {
		t.Fatalf("title = %q, want derived rag-primer", doc.Title)
	}
	if doc.SourceRef != page.URL+"/guides/rag-primer.md" {
		t.Fatalf("source_ref = %q, want the fetched URL", doc.SourceRef)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ingester.callCount() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	final, err := store.GetDocument(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if final.Markdown != "# Fetched\nbody from the web" {
		t.Fatalf("stored markdown = %q", final.Markdown)
	}
	if got := ingester.callCount(); got != 1 {
		t.Fatalf("ingester calls = %d, want 1", got)
	}
}

// TestKBDocumentUploadAutoCreatesNewCollection exercises the unscoped
// upload route: no collection is chosen up front, so the classifier's
// proposed new collection (fixedClassifier) must be created and the
// document filed into it.
func TestKBDocumentUploadAutoCreatesNewCollection(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier)

	body, contentType := multipartFile(t, "file", "notes.md", []byte("# Title\nsome content"))
	req := httptest.NewRequest("POST", "/v1/admin/kb/documents", body)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body %s", w.Code, w.Body)
	}

	var doc struct {
		ID           string `json:"id"`
		CollectionID string `json:"collection_id"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	coll, err := store.GetCollection(context.Background(), doc.CollectionID)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if coll.Name != "itest-auto" {
		t.Fatalf("collection name = %q, want the classifier's proposed itest-auto", coll.Name)
	}
}

// TestKBDocumentFromURLAutoUsesExistingCollection exercises the
// unscoped URL route with a classifier that matches an existing
// collection instead of proposing a new one.
func TestKBDocumentFromURLAutoUsesExistingCollection(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{}

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte("# Fetched\nbody from the web"))
	}))
	defer page.Close()

	saved := kbFetchTransport
	kbFetchTransport = http.DefaultTransport
	defer func() { kbFetchTransport = saved }()

	existingID, err := store.CreateCollection(context.Background(), "itest-existing", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	matchExisting := func(ctx context.Context, docTitle, docText string, collections []kb.Collection) chat.CollectionChoice {
		return chat.CollectionChoice{ExistingID: existingID}
	}

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", matchExisting)

	req := httptest.NewRequest("POST", "/v1/admin/kb/documents/url",
		strings.NewReader(`{"url":"`+page.URL+`/notes.md","title":""}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", w.Code, w.Body)
	}

	var doc struct {
		CollectionID string `json:"collection_id"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.CollectionID != existingID {
		t.Fatalf("collection_id = %q, want the classifier's matched %q", doc.CollectionID, existingID)
	}
}

func TestKBSweepStaleFailsStuckDocuments(t *testing.T) {
	store := testKBStore(t)
	ctx := context.Background()

	collID, err := store.CreateCollection(ctx, "itest-sweep", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	pendingID, err := store.CreateDocument(ctx, collID, "Stuck pending", "file", "a.md", "content", 7)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	ingestingID, err := store.CreateDocument(ctx, collID, "Stuck ingesting", "file", "b.md", "content", 7)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	if err := store.SetIngesting(ctx, ingestingID); err != nil {
		t.Fatalf("SetIngesting: %v", err)
	}

	// Fresh rows sit inside the sweep's grace window (a boot sweep must
	// not eat an upload that just started); nothing is swept yet.
	n, err := store.SweepStale(ctx)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if n != 0 {
		t.Fatalf("swept %d fresh documents, want 0 (grace window)", n)
	}

	// Backdate both past the grace window, as a restart-stranded row
	// would be.
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx,
		`UPDATE kb_documents SET updated_at = now() - interval '5 minutes' WHERE id = ANY($1)`,
		[]string{pendingID, ingestingID}); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	n, err = store.SweepStale(ctx)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if n != 2 {
		t.Fatalf("swept %d documents, want the 2 stuck ones", n)
	}
	for _, id := range []string{pendingID, ingestingID} {
		doc, err := store.GetDocument(ctx, id)
		if err != nil {
			t.Fatalf("GetDocument: %v", err)
		}
		if doc.Status != "failed" || !strings.Contains(doc.Error, "interrupted") {
			t.Fatalf("doc %s: status=%q error=%q, want failed/interrupted", id, doc.Status, doc.Error)
		}
	}
}

func TestKBDocumentFromURLRejectsBadURL(t *testing.T) {
	store := testKBStore(t)
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier)

	collID, err := store.CreateCollection(context.Background(), "itest-url-bad", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	for _, raw := range []string{`{"url":"ftp://example.com/x"}`, `{"url":"not a url"}`, `{"url":""}`} {
		req := httptest.NewRequest("POST", "/v1/admin/kb/collections/"+collID+"/documents/url", strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", raw, w.Code)
		}
	}
}

func TestKBDocumentFromURLBlocksLocalAddresses(t *testing.T) {
	store := testKBStore(t)
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	// Real guarded transport: the loopback httptest server must be
	// refused at dial time.
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier)

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret internal page"))
	}))
	defer page.Close()

	collID, err := store.CreateCollection(context.Background(), "itest-url-ssrf", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/admin/kb/collections/"+collID+"/documents/url",
		strings.NewReader(`{"url":"`+page.URL+`"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "blocked address") {
		t.Fatalf("status = %d body %s, want 502 blocked address", w.Code, w.Body)
	}
}

func TestKBDocumentReingestFailureSetsFailedStatus(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{err: errors.New("memoryd unreachable")}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier)

	collID, err := store.CreateCollection(context.Background(), "itest-reingest", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	docID, err := store.CreateDocument(context.Background(), collID, "Doc", "file", "doc.md", "content", 7)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/admin/kb/documents/"+docID+"/reingest", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("reingest status = %d body %s", w.Code, w.Body)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := store.GetDocument(context.Background(), docID)
		if err != nil {
			t.Fatalf("GetDocument: %v", err)
		}
		if got.Status == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	final, err := store.GetDocument(context.Background(), docID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if final.Status != "failed" || !strings.Contains(final.Error, "memoryd unreachable") {
		t.Fatalf("document = %+v, want failed with the ingester's error", final)
	}
}
