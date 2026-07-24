package markitdown

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConvert(t *testing.T) {
	t.Run("posts bytes with filename and mimetype headers", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/convert" {
				t.Errorf("path = %q, want /convert", r.URL.Path)
			}
			if got := r.Header.Get("X-Filename"); got != "doc.pdf" {
				t.Errorf("X-Filename = %q", got)
			}
			if got := r.Header.Get("X-Mimetype"); got != "application/pdf" {
				t.Errorf("X-Mimetype = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"markdown": "# converted"}`))
		}))
		defer srv.Close()

		out, err := Convert(context.Background(), srv.Client(), srv.URL, "doc.pdf", "application/pdf", []byte("%PDF-1.7"))
		if err != nil {
			t.Fatalf("Convert: %v", err)
		}
		if out != "# converted" {
			t.Fatalf("out = %q", out)
		}
	})

	t.Run("unconfigured base URL errors without a request", func(t *testing.T) {
		if _, err := Convert(context.Background(), nil, "", "a.html", "text/html", []byte("x")); err == nil {
			t.Fatal("empty baseURL accepted")
		}
	})

	t.Run("non-2xx surfaces status and body snippet", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "conversion failed: broken file", http.StatusUnprocessableEntity)
		}))
		defer srv.Close()

		_, err := Convert(context.Background(), srv.Client(), srv.URL, "a.html", "text/html", []byte("x"))
		if err == nil || !strings.Contains(err.Error(), "422") {
			t.Fatalf("err = %v, want http 422 surfaced", err)
		}
	})
}
