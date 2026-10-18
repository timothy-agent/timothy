package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
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

func TestOpenAIResponsesListModels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  int
		body    string
		want    []string
		wantErr string
	}{
		{name: "decodes envelope", status: http.StatusOK, body: `{"object":"list","data":[{"id":"gpt-a","object":"model"},{"id":"gpt-b"}]}`, want: []string{"gpt-a", "gpt-b"}},
		{name: "non-200 errors", status: http.StatusUnauthorized, body: `{"error":"bad key"}`, wantErr: "openai-responses: list models: http 401"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gotAuth, gotPath, gotExtra, gotMethod string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				gotPath = r.URL.Path
				gotExtra = r.Header.Get("X-Extra")
				gotMethod = r.Method
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			var p Provider = NewOpenAIResponses(OpenAIResponsesConfig{
				Name: "ors-test", BaseURL: srv.URL + "/v1", APIKey: "test-key",
				Headers: map[string]string{"X-Extra": "v"}, Timeout: 10 * time.Second,
			})
			lister, ok := p.(ModelLister)
			if !ok {
				t.Fatal("openai-responses does not implement ModelLister")
			}

			models, err := lister.ListModels(t.Context())
			if gotMethod != http.MethodGet || gotAuth != "Bearer test-key" || gotPath != "/v1/models" || gotExtra != "v" {
				t.Fatalf("request method=%q auth=%q path=%q extra=%q", gotMethod, gotAuth, gotPath, gotExtra)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			if got := modelIDs(models); strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("ids = %v, want %v", got, tt.want)
			}
		})
	}
}

func modelIDs(models []AvailableModel) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

// stubFoundationModels records the aws.Config the control client was
// built from and serves a fixed ListFoundationModels result.
type stubFoundationModels struct {
	cfg       aws.Config
	called    bool
	summaries []bedrocktypes.FoundationModelSummary
	err       error
}

func (s *stubFoundationModels) build(cfg aws.Config) foundationModelLister {
	s.cfg = cfg
	return s
}

func (s *stubFoundationModels) ListFoundationModels(context.Context, *bedrock.ListFoundationModelsInput, ...func(*bedrock.Options)) (*bedrock.ListFoundationModelsOutput, error) {
	s.called = true
	if s.err != nil {
		return nil, s.err
	}
	return &bedrock.ListFoundationModelsOutput{ModelSummaries: s.summaries}, nil
}

func staticBedrock(stub *stubFoundationModels) *Bedrock {
	b := NewBedrock(BedrockConfig{
		Name:              "br-test",
		Region:            "us-west-2",
		StaticCredentials: &StaticCredentials{AccessKeyID: "AKIASTATIC", SecretAccessKey: "secret"},
	})
	b.newControl = stub.build
	return b
}

func TestBedrockListModelsFilter(t *testing.T) {
	t.Parallel()
	text := []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText}
	tests := []struct {
		name       string
		id         *string
		modalities []bedrocktypes.ModelModality
		inference  []bedrocktypes.InferenceType
		keep       bool
	}{
		{name: "text on-demand", id: aws.String("amazon.nova-pro-v1:0"), modalities: text, inference: []bedrocktypes.InferenceType{bedrocktypes.InferenceTypeOnDemand}, keep: true},
		{name: "text on-demand and provisioned", id: aws.String("amazon.titan-text-premier-v1:0"), modalities: text, inference: []bedrocktypes.InferenceType{bedrocktypes.InferenceTypeProvisioned, bedrocktypes.InferenceTypeOnDemand}, keep: true},
		{name: "text and image output on-demand", id: aws.String("vendor.multi-v1:0"), modalities: []bedrocktypes.ModelModality{bedrocktypes.ModelModalityImage, bedrocktypes.ModelModalityText}, inference: []bedrocktypes.InferenceType{bedrocktypes.InferenceTypeOnDemand}, keep: true},
		{name: "text provisioned only", id: aws.String("amazon.nova-pro-v1:0:300k"), modalities: text, inference: []bedrocktypes.InferenceType{bedrocktypes.InferenceTypeProvisioned}},
		{name: "text no inference types", id: aws.String("vendor.profile-only-v1:0"), modalities: text},
		{name: "image output on-demand", id: aws.String("amazon.nova-canvas-v1:0"), modalities: []bedrocktypes.ModelModality{bedrocktypes.ModelModalityImage}, inference: []bedrocktypes.InferenceType{bedrocktypes.InferenceTypeOnDemand}},
		{name: "embedding output on-demand", id: aws.String("amazon.titan-embed-text-v2:0"), modalities: []bedrocktypes.ModelModality{bedrocktypes.ModelModalityEmbedding}, inference: []bedrocktypes.InferenceType{bedrocktypes.InferenceTypeOnDemand}},
		{name: "missing id", modalities: text, inference: []bedrocktypes.InferenceType{bedrocktypes.InferenceTypeOnDemand}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stub := &stubFoundationModels{summaries: []bedrocktypes.FoundationModelSummary{{
				ModelId:                 tt.id,
				ModelName:               aws.String("Display"),
				OutputModalities:        tt.modalities,
				InferenceTypesSupported: tt.inference,
			}}}
			models, err := staticBedrock(stub).ListModels(t.Context())
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			if !tt.keep {
				if len(models) != 0 {
					t.Fatalf("models = %+v, want none", models)
				}
				return
			}
			if len(models) != 1 || models[0].ID != *tt.id || models[0].DisplayName != "Display" {
				t.Fatalf("models = %+v, want [%s Display]", models, *tt.id)
			}
		})
	}
}

// Ambient rows must list through the SDK default chain and static rows
// through the secret-store keys, each in the row's region, the same
// config the chat client uses.
func TestBedrockListModelsAuth(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAAMBIENT")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent")
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	tests := []struct {
		name       string
		cfg        BedrockConfig
		wantRegion string
		wantKey    string
	}{
		{
			name:       "static keys, secret region wins",
			cfg:        BedrockConfig{Name: "b", Region: "us-east-1", StaticCredentials: &StaticCredentials{AccessKeyID: "AKIASTATIC", SecretAccessKey: "secret", Region: "eu-west-1"}},
			wantRegion: "eu-west-1",
			wantKey:    "AKIASTATIC",
		},
		{
			name:       "ambient default chain",
			cfg:        BedrockConfig{Name: "b", Auth: AuthAmbient, Region: "ap-southeast-2"},
			wantRegion: "ap-southeast-2",
			wantKey:    "AKIAAMBIENT",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubFoundationModels{}
			b := NewBedrock(tt.cfg)
			b.newControl = stub.build
			if _, err := b.ListModels(t.Context()); err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			if stub.cfg.Region != tt.wantRegion {
				t.Fatalf("region = %q, want %q", stub.cfg.Region, tt.wantRegion)
			}
			creds, err := stub.cfg.Credentials.Retrieve(t.Context())
			if err != nil {
				t.Fatalf("retrieve credentials: %v", err)
			}
			if creds.AccessKeyID != tt.wantKey {
				t.Fatalf("access key = %q, want %q", creds.AccessKeyID, tt.wantKey)
			}
		})
	}
}

func TestBedrockListModelsErrors(t *testing.T) {
	t.Parallel()
	t.Run("control plane error wraps", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("AccessDeniedException")
		stub := &stubFoundationModels{err: sentinel}
		_, err := staticBedrock(stub).ListModels(t.Context())
		if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "bedrock: list models") {
			t.Fatalf("err = %v, want wrapped %v", err, sentinel)
		}
	})
	t.Run("config error skips the control plane", func(t *testing.T) {
		t.Parallel()
		stub := &stubFoundationModels{}
		b := NewBedrock(BedrockConfig{Name: "b", Auth: AuthAmbient})
		b.newControl = stub.build
		_, err := b.ListModels(t.Context())
		if err == nil || !strings.Contains(err.Error(), "options.region") {
			t.Fatalf("err = %v, want the missing-region error", err)
		}
		if stub.called {
			t.Fatal("control plane called despite a config error")
		}
	})
}
