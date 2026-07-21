package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpenAICompatListModels(t *testing.T) {
	t.Parallel()
	var gotAuth, gotPath, gotExtra string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotExtra = r.Header.Get("X-Extra")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m-large","object":"model"},{"id":"m-small"}]}`))
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAICompat(OpenAICompatConfig{
		Name: "oai-test", BaseURL: srv.URL, APIKey: "test-key",
		Headers: map[string]string{"X-Extra": "v"}, Timeout: 10 * time.Second,
	})

	models, err := p.ListModels(t.Context())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotAuth != "Bearer test-key" || gotPath != "/models" || gotExtra != "v" {
		t.Fatalf("request auth=%q path=%q extra=%q", gotAuth, gotPath, gotExtra)
	}
	if len(models) != 2 || models[0].ID != "m-large" || models[1].ID != "m-small" {
		t.Fatalf("models = %+v", models)
	}
}

func TestAnthropicListModels(t *testing.T) {
	t.Parallel()
	var gotKey, gotVersion, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		gotVersion = r.Header.Get("Anthropic-Version")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-a","display_name":"A"},{"id":"claude-b"}]}`))
	}))
	t.Cleanup(srv.Close)
	p := NewAnthropic(AnthropicConfig{
		Name: "ant-test", BaseURL: srv.URL, APIKey: "test-key", Timeout: 10 * time.Second,
	})

	models, err := p.ListModels(t.Context())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotKey != "test-key" || gotVersion == "" || gotPath != "/v1/models" {
		t.Fatalf("request key=%q version=%q path=%q", gotKey, gotVersion, gotPath)
	}
	if len(models) != 2 || models[0].ID != "claude-a" || models[1].ID != "claude-b" {
		t.Fatalf("models = %+v", models)
	}
}

func TestListModelsMalformedBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAICompat(OpenAICompatConfig{
		Name: "oai-test", BaseURL: srv.URL, APIKey: "k", Timeout: 10 * time.Second,
	})
	if _, err := p.ListModels(t.Context()); err == nil {
		t.Fatal("malformed body accepted")
	}
}

// Bedrock deliberately does not implement ModelLister: its control
// plane needs separate AWS permissions, and the UI's manual-entry
// fallback covers it.
func TestBedrockDoesNotListModels(t *testing.T) {
	t.Parallel()
	var p Provider = NewBedrock(BedrockConfig{Name: "br-test", Region: "us-east-1"})
	if _, ok := p.(ModelLister); ok {
		t.Fatal("bedrock unexpectedly implements ModelLister; update the admin fallback contract")
	}
}
