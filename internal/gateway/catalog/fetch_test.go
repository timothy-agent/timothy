package catalog

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// fixtureJSON mirrors LiteLLM's shape: sample_spec (documentation with
// STRING-typed values, always skipped before its fields decode), an
// undecodable entry (string token max — skipped, not fatal), an entry
// missing cost fields (NULL preserved), a float token max (real
// upstream shape), and a normal fully-priced entry.
const fixtureJSON = `{
	"sample_spec": {
		"litellm_provider": "one of https://docs.litellm.ai/docs/providers",
		"mode": "one of: chat, embedding, ...",
		"max_input_tokens": "max input tokens, if the provider specifies it",
		"input_cost_per_token": 0.0000
	},
	"bad-shape-entry": {
		"litellm_provider": "openai",
		"mode": "chat",
		"max_input_tokens": "not-a-number"
	},
	"openai/float-max-entry": {
		"litellm_provider": "openai",
		"mode": "chat",
		"max_input_tokens": 128000.0,
		"input_cost_per_token": 0.000002
	},
	"no-provider-entry": {
		"mode": "chat",
		"input_cost_per_token": 0.000001
	},
	"anthropic/claude-no-cost": {
		"litellm_provider": "anthropic",
		"mode": "chat",
		"max_input_tokens": 200000
	},
	"anthropic/claude-3-5-sonnet": {
		"litellm_provider": "anthropic",
		"mode": "chat",
		"max_input_tokens": 200000,
		"max_output_tokens": 8192,
		"input_cost_per_token": 0.000003,
		"output_cost_per_token": 0.000015,
		"cache_read_input_token_cost": 0.0000003,
		"cache_creation_input_token_cost": 0.00000375
	}
}`

func TestFetchParsesFixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc123"`)
		_, _ = w.Write([]byte(fixtureJSON))
	}))
	defer srv.Close()

	res, err := Fetch(srv.URL, "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.NotModified {
		t.Fatal("expected a fresh fetch, got NotModified")
	}
	if res.ETag != `"abc123"` {
		t.Fatalf("ETag = %q", res.ETag)
	}
	// sample_spec, the providerless entry, and the undecodable entry
	// are skipped: 3 of 6 keys survive.
	if len(res.Entries) != 3 {
		t.Fatalf("Entries len = %d, want 3", len(res.Entries))
	}

	byKey := map[string]Entry{}
	for _, e := range res.Entries {
		byKey[e.ModelKey] = e
	}
	if _, ok := byKey["sample_spec"]; ok {
		t.Fatal("sample_spec must be skipped")
	}
	if _, ok := byKey["no-provider-entry"]; ok {
		t.Fatal("entry without litellm_provider must be skipped")
	}
	if _, ok := byKey["bad-shape-entry"]; ok {
		t.Fatal("undecodable entry must be skipped, not fatal")
	}

	floatMax := byKey["openai/float-max-entry"]
	if floatMax.MaxInputTokens == nil || *floatMax.MaxInputTokens != 128000 {
		t.Fatalf("float MaxInputTokens = %v, want 128000", floatMax.MaxInputTokens)
	}

	noCost := byKey["anthropic/claude-no-cost"]
	if noCost.InputPerMTok != nil || noCost.OutputPerMTok != nil {
		t.Fatalf("missing cost fields must stay nil, got %+v", noCost)
	}
	if noCost.MaxInputTokens == nil || *noCost.MaxInputTokens != 200000 {
		t.Fatalf("MaxInputTokens = %v, want 200000", noCost.MaxInputTokens)
	}

	priced := byKey["anthropic/claude-3-5-sonnet"]
	if priced.InputPerMTok == nil || *priced.InputPerMTok != 3 {
		t.Fatalf("InputPerMTok = %v, want 3 (0.000003 * 1e6)", priced.InputPerMTok)
	}
	if priced.OutputPerMTok == nil || *priced.OutputPerMTok != 15 {
		t.Fatalf("OutputPerMTok = %v, want 15", priced.OutputPerMTok)
	}
	if priced.CacheReadPerMTok == nil || *priced.CacheReadPerMTok != 0.3 {
		t.Fatalf("CacheReadPerMTok = %v, want 0.3", priced.CacheReadPerMTok)
	}
	if priced.CacheWritePerMTok == nil || *priced.CacheWritePerMTok != 3.75 {
		t.Fatalf("CacheWritePerMTok = %v, want 3.75", priced.CacheWritePerMTok)
	}
}

func TestFetchNotModified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != `"abc123"` {
			t.Errorf("If-None-Match = %q, want etag echoed", r.Header.Get("If-None-Match"))
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	res, err := Fetch(srv.URL, `"abc123"`)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !res.NotModified {
		t.Fatal("expected NotModified")
	}
	if len(res.Entries) != 0 {
		t.Fatalf("Entries should be empty on 304, got %d", len(res.Entries))
	}
}

func TestFetchOversizeRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		big := make([]byte, maxBodyBytes+1)
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	if _, err := Fetch(srv.URL, ""); err == nil {
		t.Fatal("expected an error for an oversize response")
	}
}

func TestFetchUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := Fetch(srv.URL, ""); err == nil {
		t.Fatal("expected an error for a 500 status")
	}
}
