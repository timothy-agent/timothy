package gwclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListProviders(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/admin/providers" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"providers":[{"id":"p1","name":"OpenRouter","kind":"api","driver":"openaicompat","base_url":"https://openrouter.ai/api/v1","credential_ref":"OPENROUTER_API_KEY","enabled":true}]}`))
	}))
	defer srv.Close()

	got, err := New(srv.URL).ListProviders(t.Context())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(got) != 1 || got[0].Name != "OpenRouter" || got[0].CredentialRef != "OPENROUTER_API_KEY" {
		t.Fatalf("providers = %+v", got)
	}
}

func TestCreateProvider(t *testing.T) {
	t.Parallel()
	var received AdminProvider
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/admin/providers" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode: %v", err)
		}
		_, _ = w.Write([]byte(`{"id":"p9"}`))
	}))
	defer srv.Close()

	id, err := New(srv.URL).CreateProvider(t.Context(), AdminProvider{Name: "OpenRouter", Kind: "api", Driver: "openaicompat", Enabled: true})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if id != "p9" || received.Name != "OpenRouter" || received.Kind != "api" || !received.Enabled {
		t.Fatalf("id = %s, received = %+v", id, received)
	}
}

func TestCreateProviderGatewayError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"name taken"}`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL).CreateProvider(t.Context(), AdminProvider{Name: "OpenRouter"}); err == nil {
		t.Fatal("expected error on 409")
	}
}
