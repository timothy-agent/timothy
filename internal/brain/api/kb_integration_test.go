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
	// The pool must outlive t.Context(), which is already done when
	// t.Cleanup fires: a t.Context()-scoped pool silently no-ops the
	// cleanup delete below, leaking itest-% collections that collide
	// across tests sharing fixedClassifier's name. A plain Background
	// pool leaks connections instead (pgpool has no Close), so use a
	// cancelable context released by the LAST cleanup: t.Cleanup runs
	// LIFO, so registering the cancel before the delete keeps the pool
	// alive for the delete and closes it right after.
	poolCtx, poolCancel := context.WithCancel(context.Background())
	t.Cleanup(poolCancel)
	pool := pgpool.New(poolCtx, dsn, log)
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
		if _, err := db.Exec(cctx, `DELETE FROM kb_collections WHERE name LIKE 'itest-%'`); err != nil {
			t.Logf("cleanup kb collections: %v", err)
		}
	})
	return kb.New(pool)
}

// fakeIngester never calls memoryd; tests only exercise brain's own
// CRUD/upload surface, not the memoryd round trip (covered separately
// in internal/memory/api). Ingestion runs on brain's own background
// goroutine (kb.go's startIngest), so calls needs its own lock: the
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

// fixedClassifier always proposes the same new collection: these
// integration tests exercise brain's own CRUD/upload surface, not the
// classify prompt itself (covered in internal/brain/chat).
func fixedClassifier(ctx context.Context, docTitle, docText string, collections []kb.Collection) chat.CollectionChoice {
	return chat.CollectionChoice{NewName: "itest-auto", NewDesc: "auto-classified in a test"}
}

func TestKBCollectionsCRUD(t *testing.T) {
	store := testKBStore(t)
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier, nil, nil)

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

	// Default retrieval_weight (D-085, issue #443) is neutral (1.0).
	def, err := store.GetCollection(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetCollection after create: %v", err)
	}
	if def.RetrievalWeight != 1.0 {
		t.Fatalf("default RetrievalWeight = %v, want 1.0", def.RetrievalWeight)
	}

	// An out-of-bounds retrieval_weight is rejected at create time too.
	badCreate := httptest.NewRequest("POST", "/v1/admin/kb/collections", strings.NewReader(`{"name":"itest-badweight","retrieval_weight":0}`))
	badCreate.Header.Set("Authorization", "Bearer tok")
	w = httptest.NewRecorder()
	m.ServeHTTP(w, badCreate)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("create with bad weight status = %d, want 400", w.Code)
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
	for _, body := range []string{`{}`, `{"name":"  "}`, `{"retrieval_weight":0}`, `{"retrieval_weight":2.5}`} {
		bad := httptest.NewRequest("PATCH", "/v1/admin/kb/collections/"+created.ID, strings.NewReader(body))
		bad.Header.Set("Authorization", "Bearer tok")
		w = httptest.NewRecorder()
		m.ServeHTTP(w, bad)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("patch %s status = %d, want 400", body, w.Code)
		}
	}

	// A valid retrieval_weight (D-085, issue #443) persists.
	weightPatch := httptest.NewRequest("PATCH", "/v1/admin/kb/collections/"+created.ID, strings.NewReader(`{"retrieval_weight":0.3}`))
	weightPatch.Header.Set("Authorization", "Bearer tok")
	w = httptest.NewRecorder()
	m.ServeHTTP(w, weightPatch)
	if w.Code != http.StatusOK {
		t.Fatalf("weight patch status = %d body %s", w.Code, w.Body)
	}
	weighted, err := store.GetCollection(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetCollection after weight patch: %v", err)
	}
	if weighted.RetrievalWeight != 0.3 {
		t.Fatalf("RetrievalWeight = %v, want 0.3", weighted.RetrievalWeight)
	}

	// A failed document counts toward failed_count but not the base
	// doc/chunk story: GetCollection picks it up via collectionColumns.
	docID, err := store.CreateDocument(context.Background(), created.ID, "Doc", "file", "doc.md", "curated", "content", 7)
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
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier, nil, nil)

	collID, err := store.CreateCollection(context.Background(), "itest-upload", "", 0)
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
	// after a real ReplaceChunks: this fake ingester only stands in for
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
	if final.Provenance != "curated" {
		t.Fatalf("provenance = %q, want curated (D-080: operator upload)", final.Provenance)
	}
}

// TestKBDocumentUploadCaptionsImageLinkWhenEnabled confirms issue
// #349's ingest-funnel hook: startIngest runs the Enricher between
// SetIngesting and the memoryd call, persisting the captioned markdown
// via kb.Store.UpdateMarkdown before the fake ingester ever sees it.
func TestKBDocumentUploadCaptionsImageLinkWhenEnabled(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{}
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	}))
	defer imgSrv.Close()
	enrich := &kb.Enricher{
		Fetch:   imgSrv.Client(),
		Caption: func(context.Context, string, []byte) string { return "a test chart" },
		Enabled: func(context.Context) bool { return true },
		Log:     discard(),
	}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier, nil, enrich)

	collID, err := store.CreateCollection(context.Background(), "itest-caption", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	md := "# Report\n![a chart](" + imgSrv.URL + "/a.png)\n"
	body, contentType := multipartFile(t, "file", "report.md", []byte(md))
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

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ingester.callCount() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := ingester.callCount(); got != 1 {
		t.Fatalf("ingester calls = %d, want 1", got)
	}
	final, err := store.GetDocument(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if !strings.Contains(final.Markdown, "a test chart") {
		t.Fatalf("stored markdown = %q, want a caption inserted", final.Markdown)
	}
	if !strings.Contains(final.Markdown, imgSrv.URL+"/a.png") {
		t.Fatalf("stored markdown = %q, want the original image link kept", final.Markdown)
	}
}

// TestKBSearchDocumentsCrossCollectionTitleMatch covers GET
// /v1/admin/kb/documents?q=: a case-insensitive title match across
// EVERY collection (the composer #-mention "find a kb document"
// search), narrowing an unrelated third document out, and stripping
// markdown from the response the same way listDocuments does.
func TestKBSearchDocumentsCrossCollectionTitleMatch(t *testing.T) {
	store := testKBStore(t)
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier, nil, nil)

	collA, err := store.CreateCollection(context.Background(), "itest-search-a", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	collB, err := store.CreateCollection(context.Background(), "itest-search-b", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if _, err := store.CreateDocument(context.Background(), collA, "Runbook QUERYTAG", "file", "a.md", "curated", "content a", 9); err != nil {
		t.Fatalf("CreateDocument a: %v", err)
	}
	if _, err := store.CreateDocument(context.Background(), collB, "querytag onboarding", "file", "b.md", "curated", "content b", 9); err != nil {
		t.Fatalf("CreateDocument b: %v", err)
	}
	if _, err := store.CreateDocument(context.Background(), collB, "unrelated notes", "file", "c.md", "curated", "content c", 9); err != nil {
		t.Fatalf("CreateDocument c: %v", err)
	}

	req := httptest.NewRequest("GET", "/v1/admin/kb/documents?q=querytag", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d body %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "content a") {
		t.Fatal("response must not include markdown")
	}

	var resp struct {
		Documents []struct {
			Title string `json:"title"`
		} `json:"documents"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, d := range resp.Documents {
		titles = append(titles, d.Title)
	}
	if !strings.Contains(strings.Join(titles, "|"), "Runbook QUERYTAG") || !strings.Contains(strings.Join(titles, "|"), "querytag onboarding") {
		t.Fatalf("titles = %v, want both querytag documents across collections", titles)
	}
	if strings.Contains(strings.Join(titles, "|"), "unrelated notes") {
		t.Fatalf("titles = %v, want the unrelated document excluded", titles)
	}
}

func TestKBDocumentUploadStripsNULAndInvalidUTF8(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier, nil, nil)

	collID, err := store.CreateCollection(context.Background(), "itest-nul", "", 0)
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
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier, nil, nil)

	collID, err := store.CreateCollection(context.Background(), "itest-upload-bad", "", 0)
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
	// transport refuses: swap in an unguarded one for this test.
	saved := kbFetchTransport
	kbFetchTransport = http.DefaultTransport
	defer func() { kbFetchTransport = saved }()

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier, nil, nil)

	collID, err := store.CreateCollection(context.Background(), "itest-url", "", 0)
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
	if final.Provenance != "curated" {
		t.Fatalf("provenance = %q, want curated (D-080: operator-fetched URL, distinct from a Pounce clip)", final.Provenance)
	}
}

// TestKBDocumentFromURLScopedReAddRefreshesInPlace covers the scoped
// route's dedup: re-adding the same URL updates the existing row (200,
// same id, no second row) instead of duplicating it.
func TestKBDocumentFromURLScopedReAddRefreshesInPlace(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{}

	body := "# Fetched\nversion one"
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer page.Close()

	saved := kbFetchTransport
	kbFetchTransport = http.DefaultTransport
	defer func() { kbFetchTransport = saved }()

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier, nil, nil)

	collID, err := store.CreateCollection(context.Background(), "itest-url-readd", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/admin/kb/collections/"+collID+"/documents/url",
			strings.NewReader(`{"url":"`+page.URL+`/page","title":""}`))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w
	}

	first := post()
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d body %s", first.Code, first.Body)
	}
	var firstDoc struct {
		ID string `json:"id"`
	}
	if err := decodeBody(t, first.Body.Bytes(), &firstDoc); err != nil {
		t.Fatal(err)
	}

	body = "# Fetched\nversion two"
	second := post()
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200 body %s", second.Code, second.Body)
	}
	var secondDoc struct {
		ID string `json:"id"`
	}
	if err := decodeBody(t, second.Body.Bytes(), &secondDoc); err != nil {
		t.Fatal(err)
	}
	if secondDoc.ID != firstDoc.ID {
		t.Fatalf("second add created a new document %q, want the same id %q", secondDoc.ID, firstDoc.ID)
	}

	rows, err := store.ListDocuments(context.Background(), collID)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("found %d documents, want exactly 1 (no duplicate)", len(rows))
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ingester.callCount() >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	final, err := store.GetDocument(context.Background(), firstDoc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if final.Markdown != "# Fetched\nversion two" {
		t.Fatalf("stored markdown = %q, want the refreshed body", final.Markdown)
	}
}

// TestKBDocumentFromURLAutoReAddSkipsClassifierKeepsCollection covers
// the unscoped route's dedup: re-adding the same URL must not
// re-consult the classifier and must keep the document in its current
// collection.
func TestKBDocumentFromURLAutoReAddSkipsClassifierKeepsCollection(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{}

	body := "# Fetched\nversion one"
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer page.Close()

	saved := kbFetchTransport
	kbFetchTransport = http.DefaultTransport
	defer func() { kbFetchTransport = saved }()

	classifyCalls := 0
	classify := func(ctx context.Context, docTitle, docText string, collections []kb.Collection) chat.CollectionChoice {
		classifyCalls++
		return chat.CollectionChoice{NewName: "itest-url-auto-readd"}
	}

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", classify, nil, nil)

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/admin/kb/documents/url",
			strings.NewReader(`{"url":"`+page.URL+`/auto-page","title":""}`))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w
	}

	first := post()
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d body %s", first.Code, first.Body)
	}
	var firstDoc struct {
		ID           string `json:"id"`
		CollectionID string `json:"collection_id"`
	}
	if err := decodeBody(t, first.Body.Bytes(), &firstDoc); err != nil {
		t.Fatal(err)
	}
	if classifyCalls != 1 {
		t.Fatalf("classifyCalls after first add = %d, want 1", classifyCalls)
	}

	body = "# Fetched\nversion two"
	second := post()
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200 body %s", second.Code, second.Body)
	}
	var secondDoc struct {
		ID           string `json:"id"`
		CollectionID string `json:"collection_id"`
	}
	if err := decodeBody(t, second.Body.Bytes(), &secondDoc); err != nil {
		t.Fatal(err)
	}
	if secondDoc.ID != firstDoc.ID {
		t.Fatalf("second add created a new document %q, want the same id %q", secondDoc.ID, firstDoc.ID)
	}
	if secondDoc.CollectionID != firstDoc.CollectionID {
		t.Fatalf("collection_id = %q, want unchanged %q", secondDoc.CollectionID, firstDoc.CollectionID)
	}
	if classifyCalls != 1 {
		t.Fatalf("classifyCalls after re-add = %d, want still 1 (not re-consulted)", classifyCalls)
	}
}

// TestKBDocumentFromURLDifferentURLStillCreates confirms dedup keys on
// the normalized URL, not the collection or title: a distinct URL
// always creates a second document.
func TestKBDocumentFromURLDifferentURLStillCreates(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{}

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte("# Fetched\nbody"))
	}))
	defer page.Close()

	saved := kbFetchTransport
	kbFetchTransport = http.DefaultTransport
	defer func() { kbFetchTransport = saved }()

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier, nil, nil)

	collID, err := store.CreateCollection(context.Background(), "itest-url-distinct", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	post := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/admin/kb/collections/"+collID+"/documents/url",
			strings.NewReader(`{"url":"`+page.URL+path+`","title":""}`))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w
	}

	first := post("/page-a")
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d body %s", first.Code, first.Body)
	}
	second := post("/page-b")
	if second.Code != http.StatusCreated {
		t.Fatalf("second status = %d, want 201 body %s", second.Code, second.Body)
	}
	var firstDoc, secondDoc struct {
		ID string `json:"id"`
	}
	if err := decodeBody(t, first.Body.Bytes(), &firstDoc); err != nil {
		t.Fatal(err)
	}
	if err := decodeBody(t, second.Body.Bytes(), &secondDoc); err != nil {
		t.Fatal(err)
	}
	if firstDoc.ID == secondDoc.ID {
		t.Fatal("distinct URLs must not collapse into the same document")
	}

	rows, err := store.ListDocuments(context.Background(), collID)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("found %d documents, want 2", len(rows))
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
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier, nil, nil)

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

	existingID, err := store.CreateCollection(context.Background(), "itest-existing", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	matchExisting := func(ctx context.Context, docTitle, docText string, collections []kb.Collection) chat.CollectionChoice {
		return chat.CollectionChoice{ExistingID: existingID}
	}

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", matchExisting, nil, nil)

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

	collID, err := store.CreateCollection(ctx, "itest-sweep", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	pendingID, err := store.CreateDocument(ctx, collID, "Stuck pending", "file", "a.md", "curated", "content", 7)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	ingestingID, err := store.CreateDocument(ctx, collID, "Stuck ingesting", "file", "b.md", "curated", "content", 7)
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

// clipRequest builds a valid clip JSON body, letting each test override
// individual fields via the returned map before marshaling.
func clipRequest(overrides map[string]any) string {
	body := map[string]any{
		"url":         "https://example.com/article",
		"title":       "Article title",
		"markdown":    "# Article title\n\nsome content",
	}
	for k, v := range overrides {
		if v == nil {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func postClip(t *testing.T, m *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/admin/kb/documents/clip", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	return w
}

func TestKBClipValidation(t *testing.T) {
	store := testKBStore(t)
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier, nil, nil)

	tests := []struct {
		name       string
		overrides  map[string]any
		wantStatus int
		wantCode   string
	}{
		{"missing url", map[string]any{"url": nil}, http.StatusBadRequest, "bad_request"},
		{"invalid url scheme", map[string]any{"url": "ftp://example.com/x"}, http.StatusBadRequest, "bad_request"},
		{"empty markdown", map[string]any{"markdown": "  "}, http.StatusBadRequest, "bad_request"},
		{"oversize markdown", map[string]any{"markdown": strings.Repeat("a", (128<<10)+1)}, http.StatusRequestEntityTooLarge, "too_large"},
		{"unknown collection_id", map[string]any{"collection_id": "00000000-0000-0000-0000-000000000000"}, http.StatusNotFound, "not_found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := postClip(t, m, clipRequest(tc.overrides))
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d body %s", w.Code, tc.wantStatus, w.Body)
			}
			var resp struct {
				Error string `json:"error"`
			}
			if err := decodeBody(t, w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Error != tc.wantCode {
				t.Fatalf("error code = %q, want %q", resp.Error, tc.wantCode)
			}
		})
	}
}

// TestKBClipNewDocumentAutoClassifiesAndNormalizesURL exercises the
// happy path with no collection_id: the classifier is consulted, the
// document lands with source_type clip, and the stored source_ref has
// tracking params stripped but real params kept.
func TestKBClipNewDocumentAutoClassifiesAndNormalizesURL(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{}
	classified := false
	classify := func(ctx context.Context, docTitle, docText string, collections []kb.Collection) chat.CollectionChoice {
		classified = true
		return chat.CollectionChoice{NewName: "itest-clip-auto", NewDesc: "auto"}
	}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", classify, nil, nil)

	body := clipRequest(map[string]any{"url": "https://example.com/article?utm_source=x&x=1#frag"})
	w := postClip(t, m, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 body %s", w.Code, w.Body)
	}
	if !classified {
		t.Fatal("classifier was not consulted for a new clip with no collection_id")
	}
	if strings.Contains(w.Body.String(), "some content") {
		t.Fatal("response must not include markdown")
	}

	var doc struct {
		ID           string `json:"id"`
		CollectionID string `json:"collection_id"`
		SourceType   string `json:"source_type"`
		SourceRef    string `json:"source_ref"`
		Provenance   string `json:"provenance"`
		Status       string `json:"status"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SourceType != "clip" {
		t.Fatalf("source_type = %q, want clip", doc.SourceType)
	}
	if doc.SourceRef != "https://example.com/article?x=1" {
		t.Fatalf("source_ref = %q, want tracking params stripped and fragment dropped", doc.SourceRef)
	}
	if doc.Provenance != "web" {
		t.Fatalf("provenance = %q, want web (D-080: browser-extension clip)", doc.Provenance)
	}
	coll, err := store.GetCollection(context.Background(), doc.CollectionID)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if coll.Name != "itest-clip-auto" {
		t.Fatalf("collection name = %q, want the classifier's proposed itest-clip-auto", coll.Name)
	}
}

func TestKBClipNewDocumentWithCollectionSkipsClassifier(t *testing.T) {
	store := testKBStore(t)
	classified := false
	classify := func(ctx context.Context, docTitle, docText string, collections []kb.Collection) chat.CollectionChoice {
		classified = true
		return chat.CollectionChoice{NewName: "should-not-be-created"}
	}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", classify, nil, nil)

	collID, err := store.CreateCollection(context.Background(), "itest-clip-target", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	w := postClip(t, m, clipRequest(map[string]any{"collection_id": collID}))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 body %s", w.Code, w.Body)
	}
	if classified {
		t.Fatal("classifier must not be consulted when collection_id is given")
	}

	var doc struct {
		CollectionID string `json:"collection_id"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.CollectionID != collID {
		t.Fatalf("collection_id = %q, want %q", doc.CollectionID, collID)
	}
}

// TestKBClipReClipRefreshesInPlace covers the dedup path: posting the
// same normalized URL twice must update the existing row, not create a
// second one, and must not re-consult the classifier.
func TestKBClipReClipRefreshesInPlace(t *testing.T) {
	store := testKBStore(t)
	classifyCalls := 0
	classify := func(ctx context.Context, docTitle, docText string, collections []kb.Collection) chat.CollectionChoice {
		classifyCalls++
		return chat.CollectionChoice{NewName: "itest-clip-reclip"}
	}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", classify, nil, nil)

	first := postClip(t, m, clipRequest(map[string]any{"url": "https://example.com/reclip?utm_source=a#x"}))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first clip status = %d body %s", first.Code, first.Body)
	}
	var firstDoc struct {
		ID           string `json:"id"`
		CollectionID string `json:"collection_id"`
	}
	if err := decodeBody(t, first.Body.Bytes(), &firstDoc); err != nil {
		t.Fatal(err)
	}
	if classifyCalls != 1 {
		t.Fatalf("classifyCalls after first clip = %d, want 1", classifyCalls)
	}

	second := postClip(t, m, clipRequest(map[string]any{
		"url":      "https://example.com/reclip?utm_source=b#y",
		"title":    "Updated title",
		"markdown": "# Updated title\n\nnew content",
	}))
	if second.Code != http.StatusAccepted {
		t.Fatalf("second clip status = %d body %s", second.Code, second.Body)
	}
	var secondDoc struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		CollectionID string `json:"collection_id"`
		Status       string `json:"status"`
	}
	if err := decodeBody(t, second.Body.Bytes(), &secondDoc); err != nil {
		t.Fatal(err)
	}
	if secondDoc.ID != firstDoc.ID {
		t.Fatalf("second clip created a new document %q, want the same id %q", secondDoc.ID, firstDoc.ID)
	}
	if secondDoc.Title != "Updated title" {
		t.Fatalf("title = %q, want updated", secondDoc.Title)
	}
	if secondDoc.Status != "pending" {
		t.Fatalf("status = %q, want pending", secondDoc.Status)
	}
	if secondDoc.CollectionID != firstDoc.CollectionID {
		t.Fatalf("collection_id changed to %q without collection_id in the request, want unchanged %q", secondDoc.CollectionID, firstDoc.CollectionID)
	}
	if classifyCalls != 1 {
		t.Fatalf("classifyCalls after re-clip = %d, want still 1 (not re-consulted)", classifyCalls)
	}

	final, err := store.GetDocument(context.Background(), firstDoc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if final.Markdown != "# Updated title\n\nnew content" {
		t.Fatalf("stored markdown = %q, want updated", final.Markdown)
	}
	if final.Bytes != int64(len(final.Markdown)) {
		t.Fatalf("bytes = %d, want %d", final.Bytes, len(final.Markdown))
	}

	rows, err := store.ListDocuments(context.Background(), firstDoc.CollectionID)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	count := 0
	for _, d := range rows {
		if d.ID == firstDoc.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("found %d rows for the re-clipped document, want exactly 1 (no duplicate)", count)
	}
}

func TestKBClipReClipMovesCollection(t *testing.T) {
	store := testKBStore(t)
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier, nil, nil)

	origID, err := store.CreateCollection(context.Background(), "itest-clip-orig", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	newID, err := store.CreateCollection(context.Background(), "itest-clip-new", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	first := postClip(t, m, clipRequest(map[string]any{"url": "https://example.com/move", "collection_id": origID}))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first clip status = %d body %s", first.Code, first.Body)
	}

	second := postClip(t, m, clipRequest(map[string]any{"url": "https://example.com/move", "collection_id": newID}))
	if second.Code != http.StatusAccepted {
		t.Fatalf("second clip status = %d body %s", second.Code, second.Body)
	}
	var doc struct {
		CollectionID string `json:"collection_id"`
	}
	if err := decodeBody(t, second.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.CollectionID != newID {
		t.Fatalf("collection_id = %q, want moved to %q", doc.CollectionID, newID)
	}
}

func TestKBClipStripsNULBytes(t *testing.T) {
	store := testKBStore(t)
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier, nil, nil)

	w := postClip(t, m, clipRequest(map[string]any{"markdown": "clean\x00 text \xff here"}))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", w.Code, w.Body)
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

func TestKBClipEmptyTitleUsesGeneratedTitle(t *testing.T) {
	store := testKBStore(t)
	titler := func(ctx context.Context, input string) string { return "Generated Title" }
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier, titler, nil)

	w := postClip(t, m, clipRequest(map[string]any{"title": ""}))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 body %s", w.Code, w.Body)
	}
	var doc struct {
		Title string `json:"title"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Generated Title" {
		t.Fatalf("title = %q, want the titler's Generated Title", doc.Title)
	}
}

func TestKBClipEmptyTitleFallsBackToURLWhenTitlerEmpty(t *testing.T) {
	store := testKBStore(t)
	titler := func(ctx context.Context, input string) string { return "" }
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier, titler, nil)

	body := clipRequest(map[string]any{"title": "", "url": "https://example.com/docs/getting-started.html"})
	w := postClip(t, m, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 body %s", w.Code, w.Body)
	}
	var doc struct {
		Title string `json:"title"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Title != "getting-started" {
		t.Fatalf("title = %q, want titleFromURL's derivation getting-started", doc.Title)
	}
}

func TestKBClipExplicitTitleSkipsTitler(t *testing.T) {
	store := testKBStore(t)
	titler := func(ctx context.Context, input string) string {
		t.Fatal("titler must not be consulted when title is non-empty")
		return ""
	}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier, titler, nil)

	w := postClip(t, m, clipRequest(map[string]any{"title": "Explicit Title"}))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 body %s", w.Code, w.Body)
	}
	var doc struct {
		Title string `json:"title"`
	}
	if err := decodeBody(t, w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Explicit Title" {
		t.Fatalf("title = %q, want Explicit Title", doc.Title)
	}
}

func TestKBDocumentFromURLRejectsBadURL(t *testing.T) {
	store := testKBStore(t)
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier, nil, nil)

	collID, err := store.CreateCollection(context.Background(), "itest-url-bad", "", 0)
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
	a.registerKB(m.Handle, store, &fakeIngester{}, "", fixedClassifier, nil, nil)

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret internal page"))
	}))
	defer page.Close()

	collID, err := store.CreateCollection(context.Background(), "itest-url-ssrf", "", 0)
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
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier, nil, nil)

	collID, err := store.CreateCollection(context.Background(), "itest-reingest", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	docID, err := store.CreateDocument(context.Background(), collID, "Doc", "file", "doc.md", "curated", "content", 7)
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

// waitForDocument polls store.GetDocument until check reports true or
// the deadline elapses: startIngest runs its own goroutine (kb.go), so
// tests observe its effect asynchronously.
func waitForDocument(t *testing.T, store *kb.Store, docID string, check func(kb.Document) bool) kb.Document {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last kb.Document
	for time.Now().Before(deadline) {
		doc, err := store.GetDocument(context.Background(), docID)
		if err != nil {
			t.Fatalf("GetDocument: %v", err)
		}
		last = doc
		if check(doc) {
			return doc
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}

// TestKBDocumentReingestRetryableErrorSchedulesRetry pins issue #414:
// a reingest failing on a retryable error (chain_exhausted here)
// schedules an automatic retry instead of leaving the document
// permanently failed, bumping retry_count and setting next_retry_at.
func TestKBDocumentReingestRetryableErrorSchedulesRetry(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{err: errors.New("every provider failed: chain_exhausted")}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier, nil, nil)

	collID, err := store.CreateCollection(context.Background(), "itest-retry-schedule", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	docID, err := store.CreateDocument(context.Background(), collID, "Doc", "file", "doc.md", "curated", "content", 7)
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

	final := waitForDocument(t, store, docID, func(d kb.Document) bool { return d.RetryCount > 0 })
	if final.Status != "failed" {
		t.Fatalf("status = %q, want failed", final.Status)
	}
	if final.RetryCount != 1 {
		t.Fatalf("retry_count = %d, want 1", final.RetryCount)
	}
	if final.NextRetryAt == nil || !final.NextRetryAt.After(time.Now()) {
		t.Fatalf("next_retry_at = %v, want set in the future", final.NextRetryAt)
	}
	if !strings.Contains(final.Error, "chain_exhausted") {
		t.Fatalf("error = %q, want the ingester's error preserved", final.Error)
	}
}

// TestKBDocumentReingestPermanentErrorNeverSchedulesRetry pins issue
// #414's other half: an error with no retryable marker (an unsupported
// format here) leaves the document failed with next_retry_at unset, so
// the retry sweep never picks it back up.
func TestKBDocumentReingestPermanentErrorNeverSchedulesRetry(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{err: errors.New("document produced no chunks")}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier, nil, nil)

	collID, err := store.CreateCollection(context.Background(), "itest-retry-permanent", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	docID, err := store.CreateDocument(context.Background(), collID, "Doc", "file", "doc.md", "curated", "content", 7)
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

	final := waitForDocument(t, store, docID, func(d kb.Document) bool { return d.Status == "failed" })
	if final.RetryCount != 0 {
		t.Fatalf("retry_count = %d, want 0 (permanent error never schedules)", final.RetryCount)
	}
	if final.NextRetryAt != nil {
		t.Fatalf("next_retry_at = %v, want unset for a permanent error", final.NextRetryAt)
	}
}

// TestRunKBRetrySweepRetriesUntilBudgetExhausted pins the sweep's
// bounded-attempts contract (issue #414): a document stuck on a
// retryable error gets picked up and retried by RunKBRetrySweep across
// several ticks, and once retry_count reaches kbRetryMaxAttempts the
// next failure leaves it permanently failed (no next_retry_at), still
// manually reingestable.
func TestRunKBRetrySweepRetriesUntilBudgetExhausted(t *testing.T) {
	store := testKBStore(t)
	ingester := &fakeIngester{err: errors.New("gwclient: gateway http 503: service unavailable")}

	collID, err := store.CreateCollection(context.Background(), "itest-retry-sweep", "", 0)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	docID, err := store.CreateDocument(context.Background(), collID, "Doc", "file", "doc.md", "curated", "content", 7)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	// Force every retry due immediately: this test exercises the sweep's
	// selection/exhaustion logic, not the real backoff wait.
	forceDue := func() {
		conn, err := pgx.Connect(context.Background(), os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer func() { _ = conn.Close(context.Background()) }()
		if _, err := conn.Exec(context.Background(),
			`UPDATE kb_documents SET next_retry_at = now() - interval '1 second' WHERE id = $1 AND next_retry_at IS NOT NULL`,
			docID); err != nil {
			t.Fatalf("force due: %v", err)
		}
	}

	// First failure via reingest seeds retry_count=1 with a future
	// next_retry_at; the sweep must not pick it up until it's due.
	h := &kbAPI{store: store, ingest: ingester, log: discard()}
	h.startIngest(docID, "Doc")
	waitForDocument(t, store, docID, func(d kb.Document) bool { return d.RetryCount == 1 })

	ctx := context.Background()

	// Drive the sweep through every remaining attempt: kbRetryMaxAttempts
	// total scheduled retries, then one more failure exhausts the budget.
	for want := 2; want <= kbRetryMaxAttempts; want++ {
		forceDue()
		sweepKBRetries(ctx, store, ingester, nil, discard())
		waitForDocument(t, store, docID, func(d kb.Document) bool { return d.RetryCount >= want })
	}
	forceDue()
	sweepKBRetries(ctx, store, ingester, nil, discard())
	final := waitForDocument(t, store, docID, func(d kb.Document) bool { return d.NextRetryAt == nil })

	if final.Status != "failed" {
		t.Fatalf("status = %q, want failed", final.Status)
	}
	if final.RetryCount != kbRetryMaxAttempts {
		t.Fatalf("retry_count = %d, want %d (exhausted, no further bump)", final.RetryCount, kbRetryMaxAttempts)
	}
	if final.NextRetryAt != nil {
		t.Fatalf("next_retry_at = %v, want unset once the retry budget is exhausted", final.NextRetryAt)
	}
	if !strings.Contains(final.Error, "503") {
		t.Fatalf("error = %q, want the ingester's last error preserved", final.Error)
	}

	// Exhausted but still manually reingestable (AC): reingest must not
	// be blocked by the spent automatic-retry budget.
	req := httptest.NewRequest("POST", "/v1/admin/kb/documents/"+docID+"/reingest", nil)
	req.Header.Set("Authorization", "Bearer tok")
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, ingester, "", fixedClassifier, nil, nil)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("manual reingest after exhaustion status = %d body %s", w.Code, w.Body)
	}
}

// TestSweepKBRetriesNoopIsQuiet pins the "no failed documents, no log
// noise" acceptance criterion: an empty DueForRetry result must not
// log anything.
func TestSweepKBRetriesNoopIsQuiet(t *testing.T) {
	store := testKBStore(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	sweepKBRetries(context.Background(), store, &fakeIngester{}, nil, log)

	if buf.Len() != 0 {
		t.Fatalf("sweep logged with no failed documents: %s", buf.String())
	}
}
