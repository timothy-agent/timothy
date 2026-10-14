package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLayaCapabilities(t *testing.T) {
	t.Parallel()
	l := NewLaya(LayaConfig{Name: "laya-test", BaseURL: "http://localhost:8000"})
	if l.Name() != "laya-test" {
		t.Fatalf("Name() = %q, want laya-test", l.Name())
	}
	if l.Kind() != KindAPI {
		t.Fatalf("Kind() = %v, want KindAPI", l.Kind())
	}
	caps := l.Capabilities()
	if len(caps) != 1 || caps[0] != CapDecide {
		t.Fatalf("Capabilities() = %v, want [CapDecide]", caps)
	}
	if _, err := l.Stream(t.Context(), CompletionRequest{}); err == nil {
		t.Fatal("Stream() did not return error")
	}
}

func TestLayaDecideChoice(t *testing.T) {
	t.Parallel()
	var (
		gotPath    string
		gotBody    layaRequest
		gotHeaders http.Header
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "convaiinnovations/laya-typed-decisions",
			"answers": {
				"q": {
					"type": "choice",
					"choice": "coding",
					"confidence": 0.88,
					"probabilities": {
						"coding": 0.88,
						"research": 0.12
					}
				}
			},
			"usage": {
				"input_tokens": 42
			}
		}`))
	}))
	defer srv.Close()

	l := NewLaya(LayaConfig{
		Name:    "laya",
		BaseURL: srv.URL,
		Headers: map[string]string{"X-Test": "123"},
		Timeout: 5 * time.Second,
	})

	req := DecideRequest{
		Kind:     DecideChoice,
		Question: "Which mission kind?",
		Options:  []string{"coding", "research"},
	}

	res, usage, err := l.Decide(t.Context(), "convaiinnovations/laya-typed-decisions", req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}

	if gotPath != "/v1/systemone" {
		t.Fatalf("Path = %q, want /v1/systemone", gotPath)
	}
	if gotHeaders.Get("X-Test") != "123" {
		t.Fatalf("Header X-Test = %q, want 123", gotHeaders.Get("X-Test"))
	}
	if gotBody.Questions["q"].Type != "choice" {
		t.Fatalf("question type = %q, want choice", gotBody.Questions["q"].Type)
	}
	if gotBody.State != "Which mission kind?" {
		t.Fatalf("state = %q, want 'Which mission kind?'", gotBody.State)
	}

	if res.Choice != "coding" {
		t.Fatalf("res.Choice = %q, want coding", res.Choice)
	}
	if res.Scores["coding"] != 0.88 || res.Scores["research"] != 0.12 {
		t.Fatalf("res.Scores = %v, want probabilities map", res.Scores)
	}
	if usage == nil || usage.InputTokens != 42 {
		t.Fatalf("usage = %+v, want InputTokens 42", usage)
	}
}

func TestLayaDecideYesNo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		noul       float64
		wantChoice string
	}{
		{"high probability is yes", 0.92, "yes"},
		{"low probability is no", 0.08, "no"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				resp := map[string]any{
					"model": "laya",
					"answers": map[string]any{
						"q": map[string]any{
							"type": "noul",
							"noul": tc.noul,
						},
					},
					"usage": map[string]int{"input_tokens": 10},
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer srv.Close()

			l := NewLaya(LayaConfig{Name: "laya", BaseURL: srv.URL})
			res, usage, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideYesNo, Question: "Is it complete?"})
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if res.Choice != tc.wantChoice {
				t.Fatalf("Choice = %q, want %q", res.Choice, tc.wantChoice)
			}
			if res.Scores["yes"] != tc.noul {
				t.Fatalf("Scores[yes] = %f, want %f", res.Scores["yes"], tc.noul)
			}
			if usage == nil || usage.InputTokens != 10 {
				t.Fatalf("usage = %+v, want 10 input tokens", usage)
			}
		})
	}
}

func TestLayaDecideScore(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "laya",
			"answers": {
				"q": {
					"type": "score",
					"score": 0.73,
					"probabilities": {"0": 0.27, "1": 0.73}
				}
			}
		}`))
	}))
	defer srv.Close()

	l := NewLaya(LayaConfig{Name: "laya", BaseURL: srv.URL})
	res, _, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideScore, Question: "Urgency score"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Score == nil || *res.Score != 0.73 {
		t.Fatalf("Score = %v, want 0.73", res.Score)
	}
	if res.Scores["1"] != 0.73 {
		t.Fatalf("Scores = %v, want probabilities", res.Scores)
	}
}

func TestLayaBaseURLNormalization(t *testing.T) {
	t.Parallel()
	paths := make(chan string, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"q":{"type":"choice","choice":"coding"}}}`))
	}))
	defer srv.Close()

	for _, u := range []string{srv.URL, srv.URL + "/v1", srv.URL + "/v1/systemone"} {
		l := NewLaya(LayaConfig{Name: "laya", BaseURL: u})
		_, _, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideChoice, Question: "q", Options: []string{"coding", "research"}})
		if err != nil {
			t.Fatalf("Decide with BaseURL %q: %v", u, err)
		}
		if p := <-paths; p != "/v1/systemone" {
			t.Fatalf("BaseURL %q hit path %q, want /v1/systemone", u, p)
		}
	}
}

func TestLayaServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"detail": "model loading"}`))
	}))
	defer srv.Close()

	l := NewLaya(LayaConfig{Name: "laya", BaseURL: srv.URL})
	_, _, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideChoice, Question: "q", Options: []string{"a", "b"}})
	if err == nil {
		t.Fatal("expected error on 503, got nil")
	}
}
