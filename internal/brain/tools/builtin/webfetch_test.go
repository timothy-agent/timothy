package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWebFetchRefusesLocalAddresses(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret internal page"))
	}))
	defer srv.Close()

	tool := WebFetch(WebFetchConfig{})
	args, _ := json.Marshal(map[string]string{"url": srv.URL})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("fetch of %s: err = %v, want blocked address", srv.URL, err)
	}
}

func TestWebFetchRejectsBadSchemes(t *testing.T) {
	t.Parallel()
	tool := WebFetch(WebFetchConfig{})
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com", "gopher://x"} {
		args, _ := json.Marshal(map[string]string{"url": u})
		_, err := tool.Execute(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "unsupported scheme") {
			t.Fatalf("fetch %s: err = %v, want unsupported scheme", u, err)
		}
	}
}

func TestExtractHTMLText(t *testing.T) {
	t.Parallel()
	page := `<!doctype html><html><head><title>Pricing Page</title>
<style>body { color: red }</style><script>alert(1)</script></head>
<body><h1>Plans</h1><p>Basic costs <b>$5</b>.</p>
<script>tracker()</script><noscript>enable js</noscript></body></html>`

	got := extractHTMLText([]byte(page))
	for _, want := range []string{"Pricing Page", "Plans", "Basic costs", "$5"} {
		if !strings.Contains(got, want) {
			t.Fatalf("extract missing %q in %q", want, got)
		}
	}
	for _, banned := range []string{"alert(1)", "color: red", "tracker()", "enable js"} {
		if strings.Contains(got, banned) {
			t.Fatalf("extract leaked %q", banned)
		}
	}
	// Title leads.
	if !strings.HasPrefix(got, "Pricing Page") {
		t.Fatalf("title does not lead: %q", got)
	}
}

func TestFetchReadableTruncatesLongText(t *testing.T) {
	t.Parallel()
	// Exercise the truncation path without the network: call the
	// extractor + cap logic through a local response served over a
	// custom client whose transport skips the IP guard.
	long := strings.Repeat("word ", (webFetchMaxResult/5)+2000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(long))
	}))
	defer srv.Close()

	got, err := fetchReadable(context.Background(), srv.Client(), "", srv.URL)
	if err != nil {
		t.Fatalf("fetchReadable: %v", err)
	}
	if !strings.Contains(got, "[truncated") {
		t.Fatal("long response missing truncation note")
	}
	if len(got) > webFetchMaxResult+100 {
		t.Fatalf("result length %d exceeds cap", len(got))
	}
}

func TestFetchReadableStripsUserinfo(t *testing.T) {
	t.Parallel()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Inject credentials into the URL; they must not become a header.
	u, _ := url.Parse(srv.URL)
	authed := u.Scheme + "://user:secret@" + u.Host + "/"
	if _, err := fetchReadable(context.Background(), srv.Client(), "", authed); err != nil {
		t.Fatalf("fetchReadable: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization header leaked: %q", gotAuth)
	}
}

func TestFetchReadableRejectsNonTextAndErrors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bin":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0x1, 0x2})
		case "/missing":
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if _, err := fetchReadable(context.Background(), srv.Client(), "", srv.URL+"/bin"); err == nil ||
		!strings.Contains(err.Error(), "unsupported content type") {
		t.Fatalf("binary fetch err = %v", err)
	}
	if _, err := fetchReadable(context.Background(), srv.Client(), "", srv.URL+"/missing"); err == nil ||
		!strings.Contains(err.Error(), "http 404") {
		t.Fatalf("404 fetch err = %v", err)
	}
}

// fakeMarkitdown returns a sidecar stub whose /convert response is
// controlled by the handler.
func fakeMarkitdown(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchReadableHTMLPrefersMarkitdown(t *testing.T) {
	t.Parallel()
	md := fakeMarkitdown(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Mimetype"); got != "text/html" {
			t.Errorf("X-Mimetype = %q", got)
		}
		_, _ = w.Write([]byte(`{"markdown": "# Plans\n\n| tier | price |\n|---|---|\n| basic | $5 |"}`))
	})
	page := fakeMarkitdown(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Plans</h1><table><tr><td>basic</td><td>$5</td></tr></table></body></html>`))
	})

	got, err := fetchReadable(context.Background(), page.Client(), md.URL, page.URL)
	if err != nil {
		t.Fatalf("fetchReadable: %v", err)
	}
	if !strings.Contains(got, "| tier | price |") {
		t.Fatalf("got %q, want markitdown table structure", got)
	}
}

func TestFetchReadableHTMLFallsBackWhenSidecarFails(t *testing.T) {
	t.Parallel()
	md := fakeMarkitdown(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "conversion failed", http.StatusUnprocessableEntity)
	})
	page := fakeMarkitdown(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>Pricing</title></head><body><p>Basic costs $5.</p></body></html>`))
	})

	got, err := fetchReadable(context.Background(), page.Client(), md.URL, page.URL)
	if err != nil {
		t.Fatalf("fetchReadable: %v", err)
	}
	if !strings.Contains(got, "Basic costs") {
		t.Fatalf("got %q, want DOM-extractor fallback content", got)
	}
}

func TestFetchReadablePDF(t *testing.T) {
	t.Parallel()
	md := fakeMarkitdown(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Mimetype"); got != "application/pdf" {
			t.Errorf("X-Mimetype = %q", got)
		}
		_, _ = w.Write([]byte(`{"markdown": "# Q3 Report\n\nRevenue grew."}`))
	})
	pdf := fakeMarkitdown(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.7 fake"))
	})

	got, err := fetchReadable(context.Background(), pdf.Client(), md.URL, pdf.URL)
	if err != nil {
		t.Fatalf("fetchReadable: %v", err)
	}
	if !strings.Contains(got, "Q3 Report") {
		t.Fatalf("got %q, want converted PDF markdown", got)
	}

	// Without the sidecar, PDFs stay unsupported with a clear reason.
	if _, err := fetchReadable(context.Background(), pdf.Client(), "", pdf.URL); err == nil ||
		!strings.Contains(err.Error(), "markitdown sidecar") {
		t.Fatalf("unconfigured pdf fetch err = %v", err)
	}

	// A sidecar failure on PDF is an error, not silent garbage.
	broken := fakeMarkitdown(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "conversion failed", http.StatusUnprocessableEntity)
	})
	if _, err := fetchReadable(context.Background(), pdf.Client(), broken.URL, pdf.URL); err == nil ||
		!strings.Contains(err.Error(), "pdf conversion failed") {
		t.Fatalf("broken sidecar pdf fetch err = %v", err)
	}
}
