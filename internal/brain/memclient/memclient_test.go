package memclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractRoundTrip(t *testing.T) {
	t.Parallel()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/extract" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"memory_ids": []string{"a", "b"}})
	}))
	defer srv.Close()

	ids, err := New(srv.URL).Extract(t.Context(), "s1", 42, "turn text")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ids) != 2 || ids[0] != "a" {
		t.Fatalf("ids = %v", ids)
	}
	if got["session_id"] != "s1" || got["source_seq"] != float64(42) || got["text"] != "turn text" {
		t.Fatalf("request body = %v", got)
	}
}

func TestExtractSurfacesHTTPError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":"extraction_failed"}}`, http.StatusBadGateway)
	}))
	defer srv.Close()

	if _, err := New(srv.URL).Extract(t.Context(), "s1", 1, "x"); err == nil {
		t.Fatal("Extract succeeded on 502, want error")
	}
}
