package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterTranscribe(t *testing.T) {
	t.Run("absent when whisperURL unset", func(t *testing.T) {
		a, _, _ := testAPI(t, "tok", nil)
		m := http.NewServeMux()
		a.registerTranscribe(m.Handle, http.DefaultClient, "")

		req := httptest.NewRequest(http.MethodPost, "/v1/transcribe", strings.NewReader("audio"))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("code = %d, want 404 (route unmounted)", w.Code)
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("sidecar must not be reached without auth")
		}))
		defer srv.Close()

		a, _, _ := testAPI(t, "tok", nil)
		m := http.NewServeMux()
		a.registerTranscribe(m.Handle, srv.Client(), srv.URL)

		req := httptest.NewRequest(http.MethodPost, "/v1/transcribe", strings.NewReader("audio"))
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("code = %d, want 401", w.Code)
		}
	})

	t.Run("proxies body to the sidecar and returns text", func(t *testing.T) {
		var gotBody []byte
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text": "hello world"}`))
		}))
		defer srv.Close()

		a, _, _ := testAPI(t, "tok", nil)
		m := http.NewServeMux()
		a.registerTranscribe(m.Handle, srv.Client(), srv.URL)

		req := httptest.NewRequest(http.MethodPost, "/v1/transcribe", strings.NewReader("fake audio bytes"))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
		}
		if gotPath != "/transcribe" {
			t.Fatalf("sidecar path = %q, want /transcribe", gotPath)
		}
		if string(gotBody) != "fake audio bytes" {
			t.Fatalf("sidecar body = %q", gotBody)
		}
		if !strings.Contains(w.Body.String(), "hello world") {
			t.Fatalf("response body = %s", w.Body.String())
		}
	})

	t.Run("forwards the language query param to the sidecar", func(t *testing.T) {
		var gotQuery string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text": "hello"}`))
		}))
		defer srv.Close()

		a, _, _ := testAPI(t, "tok", nil)
		m := http.NewServeMux()
		a.registerTranscribe(m.Handle, srv.Client(), srv.URL)

		req := httptest.NewRequest(http.MethodPost, "/v1/transcribe?language=bn", strings.NewReader("audio"))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
		}
		if gotQuery != "language=bn" {
			t.Fatalf("sidecar query = %q, want language=bn", gotQuery)
		}
	})

	t.Run("rejects an empty body", func(t *testing.T) {
		a, _, _ := testAPI(t, "tok", nil)
		m := http.NewServeMux()
		a.registerTranscribe(m.Handle, http.DefaultClient, "http://unused")

		req := httptest.NewRequest(http.MethodPost, "/v1/transcribe", strings.NewReader(""))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400", w.Code)
		}
	})

	t.Run("body over the cap is rejected", func(t *testing.T) {
		a, _, _ := testAPI(t, "tok", nil)
		m := http.NewServeMux()
		a.registerTranscribe(m.Handle, http.DefaultClient, "http://unused")

		big := strings.NewReader(strings.Repeat("a", transcribeBodyLimit+1))
		req := httptest.NewRequest(http.MethodPost, "/v1/transcribe", big)
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("code = %d, want 413", w.Code)
		}
	})

	t.Run("non-2xx from the sidecar surfaces as bad gateway", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "transcription failed", http.StatusUnprocessableEntity)
		}))
		defer srv.Close()

		a, _, _ := testAPI(t, "tok", nil)
		m := http.NewServeMux()
		a.registerTranscribe(m.Handle, srv.Client(), srv.URL)

		req := httptest.NewRequest(http.MethodPost, "/v1/transcribe", strings.NewReader("audio"))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		if w.Code != http.StatusBadGateway {
			t.Fatalf("code = %d, want 502", w.Code)
		}
	})
}
