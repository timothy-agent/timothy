package provider

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
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
		_, _ = w.Write([]byte(`{"answers":{"q":{"type":"choice","choice":"a","noul":0.7}},"usage":{"input_tokens":1}}`))
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name string
		req  DecideRequest
	}{
		{"choice", DecideRequest{Kind: DecideChoice, Question: "Pick one.", State: "some state", Options: []string{"a: first", "b"}}},
		{"yesno", DecideRequest{Kind: DecideYesNo, Question: "Is it?", Options: []string{"yes", "no"}}},
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
