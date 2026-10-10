package pdfgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestClientRasterize(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`)
	png := []byte("\x89PNG fake")
	tests := []struct {
		name    string
		status  int
		body    []byte
		want    []byte
		wantErr string
	}{
		{"success", http.StatusOK, png, png, ""},
		{"too large", http.StatusRequestEntityTooLarge, []byte(`{"error":"request body too large"}`), nil, "http 413: request body too large"},
		{"bad svg", http.StatusUnprocessableEntity, []byte(`{"error":"svg rasterize failed: failed to parse SVG"}`), nil, "http 422: svg rasterize failed"},
		{"timeout", http.StatusGatewayTimeout, []byte(`{"error":"svg rasterize timed out after 20s"}`), nil, "http 504: svg rasterize timed out"},
		{"malformed error body", http.StatusInternalServerError, []byte("not json"), nil, "http 500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/rasterize" {
					t.Errorf("request = %s %s, want POST /rasterize", r.Method, r.URL.Path)
				}
				if ct := r.Header.Get("Content-Type"); ct != "image/svg+xml" {
					t.Errorf("content-type = %q, want image/svg+xml", ct)
				}
				if got, _ := io.ReadAll(r.Body); !bytes.Equal(got, svg) {
					t.Errorf("body = %q, want the svg", got)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write(tt.body)
			}))
			defer srv.Close()

			got, err := New(srv.URL).Rasterize(t.Context(), svg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Rasterize: %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientRasterizeHasShortDeadline(t *testing.T) {
	c := New("http://pdfgen.invalid")
	var deadline time.Time
	var ok bool
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok = r.Context().Deadline()
		return nil, errors.New("stop")
	})
	if _, err := c.Rasterize(t.Context(), []byte("<svg/>")); err == nil {
		t.Fatal("err = nil, want the transport error")
	}
	if left := time.Until(deadline); !ok || left > rasterizeTimeout || rasterizeTimeout >= renderTimeout {
		t.Fatalf("deadline set=%v in %v, want within %v and below the render timeout", ok, left, rasterizeTimeout)
	}
}
