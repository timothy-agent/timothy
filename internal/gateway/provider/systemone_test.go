package provider

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSystemOnePresetDefaults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		build       func(SystemOneConfig) *SystemOne
		cfg         SystemOneConfig
		wantDriver  string
		wantBaseURL string
		wantModel   string
		wantTimeout time.Duration
	}{
		{"jev empty config", NewJev, SystemOneConfig{Name: "j"}, "jev", DefaultJevBaseURL, DefaultJevModel, systemoneTimeout},
		{"jev explicit config", NewJev, SystemOneConfig{Name: "j", BaseURL: "http://jev.test", Timeout: time.Second}, "jev", "http://jev.test", DefaultJevModel, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := tc.build(tc.cfg)
			if s.Name() != tc.cfg.Name {
				t.Fatalf("Name() = %q, want %q", s.Name(), tc.cfg.Name)
			}
			if s.preset.driver != tc.wantDriver {
				t.Fatalf("driver = %q, want %q", s.preset.driver, tc.wantDriver)
			}
			if s.cfg.BaseURL != tc.wantBaseURL {
				t.Fatalf("BaseURL = %q, want %q", s.cfg.BaseURL, tc.wantBaseURL)
			}
			if s.preset.defaultModel != tc.wantModel {
				t.Fatalf("defaultModel = %q, want %q", s.preset.defaultModel, tc.wantModel)
			}
			if s.cfg.Timeout != tc.wantTimeout {
				t.Fatalf("Timeout = %v, want %v", s.cfg.Timeout, tc.wantTimeout)
			}
			if s.preset.listModels == nil {
				t.Fatal("preset has no model lister")
			}
		})
	}
}

// optIDs builds decide options that carry only an id.
func optIDs(ids ...string) []DecideOption {
	opts := make([]DecideOption, len(ids))
	for i, id := range ids {
		opts[i] = DecideOption{ID: id}
	}
	return opts
}

// TestSystemOneChoiceCriteriaMapping pins the typed option wire:
// criteria map each id to its description (the id when there is none),
// and the reply matches on id, so an id with a colon round-trips exactly.
func TestSystemOneChoiceCriteriaMapping(t *testing.T) {
	t.Parallel()
	opts := []DecideOption{
		{ID: "openai", Description: "any OpenAI model"},
		{ID: "openai:gpt-5", Description: "flagship"},
		{ID: "local:qwen3:4b", Description: "cheap: runs locally"},
		{ID: "plain"},
	}
	wantCrit := map[string]any{
		"openai":         "any OpenAI model",
		"openai:gpt-5":   "flagship",
		"local:qwen3:4b": "cheap: runs locally",
		"plain":          "plain",
	}
	for _, tc := range []struct {
		name       string
		reply      string
		wantChoice string
		wantErr    bool
	}{
		{"colon id round-trips", "openai:gpt-5", "openai:gpt-5", false},
		{"id with two colons round-trips", "local:qwen3:4b", "local:qwen3:4b", false},
		{"prefix of a colon id is its own option", "openai", "openai", false},
		{"case-insensitive returns id as given", "LOCAL:QWEN3:4B", "local:qwen3:4b", false},
		{"description is not an id", "flagship", "", true},
		{"full label is not an id", "openai:gpt-5: flagship", "", true},
	} {
		stub := newJevStub(t, `{"type":"choice","choice":`+strconv.Quote(tc.reply)+`}`)
		s := NewJev(SystemOneConfig{Name: "d", BaseURL: stub.srv.URL})
		t.Run(s.preset.driver+"/"+tc.name, func(t *testing.T) {
			t.Parallel()
			res, _, err := s.Decide(t.Context(), "m", DecideRequest{Kind: DecideChoice, Question: "Which route?", Options: opts})
			if q := stub.got.Questions[systemoneQuestionID]; !reflect.DeepEqual(q.Criteria, wantCrit) {
				t.Fatalf("criteria = %#v, want %#v", q.Criteria, wantCrit)
			}
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "not an allowed option") {
					t.Fatalf("err = %v, want 'not an allowed option'", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if res.Choice != tc.wantChoice {
				t.Fatalf("choice = %q, want %q", res.Choice, tc.wantChoice)
			}
		})
	}
}
