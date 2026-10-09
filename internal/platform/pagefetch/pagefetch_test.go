package pagefetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const accept = "text/html, */*;q=0.1"

var article = "<html><head><title>T</title></head><body><article>" +
	strings.Repeat("Real article text that a reader came for. ", 30) +
	"</article></body></html>"

const spaShell = `<html><head><script src="/app.js"></script></head><body><div id="root"></div>` +
	`<noscript>You need to enable JavaScript to run this app.</noscript></body></html>`

func htmlPage(body string) *Page {
	return &Page{Status: 200, ContentType: "text/html; charset=utf-8", Body: []byte(body)}
}

func TestIsShell(t *testing.T) {
	tests := []struct {
		name string
		page *Page
		want bool
	}{
		{"spa root div", htmlPage(spaShell), true},
		{"empty body", htmlPage("<html><body></body></html>"), true},
		{"script text not counted", htmlPage("<html><body><script>" + strings.Repeat("var a=1;", 200) + "</script></body></html>"), true},
		{"article", htmlPage(article), false},
		{"short page asking for javascript", htmlPage("<html><body><p>" + strings.Repeat("word ", 150) +
			"</p><p>Please enable JavaScript to continue.</p></body></html>"), true},
		{"long article with a javascript notice", htmlPage("<html><body><p>" + strings.Repeat("word ", 500) +
			"</p><noscript>Please enable JavaScript to view the comments.</noscript></body></html>"), false},
		{"plain text never a shell", &Page{Status: 200, ContentType: "text/plain", Body: []byte("short")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isShell(tt.page); got != tt.want {
				t.Fatalf("isShell = %v, want %v", got, tt.want)
			}
		})
	}
}

// server answers each request with handler(userAgent) and counts calls.
func server(t *testing.T, calls *atomic.Int32, handler func(ua string) (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		status, body := handler(r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func isCrawler(ua string) bool { return strings.Contains(ua, "Googlebot") }

func TestFetchTiers(t *testing.T) {
	tests := []struct {
		name      string
		handler   func(ua string) (int, string)
		wantCalls int32
		wantCode  int
		wantBody  string
	}{
		{
			name:      "article served to the browser needs one request",
			handler:   func(string) (int, string) { return 200, article },
			wantCalls: 1, wantCode: 200, wantBody: "Real article text",
		},
		{
			name: "host refusing non-browser clients is read by the browser tier",
			handler: func(ua string) (int, string) {
				if !strings.HasPrefix(ua, "Mozilla/5.0 (Windows") {
					return 403, "blocked"
				}
				return 200, article
			},
			wantCalls: 1, wantCode: 200, wantBody: "Real article text",
		},
		{
			name: "403 to the browser falls back to the crawler",
			handler: func(ua string) (int, string) {
				if !isCrawler(ua) {
					return 403, "blocked"
				}
				return 200, article
			},
			wantCalls: 2, wantCode: 200, wantBody: "Real article text",
		},
		{
			name: "shell to the browser, prerendered html to the crawler",
			handler: func(ua string) (int, string) {
				if isCrawler(ua) {
					return 200, article
				}
				return 200, spaShell
			},
			wantCalls: 2, wantCode: 200, wantBody: "Real article text",
		},
		{
			name:      "shell for both keeps the browser result",
			handler:   func(string) (int, string) { return 200, spaShell },
			wantCalls: 2, wantCode: 200, wantBody: `<div id="root">`,
		},
		{
			name:      "403 for both returns the refusal",
			handler:   func(string) (int, string) { return 403, "blocked" },
			wantCalls: 2, wantCode: 403, wantBody: "blocked",
		},
		{
			name:      "404 is not retried",
			handler:   func(string) (int, string) { return 404, "missing" },
			wantCalls: 1, wantCode: 404, wantBody: "missing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := server(t, &calls, tt.handler)
			page, err := Fetch(context.Background(), srv.Client(), srv.URL, accept, 1<<20)
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if got := calls.Load(); got != tt.wantCalls {
				t.Fatalf("requests = %d, want %d", got, tt.wantCalls)
			}
			if page.Status != tt.wantCode || !strings.Contains(string(page.Body), tt.wantBody) {
				t.Fatalf("page = %d %q, want %d containing %q", page.Status, page.Body, tt.wantCode, tt.wantBody)
			}
		})
	}
}

func TestFetchSendsBrowserHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(article))
	}))
	defer srv.Close()

	if _, err := Fetch(context.Background(), srv.Client(), srv.URL, accept, 1<<20); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	for k, want := range map[string]string{
		"User-Agent":      browserUA,
		"Accept":          accept,
		"Accept-Language": "en-US,en;q=0.9",
		"Sec-Fetch-Mode":  "navigate",
	} {
		if got.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, got.Get(k), want)
		}
	}
}

func TestFetchCapsBodyAtMaxPlusOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()

	page, err := Fetch(context.Background(), srv.Client(), srv.URL, accept, 10)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(page.Body) != 11 {
		t.Fatalf("body = %d bytes, want 11 (max+1)", len(page.Body))
	}
}

func TestFetchTransportErrorReturned(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	if _, err := Fetch(context.Background(), http.DefaultClient, srv.URL, accept, 1<<20); err == nil {
		t.Fatal("Fetch on a closed server returned no error")
	}
}
