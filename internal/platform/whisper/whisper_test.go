package whisper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTranscribe(t *testing.T) {
	t.Run("posts bytes and returns the transcript", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/transcribe" {
				t.Errorf("path = %q, want /transcribe", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text": "hello world"}`))
		}))
		defer srv.Close()

		out, err := Transcribe(context.Background(), srv.Client(), srv.URL, []byte("fake audio bytes"), "")
		if err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		if out != "hello world" {
			t.Fatalf("out = %q", out)
		}
	})

	t.Run("passes the language as a query param when set", func(t *testing.T) {
		var gotQuery string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text": "hello"}`))
		}))
		defer srv.Close()

		if _, err := Transcribe(context.Background(), srv.Client(), srv.URL, []byte("x"), "bn"); err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		if gotQuery != "language=bn" {
			t.Fatalf("query = %q, want language=bn", gotQuery)
		}
	})

	t.Run("omits the query param when language is empty", func(t *testing.T) {
		var gotQuery string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text": "hello"}`))
		}))
		defer srv.Close()

		if _, err := Transcribe(context.Background(), srv.Client(), srv.URL, []byte("x"), ""); err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		if gotQuery != "" {
			t.Fatalf("query = %q, want empty", gotQuery)
		}
	})

	t.Run("unconfigured base URL errors without a request", func(t *testing.T) {
		if _, err := Transcribe(context.Background(), nil, "", []byte("x"), ""); err == nil {
			t.Fatal("empty baseURL accepted")
		}
	})

	t.Run("non-2xx surfaces status and body snippet", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "transcription failed: bad audio", http.StatusUnprocessableEntity)
		}))
		defer srv.Close()

		_, err := Transcribe(context.Background(), srv.Client(), srv.URL, []byte("x"), "")
		if err == nil || !strings.Contains(err.Error(), "422") {
			t.Fatalf("err = %v, want http 422 surfaced", err)
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
		if _, err := Transcribe(ctx, srv.Client(), srv.URL, []byte("x"), ""); err == nil {
			t.Fatal("canceled context accepted")
		}
	})
}
