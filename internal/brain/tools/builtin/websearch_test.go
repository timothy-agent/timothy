package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearchParsesResults(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("format = %q, want json", r.URL.Query().Get("format"))
		}
		if r.URL.Query().Get("q") != "golang" {
			t.Errorf("q = %q, want golang", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"The Go Programming Language","url":"https://go.dev/","content":"Go is an open source language."},
			{"title":"Go Playground","url":"https://go.dev/play/","content":"Run Go code online."}
		]}`))
	}))
	defer srv.Close()

	tool := WebSearch(srv.URL)
	args, _ := json.Marshal(map[string]string{"query": "golang"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"The Go Programming Language", "https://go.dev/", "Go Playground"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWebSearchEmptyResults(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	tool := WebSearch(srv.URL)
	args, _ := json.Marshal(map[string]string{"query": "asdkjaslkdjaslkd"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "no results found" {
		t.Fatalf("out = %q, want %q", out, "no results found")
	}
}

func TestWebSearchRejectsEmptyQuery(t *testing.T) {
	t.Parallel()
	tool := WebSearch("http://unused")
	args, _ := json.Marshal(map[string]string{"query": "   "})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected an error for a blank query")
	}
}

func TestWebSearchSurfacesBackendErrors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tool := WebSearch(srv.URL)
	args, _ := json.Marshal(map[string]string{"query": "golang"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want it to mention http 503", err)
	}
}

func TestWebSearchCapsResultCount(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString(`{"results":[`)
		for i := 0; i < 25; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"title":"r","url":"https://example.com","content":"c"}`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	}))
	defer srv.Close()

	tool := WebSearch(srv.URL)
	args, _ := json.Marshal(map[string]string{"query": "x"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Count(out, "https://example.com") != webSearchMaxResult {
		t.Fatalf("result count = %d, want %d", strings.Count(out, "https://example.com"), webSearchMaxResult)
	}
}
