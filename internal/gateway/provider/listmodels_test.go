package provider

import (
	"encoding/json"
	"math"
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

func TestPerMTok(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want *float64
	}{
		{name: "paid", raw: `"0.0000025"`, want: fptr(2.5)},
		{name: "exponent", raw: `"1e-6"`, want: fptr(1)},
		{name: "free", raw: `"0"`, want: fptr(0)},
		{name: "missing", raw: ``, want: nil},
		{name: "null", raw: `null`, want: nil},
		{name: "empty string", raw: `""`, want: nil},
		{name: "malformed string", raw: `"abc"`, want: nil},
		{name: "variable price", raw: `"-1"`, want: nil},
		{name: "not a number", raw: `"NaN"`, want: nil},
		{name: "infinite", raw: `"Inf"`, want: nil},
		{name: "number not string", raw: `0.000001`, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := perMTok(json.RawMessage(tc.raw))
			if !sameFloat(got, tc.want) {
				t.Fatalf("perMTok(%s) = %v, want %v", tc.raw, deref(got), deref(tc.want))
			}
		})
	}
}

func TestIsOpenRouterHost(t *testing.T) {
	t.Parallel()
	for url, want := range map[string]bool{
		"https://openrouter.ai/api/v1":      true,
		"https://openrouter.ai:443/api/v1":  true,
		"https://api.openrouter.ai.evil/v1": false,
		"https://api.openai.com/v1":         false,
		"http://localhost:11434/v1":         false,
		"::bad":                             false,
	} {
		if got := IsOpenRouterHost(url); got != want {
			t.Errorf("IsOpenRouterHost(%q) = %v, want %v", url, got, want)
		}
	}
}

// openRouterModels is a trimmed openrouter.ai /models reply: a paid
// model, a :free model, a variable-price router, malformed values and
// an entry without pricing.
const openRouterModels = `{"data":[
	{"id":"openai/gpt-4o","name":"OpenAI: GPT-4o","context_length":128000,
	 "pricing":{"prompt":"0.0000025","completion":"0.00001","request":"0","input_cache_read":"0.00000125"}},
	{"id":"meta-llama/llama-3.3-70b-instruct:free","context_length":65536,
	 "pricing":{"prompt":"0","completion":"0"}},
	{"id":"openrouter/auto","context_length":null,"pricing":{"prompt":"-1","completion":"-1"}},
	{"id":"broken/model","context_length":"lots","pricing":{"prompt":"cheap","completion":0.00001}},
	{"id":"bare/model"}
]}`

// rewriteHost sends every request to target over plain http, keeping
// the path.
type rewriteHost struct{ target string }

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = r.target
	return http.DefaultTransport.RoundTrip(req)
}

func TestOpenAICompatListModelsOpenRouterPricing(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(openRouterModels))
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAICompat(OpenAICompatConfig{
		Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", APIKey: "k", Timeout: 10 * time.Second,
	})
	p.client = &http.Client{Transport: rewriteHost{target: srv.Listener.Addr().String()}}

	models, err := p.ListModels(t.Context())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotPath != "/api/v1/models" {
		t.Fatalf("path = %q", gotPath)
	}
	assertModels(t, models, []AvailableModel{
		{ID: "openai/gpt-4o", InputPerMTok: fptr(2.5), OutputPerMTok: fptr(10), CacheReadPerMTok: fptr(1.25), ContextLength: iptr(128000)},
		{ID: "meta-llama/llama-3.3-70b-instruct:free", InputPerMTok: fptr(0), OutputPerMTok: fptr(0), ContextLength: iptr(65536)},
		{ID: "openrouter/auto"},
		{ID: "broken/model"},
		{ID: "bare/model"},
	})
}

func TestOpenAICompatListModelsIgnoresPricingOffOpenRouter(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(openRouterModels))
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAICompat(OpenAICompatConfig{Name: "other", BaseURL: srv.URL, APIKey: "k", Timeout: 10 * time.Second})

	models, err := p.ListModels(t.Context())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	assertModels(t, models, []AvailableModel{
		{ID: "openai/gpt-4o"}, {ID: "meta-llama/llama-3.3-70b-instruct:free"},
		{ID: "openrouter/auto"}, {ID: "broken/model"}, {ID: "bare/model"},
	})
}

func assertModels(t *testing.T, got, want []AvailableModel) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d models %+v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.ID != w.ID || !sameFloat(g.InputPerMTok, w.InputPerMTok) || !sameFloat(g.OutputPerMTok, w.OutputPerMTok) ||
			!sameFloat(g.CacheReadPerMTok, w.CacheReadPerMTok) || !sameFloat(g.CacheWritePerMTok, w.CacheWritePerMTok) ||
			(g.ContextLength == nil) != (w.ContextLength == nil) || (g.ContextLength != nil && *g.ContextLength != *w.ContextLength) {
			t.Fatalf("model %d = %+v, want %+v", i, g, w)
		}
	}
}

func fptr(v float64) *float64 { return &v }
func iptr(v int64) *int64     { return &v }

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func sameFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return math.Abs(*a-*b) < 1e-9
}
