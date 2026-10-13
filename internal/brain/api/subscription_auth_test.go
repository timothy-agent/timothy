package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubscriptionAuth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v8/management/oauth/auth-url", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		provider := r.URL.Query().Get("provider")
		if provider != "codex" && provider != "claude" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"state":  "mock-state-123",
			"url":    "https://auth.example.com/oauth?provider=" + provider,
		})
	})
	mux.HandleFunc("GET /v8/management/oauth/status", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		state := r.URL.Query().Get("state")
		if state != "mock-state-123" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
		})
	})
	mux.HandleFunc("POST /v8/management/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["provider"] == "" || b["redirect_url"] == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	})
	mux.HandleFunc("DELETE /v8/management/oauth/session", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"cancelled": true, "status": "ok"})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	api := &API{
		token: "test-token",
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	appMux := http.NewServeMux()
	api.registerSubscriptionAuth(appMux.Handle, server.URL, "test-key")

	t.Run("start chatgpt maps to codex", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/providers/subscription/auth/start", strings.NewReader(`{"provider":"chatgpt"}`))
		req.Header.Set("Authorization", "Bearer test-token")
		appMux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var res map[string]any
		_ = json.NewDecoder(rec.Body).Decode(&res)
		if res["state"] != "mock-state-123" {
			t.Errorf("unexpected state: %v", res["state"])
		}
		if !strings.Contains(res["url"].(string), "provider=codex") {
			t.Errorf("expected url with provider=codex, got %v", res["url"])
		}
	})

	t.Run("start claude maps to claude", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/providers/subscription/auth/start", strings.NewReader(`{"provider":"claude"}`))
		req.Header.Set("Authorization", "Bearer test-token")
		appMux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("start invalid provider returns 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/providers/subscription/auth/start", strings.NewReader(`{"provider":"unknown"}`))
		req.Header.Set("Authorization", "Bearer test-token")
		appMux.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("check status", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/providers/subscription/auth/status?state=mock-state-123", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		appMux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("submit callback", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/providers/subscription/auth/callback", strings.NewReader(`{"provider":"chatgpt","redirect_url":"http://localhost:1455/cb"}`))
		req.Header.Set("Authorization", "Bearer test-token")
		appMux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("cancel session", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/v1/providers/subscription/auth/session?state=mock-state-123", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		appMux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}
