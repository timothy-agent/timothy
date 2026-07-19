package secretstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSplitRef(t *testing.T) {
	cases := []struct {
		ref, defField, wantRef, wantField string
	}{
		{"timothy/anthropic", "value", "timothy/anthropic", "value"},
		{"timothy/anthropic#api_key", "value", "timothy/anthropic", "api_key"},
		{"arn:aws:secretsmanager:eu-west-1:1:secret:x", "", "arn:aws:secretsmanager:eu-west-1:1:secret:x", ""},
		{"name#key", "", "name", "key"},
	}
	for _, c := range cases {
		ref, field := splitRef(c.ref, c.defField)
		if ref != c.wantRef || field != c.wantField {
			t.Errorf("splitRef(%q, %q) = (%q, %q), want (%q, %q)",
				c.ref, c.defField, ref, field, c.wantRef, c.wantField)
		}
	}
}

func TestPluckJSONField(t *testing.T) {
	val, err := pluckJSONField(`{"api_key":"sk-123","other":"x"}`, "api_key")
	if err != nil || val != "sk-123" {
		t.Fatalf("pluckJSONField = (%q, %v), want (sk-123, nil)", val, err)
	}
	if _, err := pluckJSONField(`{"a":"b"}`, "missing"); err == nil {
		t.Fatal("missing key: want error")
	}
	if _, err := pluckJSONField(`not json`, "a"); err == nil {
		t.Fatal("non-JSON secret: want error")
	}
}

func TestNormalizeBackendConfig(t *testing.T) {
	got, err := normalizeBackendConfig("vault", json.RawMessage(`{"address":"http://vault:8200"}`))
	if err != nil {
		t.Fatal(err)
	}
	var cfg VaultConfig
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Mount != defaultVaultMount || cfg.TokenRef != defaultVaultTokenRef {
		t.Errorf("defaults not filled: %+v", cfg)
	}

	if _, err := normalizeBackendConfig("vault", json.RawMessage(`{}`)); err == nil {
		t.Fatal("vault without address: want error")
	}
	if _, err := normalizeBackendConfig("db", json.RawMessage(`{}`)); err == nil {
		t.Fatal("backend db: want error (only vault/asm have config)")
	}
	if _, err := normalizeBackendConfig("asm", json.RawMessage(`{"region":"eu-west-1"}`)); err != nil {
		t.Fatalf("asm config: %v", err)
	}
}

func TestVaultRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "tok-1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path != "/v1/secret/data/timothy/anthropic" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"data":{"data":{"value":"sk-abc"}}}`))
	}))
	defer srv.Close()

	var body struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	err := vaultRequest(context.Background(), srv.URL+"/", "/v1/secret/data/timothy/anthropic", "tok-1", &body)
	if err != nil {
		t.Fatal(err)
	}
	if body.Data.Data["value"] != "sk-abc" {
		t.Errorf("value = %q, want sk-abc", body.Data.Data["value"])
	}

	err = vaultRequest(context.Background(), srv.URL, "/v1/secret/data/timothy/anthropic", "wrong", nil)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("bad token: want 403 error, got %v", err)
	}
}
