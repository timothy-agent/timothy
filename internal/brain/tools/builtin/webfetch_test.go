package builtin

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBlockedIP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ip      string
		blocked bool
	}{
		{ip: "127.0.0.1", blocked: true},
		{ip: "::1", blocked: true},
		{ip: "10.1.2.3", blocked: true},
		{ip: "172.16.0.1", blocked: true},
		{ip: "192.168.1.1", blocked: true},
		{ip: "169.254.169.254", blocked: true}, // cloud metadata
		{ip: "100.64.0.1", blocked: true},      // CGNAT
		{ip: "0.0.0.0", blocked: true},
		{ip: "224.0.0.1", blocked: true},
		{ip: "fd00::1", blocked: true}, // IPv6 ULA
		{ip: "fe80::1", blocked: true}, // IPv6 link-local
		{ip: "8.8.8.8", blocked: false},
		{ip: "1.1.1.1", blocked: false},
		{ip: "2606:4700:4700::1111", blocked: false},
	}
	for _, tc := range tests {
		t.Run(tc.ip, func(t *testing.T) {
			t.Parallel()
			reason := blockedIP(net.ParseIP(tc.ip))
			if (reason != "") != tc.blocked {
				t.Fatalf("blockedIP(%s) = %q, want blocked=%v", tc.ip, reason, tc.blocked)
			}
		})
	}
}

func TestWebFetchRefusesLocalAddresses(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret internal page"))
	}))
	defer srv.Close()

	tool := WebFetch()
	args, _ := json.Marshal(map[string]string{"url": srv.URL})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("fetch of %s: err = %v, want blocked address", srv.URL, err)
	}
}

func TestWebFetchRejectsBadSchemes(t *testing.T) {
	t.Parallel()
	tool := WebFetch()
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

	got, err := fetchReadable(context.Background(), srv.Client(), srv.URL)
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

	if _, err := fetchReadable(context.Background(), srv.Client(), srv.URL+"/bin"); err == nil ||
		!strings.Contains(err.Error(), "unsupported content type") {
		t.Fatalf("binary fetch err = %v", err)
	}
	if _, err := fetchReadable(context.Background(), srv.Client(), srv.URL+"/missing"); err == nil ||
		!strings.Contains(err.Error(), "http 404") {
		t.Fatalf("404 fetch err = %v", err)
	}
}
