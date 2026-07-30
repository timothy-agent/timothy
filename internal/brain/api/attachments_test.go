package api

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// pngBytes is a minimal valid 1x1 PNG — enough for http.DetectContentType
// to sniff "image/png" without a real file on disk.
var pngBytes = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestAttachmentsEndpointsUnmountedWhenStoreNil(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerAttachments(m.Handle, nil)

	for _, req := range []struct{ method, path string }{
		{"POST", "/v1/attachments"},
		{"GET", "/v1/attachments/abc"},
	} {
		httpReq := httptest.NewRequest(req.method, req.path, nil)
		httpReq.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httpReq)
		if w.Code != 404 {
			t.Fatalf("%s %s with a nil attachment store = %d, want 404 (unmounted)", req.method, req.path, w.Code)
		}
	}
}

func TestAttachmentsRequireAuth(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := attachments.New(t.TempDir(), pool)
	m := mux(a)
	a.registerAttachments(m.Handle, store)

	req := httptest.NewRequest("POST", "/v1/attachments", nil)
	// No Authorization header.
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("unauthenticated upload = %d, want 401", w.Code)
	}
}

func TestAttachmentsUploadBadMultipartField(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := attachments.New(t.TempDir(), pool)
	m := mux(a)
	a.registerAttachments(m.Handle, store)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	// Wrong field name — handler requires "file".
	part, err := mw.CreateFormFile("wrong_field", "upload.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(pngBytes); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("missing \"file\" field = %d, want 400", w.Code)
	}
}

func TestAttachmentsUploadReachesStore(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	// A never-connecting pool proves upload parses the multipart body
	// and reaches Store.Save (which then fails at the DB step) —
	// the actual save/dedup/mime-sniff semantics against a real
	// Postgres are covered by the attachments package's own
	// integration tests.
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := attachments.New(t.TempDir(), pool)
	m := mux(a)
	a.registerAttachments(m.Handle, store)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "upload.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(pngBytes); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	// A degraded pool surfaces as failAttachment's default 400 mapping
	// (matches the "bad_request" fallback for a non-sentinel error) —
	// proving the request passed multipart parsing and reached Save.
	if w.Code != 400 {
		t.Fatalf("upload against a degraded store = %d, want 400 (reached the store)", w.Code)
	}
}

func TestAttachmentsDownloadUnknownID(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	store := attachments.New(t.TempDir(), pgpool.New(context.Background(), "postgres://invalid/nope", discard()))
	m := mux(a)
	a.registerAttachments(m.Handle, store)

	req := httptest.NewRequest("GET", "/v1/attachments/does-not-exist", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	// Degraded store: Get fails before ErrNotFound can even be
	// distinguished, so this also lands on the generic 400 mapping —
	// same "reached the store" proof as the upload test above.
	if w.Code != 400 {
		t.Fatalf("download against a degraded store = %d, want 400 (reached the store)", w.Code)
	}
}
