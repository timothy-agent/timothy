package provider

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
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
		{"laya empty config", NewLaya, SystemOneConfig{Name: "l"}, "laya", "", "", systemoneTimeout},
		{"laya explicit config", NewLaya, SystemOneConfig{Name: "l", BaseURL: "http://laya:8000", Timeout: time.Second}, "laya", "http://laya:8000", "", time.Second},
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

// TestSystemOnePresetsSendIdenticalBodies pins that laya and jev share one
// wire: the same model and DecideRequest produce byte-identical bodies.
func TestSystemOnePresetsSendIdenticalBodies(t *testing.T) {
	t.Parallel()
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"answers":{"q":{"type":"choice","choice":"a","noul":0.7,"score":1}},"usage":{"input_tokens":1}}`))
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name string
		req  DecideRequest
	}{
		{"choice", DecideRequest{Kind: DecideChoice, Question: "Pick one.", State: "some state", Options: []DecideOption{{ID: "a", Description: "first"}, {ID: "b"}}}},
		{"yesno", DecideRequest{Kind: DecideYesNo, Question: "Is it?", Options: optIDs("yes", "no")}},
		{"score default levels", DecideRequest{Kind: DecideScore, Question: "How much?"}},
		{"score custom levels", DecideRequest{Kind: DecideScore, Question: "How much?", Options: optIDs("low", "mid", "high")}},
	} {
		mu.Lock()
		bodies = nil
		mu.Unlock()
		for _, s := range []*SystemOne{
			NewLaya(SystemOneConfig{Name: "laya", BaseURL: srv.URL}),
			NewJev(SystemOneConfig{Name: "jev", BaseURL: srv.URL, APIKey: "k"}),
		} {
			if _, _, err := s.Decide(t.Context(), "m-1", tc.req); err != nil {
				t.Fatalf("%s %s: Decide: %v", tc.name, s.preset.driver, err)
			}
		}
		mu.Lock()
		got := bodies
		mu.Unlock()
		if len(got) != 2 {
			t.Fatalf("%s: got %d bodies, want 2", tc.name, len(got))
		}
		if !bytes.Equal(got[0], got[1]) {
			t.Fatalf("%s: bodies differ\nlaya: %s\njev:  %s", tc.name, got[0], got[1])
		}
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

// TestSystemOneChoiceCriteriaMapping pins the typed option wire for
// both presets: criteria map each id to its description (the id when
// there is none), and the reply matches on id, so an id with a colon
// round-trips exactly.
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
	for _, build := range []func(SystemOneConfig) *SystemOne{NewLaya, NewJev} {
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
			stub := newLayaStub(t, `{"type":"choice","choice":`+strconv.Quote(tc.reply)+`}`)
			s := build(SystemOneConfig{Name: "d", BaseURL: stub.srv.URL})
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
}
