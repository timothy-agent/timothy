//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
)

// TestKBSystemCollectionIsReadOnly: every admin write against a system
// collection is 403, reads still work, and the classifier never sees it.
func TestKBSystemCollectionIsReadOnly(t *testing.T) {
	store := testKBStore(t)
	const name = "itest-system-docs"
	raw, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = raw.Exec(ctx, `DELETE FROM kb_system_bundles WHERE name = $1`, name)
		raw.Close()
	})

	ids, err := store.ReplaceSystemDocuments(t.Context(), name, "bundled", []kb.SystemDocument{
		{Title: "Tools", SourceRef: "https://example.com/tools", Markdown: "# Tools\nbody", Meta: map[string]string{"app_path": "/settings/tools"}},
	})
	if err != nil {
		t.Fatalf("ReplaceSystemDocuments: %v", err)
	}
	if err := store.SetSystemBundle(t.Context(), name, "v1.2.3", "hash"); err != nil {
		t.Fatal(err)
	}
	collID := ""
	cs, err := store.ListCollections(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == name {
			collID = c.ID
		}
	}
	docID := ids[0]

	var seen []kb.Collection
	classify := func(_ context.Context, _, _ string, collections []kb.Collection) chat.CollectionChoice {
		seen = collections
		return chat.CollectionChoice{NewName: "itest-auto", NewDesc: "auto"}
	}
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerKB(m.Handle, store, &fakeIngester{}, "", classify, nil, nil)
	do := func(method, path, contentType string, body *strings.Reader) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, body)
		req.Header.Set("Authorization", "Bearer tok")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w
	}

	uploadBody, uploadType := multipartFile(t, "file", "notes.md", []byte("# notes"))
	writes := []struct {
		name, method, path, contentType string
		body                            *strings.Reader
	}{
		{"rename", "PATCH", "/v1/admin/kb/collections/" + collID, "application/json", strings.NewReader(`{"name":"itest-renamed"}`)},
		{"delete collection", "DELETE", "/v1/admin/kb/collections/" + collID, "", strings.NewReader("")},
		{"upload", "POST", "/v1/admin/kb/collections/" + collID + "/documents", uploadType, strings.NewReader(uploadBody.String())},
		{"url", "POST", "/v1/admin/kb/collections/" + collID + "/documents/url", "application/json", strings.NewReader(`{"url":"https://example.com/a"}`)},
		{"clip", "POST", "/v1/admin/kb/documents/clip", "application/json",
			strings.NewReader(`{"url":"https://example.com/c","title":"c","markdown":"# c","collection_id":"` + collID + `"}`)},
		{"delete document", "DELETE", "/v1/admin/kb/documents/" + docID, "", strings.NewReader("")},
		{"reingest", "POST", "/v1/admin/kb/documents/" + docID + "/reingest", "", strings.NewReader("")},
	}
	for _, tt := range writes {
		t.Run(tt.name, func(t *testing.T) {
			w := do(tt.method, tt.path, tt.contentType, tt.body)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "read-only") {
				t.Fatalf("status = %d body %s, want 403 read-only", w.Code, w.Body)
			}
		})
	}

	w := do("GET", "/v1/admin/kb/collections/"+collID, "", strings.NewReader(""))
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d", w.Code)
	}
	var got kb.Collection
	if err := decodeBody(t, w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.System || got.BundleVersion != "v1.2.3" || got.Name != name || got.DocCount != 1 {
		t.Fatalf("collection = %+v", got)
	}
	if w := do("GET", "/v1/admin/kb/collections/"+collID+"/documents", "", strings.NewReader("")); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"source_type":"selfdocs"`) {
		t.Fatalf("list documents = %d %s", w.Code, w.Body)
	}

	autoBody, autoType := multipartFile(t, "file", "auto.md", []byte("# auto"))
	if w := do("POST", "/v1/admin/kb/documents", autoType, strings.NewReader(autoBody.String())); w.Code != http.StatusCreated {
		t.Fatalf("auto upload = %d %s", w.Code, w.Body)
	}
	for _, c := range seen {
		if c.System {
			t.Fatalf("classifier saw system collection %q", c.Name)
		}
	}
}
