package pdfgen

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientRenderSuccess(t *testing.T) {
	want := []byte("%PDF-1.4 fake bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/render" {
			t.Fatalf("path = %q, want /render", r.URL.Path)
		}
		var body renderRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Documents) != 1 || body.Documents[0].Title != "Report" {
			t.Fatalf("request = %+v, want one Report document", body)
		}
		if !body.Options.TOC || body.Options.CoverTitle != "Cover" {
			t.Fatalf("options = %+v, want TOC true and CoverTitle Cover", body.Options)
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(want)
	}))
	defer srv.Close()

	c := New(srv.URL)
	got, err := c.Render(t.Context(), []Document{{Title: "Report", Content: "# Hi"}}, Options{CoverTitle: "Cover", TOC: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestClientRenderBadRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "documents must not be empty"})
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.Render(t.Context(), nil, Options{})
	if err == nil || !strings.Contains(err.Error(), "documents must not be empty") {
		t.Fatalf("err = %v, want it to surface the sidecar error message", err)
	}
}

func TestClientRenderServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "typst compile failed"})
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.Render(t.Context(), []Document{{Title: "T", Content: "x"}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "typst compile failed") {
		t.Fatalf("err = %v, want it to surface the sidecar error message", err)
	}
}

func TestClientRenderMalformedErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.Render(t.Context(), []Document{{Title: "T", Content: "x"}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "http 500") {
		t.Fatalf("err = %v, want it to fall back to the status code", err)
	}
}
