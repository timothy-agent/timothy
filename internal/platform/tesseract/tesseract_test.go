package tesseract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRecognize(t *testing.T) {
	t.Run("posts bytes and returns the recognized text", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/recognize" {
				t.Errorf("path = %q, want /recognize", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text": "invoice total 42"}`))
		}))
		defer srv.Close()

		out, err := Recognize(context.Background(), srv.Client(), srv.URL, []byte("fake png bytes"), "image/png")
		if err != nil {
			t.Fatalf("Recognize: %v", err)
		}
		if out != "invoice total 42" {
			t.Fatalf("out = %q", out)
		}
	})

	t.Run("passes the media type as Content-Type", func(t *testing.T) {
		var gotType string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotType = r.Header.Get("Content-Type")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text": "x"}`))
		}))
		defer srv.Close()

		if _, err := Recognize(context.Background(), srv.Client(), srv.URL, []byte("x"), "image/jpeg"); err != nil {
			t.Fatalf("Recognize: %v", err)
		}
		if gotType != "image/jpeg" {
			t.Fatalf("Content-Type = %q, want image/jpeg", gotType)
		}
	})

	t.Run("empty media type falls back to octet-stream", func(t *testing.T) {
		var gotType string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotType = r.Header.Get("Content-Type")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text": "x"}`))
		}))
		defer srv.Close()

		if _, err := Recognize(context.Background(), srv.Client(), srv.URL, []byte("x"), ""); err != nil {
			t.Fatalf("Recognize: %v", err)
		}
		if gotType != "application/octet-stream" {
			t.Fatalf("Content-Type = %q, want application/octet-stream", gotType)
		}
	})

	t.Run("unconfigured base URL errors without a request", func(t *testing.T) {
		if _, err := Recognize(context.Background(), nil, "", []byte("x"), "image/png"); err == nil {
			t.Fatal("empty baseURL accepted")
		}
	})

	t.Run("non-2xx surfaces status and body snippet", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "ocr failed: unreadable image", http.StatusUnprocessableEntity)
		}))
		defer srv.Close()

		_, err := Recognize(context.Background(), srv.Client(), srv.URL, []byte("x"), "image/png")
		if err == nil || !strings.Contains(err.Error(), "422") {
			t.Fatalf("err = %v, want http 422 surfaced", err)
		}
	})

	t.Run("malformed json response errors", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text": `))
		}))
		defer srv.Close()

		if _, err := Recognize(context.Background(), srv.Client(), srv.URL, []byte("x"), "image/png"); err == nil {
			t.Fatal("malformed json accepted")
		}
	})

	t.Run("caller context already canceled fails fast", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(time.Second):
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Recognize(ctx, srv.Client(), srv.URL, []byte("x"), "image/png"); err == nil {
			t.Fatal("canceled context accepted")
		}
	})
}
