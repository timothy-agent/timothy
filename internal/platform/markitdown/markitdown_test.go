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

func TestPDFImages(t *testing.T) {
	t.Run("posts raw bytes and decodes per-page results", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/pdf/images" {
				t.Errorf("path = %q, want /pdf/images", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pages":[
				{"page":1,"text_chars":2000,"images":[{"index":7,"media_type":"image/png","data_b64":"AAAA","width":200,"height":100}],"render_b64":null},
				{"page":2,"text_chars":10,"images":[],"render_b64":"QkJC"}
			]}`))
		}))
		defer srv.Close()

		out, err := PDFImages(context.Background(), srv.Client(), srv.URL, []byte("%PDF-1.7"))
		if err != nil {
			t.Fatalf("PDFImages: %v", err)
		}
		if len(out.Pages) != 2 {
			t.Fatalf("pages = %d, want 2", len(out.Pages))
		}
		p1 := out.Pages[0]
		if p1.TextChars != 2000 || len(p1.Images) != 1 || p1.RenderB64 != nil {
			t.Fatalf("page 1 = %+v, want text-rich page with one image and no render", p1)
		}
		if p1.Images[0].MediaType != "image/png" || p1.Images[0].Width != 200 {
			t.Fatalf("page 1 image = %+v", p1.Images[0])
		}
		p2 := out.Pages[1]
		if p2.RenderB64 == nil || *p2.RenderB64 != "QkJC" {
			t.Fatalf("page 2 = %+v, want a rendered page (scanned)", p2)
		}
	})

	t.Run("unconfigured base URL errors without a request", func(t *testing.T) {
		if _, err := PDFImages(context.Background(), nil, "", []byte("x")); err == nil {
			t.Fatal("empty baseURL accepted")
		}
	})

	t.Run("non-2xx surfaces status and body snippet", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "pdf open failed: not a pdf", http.StatusUnprocessableEntity)
		}))
		defer srv.Close()

		_, err := PDFImages(context.Background(), srv.Client(), srv.URL, []byte("x"))
		if err == nil || !strings.Contains(err.Error(), "422") {
			t.Fatalf("err = %v, want http 422 surfaced", err)
		}
	})
}

func TestTruncateMarkdown(t *testing.T) {
	t.Run("under cap is unchanged", func(t *testing.T) {
		md := "# short document"
		if got := TruncateMarkdown(md); got != md {
			t.Fatalf("TruncateMarkdown = %q, want unchanged", got)
		}
	})

	t.Run("over cap is cut and marked", func(t *testing.T) {
		md := strings.Repeat("a", maxMarkdownBytes+100)
		got := TruncateMarkdown(md)
		if len(got) <= maxMarkdownBytes || !strings.HasSuffix(got, truncatedMarker) {
			t.Fatalf("TruncateMarkdown did not cut and mark an over-cap document (len=%d)", len(got))
		}
		if !strings.HasPrefix(got, strings.Repeat("a", maxMarkdownBytes)) {
			t.Fatal("TruncateMarkdown did not preserve the first maxMarkdownBytes")
		}
	})
}
