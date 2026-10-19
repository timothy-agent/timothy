package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jevStub answers /v1/systemone with the given answer object and
// records the last request payload it received.
type jevStub struct {
	srv    *httptest.Server
	got    systemoneRequest
	auth   string
	status int
	answer string
}

func newJevStub(t *testing.T, answer string) *jevStub {
	t.Helper()
	s := &jevStub{answer: answer, status: http.StatusOK}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/systemone" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&s.got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if s.status != http.StatusOK {
			http.Error(w, s.answer, s.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-latest","answers":{"q":` + s.answer + `},"usage":{"input_tokens":83,"output_tokens":0}}`))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func TestJevCapabilitiesAndStream(t *testing.T) {
	t.Parallel()
	j := NewJev(SystemOneConfig{Name: "jev"})
	caps := j.Capabilities()
	if len(caps) != 1 || caps[0] != CapDecide {
		t.Fatalf("capabilities = %v, want decide only", caps)
	}
	if _, err := j.Stream(t.Context(), CompletionRequest{}); err == nil {
		t.Fatal("stream must error on a decision model")
	}
}

func TestJevDecideChoice(t *testing.T) {
	t.Parallel()
	stub := newJevStub(t, `{"type":"choice","choice":"research","probabilities":{"coding":0.1,"research":0.85,"general":0.05},"confidence":0.8}`)
	j := NewJev(SystemOneConfig{Name: "jev", BaseURL: stub.srv.URL, APIKey: "jv_live_testkey"}) // #nosec G101

	res, usage, err := j.Decide(t.Context(), "jev-latest", DecideRequest{
		Kind:     DecideChoice,
		Question: "Which kind of mission is this?",
		Options:  optIDs("coding", "research", "general"),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Choice != "research" {
		t.Fatalf("res.Choice = %q, want research", res.Choice)
	}
	if res.Scores["research"] != 0.85 || res.Scores["coding"] != 0.1 {
		t.Fatalf("res.Scores = %v", res.Scores)
	}
	if usage == nil || usage.InputTokens != 83 || usage.OutputTokens != 0 {
		t.Fatalf("usage = %+v", usage)
	}
	if stub.got.Model != "jev-latest" || stub.got.State != "Which kind of mission is this?" {
		t.Fatalf("request = %+v", stub.got)
	}
	if stub.auth != "Bearer jv_live_testkey" {
		t.Fatalf("auth = %q, want Bearer jv_live_testkey", stub.auth)
	}
	q, ok := stub.got.Questions["q"]
	if !ok {
		t.Fatalf("missing question q in %+v", stub.got)
	}
	if q.Type != "choice" || q.Instructions != "Which kind of mission is this?" {
		t.Fatalf("question = %+v", q)
	}
	crit, ok := q.Criteria.(map[string]any)
	if !ok {
		t.Fatalf("criteria = %T, want map", q.Criteria)
	}
	if len(crit) != 3 || crit["coding"] != "coding" || crit["research"] != "research" {
		t.Fatalf("criteria = %v", crit)
	}
}

func TestJevDecideChoiceWithStateAndDescriptions(t *testing.T) {
	t.Parallel()
	stub := newJevStub(t, `{"type":"choice","choice":"coding","probabilities":{"coding":0.9,"general":0.1}}`)
	j := NewJev(SystemOneConfig{Name: "jev", BaseURL: stub.srv.URL})

	res, _, err := j.Decide(t.Context(), "typed-decisions", DecideRequest{
		Kind:     DecideChoice,
		Question: "Classify this goal.",
		State:    "Write a go compiler",
		Options:  []DecideOption{{ID: "coding", Description: "writes code"}, {ID: "general", Description: "documents"}},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Choice != "coding" {
		t.Fatalf("res.Choice = %q, want coding", res.Choice)
	}
	if stub.got.State != "Write a go compiler" {
		t.Fatalf("got.State = %q, want state separated from question", stub.got.State)
	}
	q := stub.got.Questions["q"]
	if q.Instructions != "Classify this goal." {
		t.Fatalf("q.Instructions = %q, want instructions separated from state", q.Instructions)
	}
	crit, ok := q.Criteria.(map[string]any)
	if !ok || crit["coding"] != "writes code" || crit["general"] != "documents" {
		t.Fatalf("crit = %+v, want each id mapped to its description", crit)
	}
}

func TestJevDecideChoiceOutsideOptionsIsAnError(t *testing.T) {
	t.Parallel()
	stub := newJevStub(t, `{"type":"choice","choice":"hallucinated","probabilities":{"coding":0.1,"research":0.85,"general":0.05},"confidence":0.8}`)
	j := NewJev(SystemOneConfig{Name: "jev", BaseURL: stub.srv.URL})

	_, usage, err := j.Decide(t.Context(), "jev-latest", DecideRequest{
		Kind:     DecideChoice,
		Question: "Which kind of mission is this?",
		Options:  optIDs("coding", "research", "general"),
	})
	if err == nil || !strings.Contains(err.Error(), "not an allowed option") {
		t.Fatalf("err = %v, want 'not an allowed option'", err)
	}
	if usage == nil {
		t.Fatal("usage dropped on a bad answer")
	}
}

func TestJevDecideYesNo(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		noul       string
		wantChoice string
	}{
		{"yes", "0.91", "yes"},
		{"no", "0.2", "no"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := newJevStub(t, `{"type":"noul","noul":`+tc.noul+`,"confidence":0.9}`)
			j := NewJev(SystemOneConfig{Name: "jev", BaseURL: stub.srv.URL})
			res, _, err := j.Decide(t.Context(), "", DecideRequest{Kind: DecideYesNo, Question: "Does the user ask for a refund?", Options: optIDs("yes", "no")})
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			q := stub.got.Questions["q"]
			if q.Type != "noul" || q.Criteria != nil || stub.got.State != "Does the user ask for a refund?" || stub.got.Model != "jev-latest" {
				t.Fatalf("request = %+v", stub.got)
			}
			if res.Choice != tc.wantChoice || res.Scores["yes"]+res.Scores["no"] != 1 {
				t.Fatalf("res = %+v", res)
			}
		})
	}
}

func TestJevDecideMissingAnswerIsAnError(t *testing.T) {
	t.Parallel()
	stub := newJevStub(t, `{"type":"choice","choice":"a"}`)
	j := NewJev(SystemOneConfig{Name: "jev", BaseURL: stub.srv.URL})
	if _, _, err := j.Decide(t.Context(), "", DecideRequest{Kind: DecideYesNo, Question: "q", Options: optIDs("yes", "no")}); err == nil || !strings.Contains(err.Error(), "no noul probability") {
		t.Fatalf("err = %v, want 'no noul probability'", err)
	}
}

// TestJevDecideHTTPErrorWithoutRequestBody verifies that on 4xx/5xx from Jev,
// the driver returns an error containing the status code without echoing the
// input request question/state.
func TestJevDecideHTTPErrorWithoutRequestBody(t *testing.T) {
	t.Parallel()
	sensitiveQuestion := "SUPER_SECRET_USER_INSTRUCTION_THAT_MUST_NOT_LEAK"
	stub := newJevStub(t, `{"error":"invalid_api_key","message":"unauthorized"}`)
	stub.status = http.StatusUnauthorized

	j := NewJev(SystemOneConfig{Name: "jev", BaseURL: stub.srv.URL})
	_, _, err := j.Decide(t.Context(), "", DecideRequest{Kind: DecideYesNo, Question: sensitiveQuestion})
	if err == nil {
		t.Fatal("Decide must error on 401")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "401") {
		t.Fatalf("err = %q, want '401'", errMsg)
	}
	if strings.Contains(errMsg, sensitiveQuestion) {
		t.Fatalf("err = %q leaked request body / question!", errMsg)
	}
}

func TestJevURLVariations(t *testing.T) {
	t.Parallel()
	stub := newJevStub(t, `{"type":"choice","choice":"yes","probabilities":{"yes":1}}`)
	for _, u := range []string{
		stub.srv.URL,
		stub.srv.URL + "/",
		stub.srv.URL + "/v1",
		stub.srv.URL + "/v1/systemone",
	} {
		j := NewJev(SystemOneConfig{Name: "jev", BaseURL: u})
		res, _, err := j.Decide(t.Context(), "", DecideRequest{Kind: DecideChoice, Question: "q", Options: optIDs("yes", "no")})
		if err != nil {
			t.Fatalf("url %s: Decide: %v", u, err)
		}
		if res.Choice != "yes" {
			t.Fatalf("url %s: res = %+v", u, res)
		}
	}
}

func TestJevListModels(t *testing.T) {
	t.Parallel()

	t.Run("dynamic /v1/models response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"jev-latest","description":"Default fast decision model"},{"name":"jev-preview","description":"Experimental preview model"}]}`))
		}))
		defer srv.Close()

		j := NewJev(SystemOneConfig{Name: "jev", BaseURL: srv.URL})
		models, err := j.ListModels(t.Context())
		if err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if len(models) != 2 || models[0].ID != "jev-latest" || models[1].ID != "jev-preview" {
			t.Fatalf("models = %+v, want jev-latest and jev-preview", models)
		}
		if models[0].DisplayName != "Default fast decision model" {
			t.Fatalf("models[0].DisplayName = %q", models[0].DisplayName)
		}
	})

	t.Run("error when endpoint unavailable", func(t *testing.T) {
		t.Parallel()
		j := NewJev(SystemOneConfig{Name: "jev", BaseURL: "http://127.0.0.1:1"})
		models, err := j.ListModels(t.Context())
		if err == nil || !strings.Contains(err.Error(), "jev: list models") {
			t.Fatalf("ListModels = %+v, %v; want a jev list models error", models, err)
		}
		if models != nil {
			t.Fatalf("models = %+v, want none on error", models)
		}
	})

	t.Run("error on HTTP failure", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"invalid_api_key"}`, http.StatusUnauthorized)
		}))
		defer srv.Close()
		j := NewJev(SystemOneConfig{Name: "jev", BaseURL: srv.URL})
		if models, err := j.ListModels(t.Context()); err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("ListModels = %+v, %v; want a 401 error", models, err)
		}
	})

	t.Run("empty list when the endpoint lists nothing", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"models":[]}`))
		}))
		defer srv.Close()
		j := NewJev(SystemOneConfig{Name: "jev", BaseURL: srv.URL})
		models, err := j.ListModels(t.Context())
		if err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if len(models) != 0 {
			t.Fatalf("models = %+v, want empty", models)
		}
	})
}
