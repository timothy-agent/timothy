package provider

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jevStub answers /v1/systemone with the given answer object and
// records the last request payload it received.
type jevStub struct {
	srv    *httptest.Server
	got    jevRequest
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
	j := NewJev(JevConfig{Name: "jev"})
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
	j := NewJev(JevConfig{Name: "jev", BaseURL: stub.srv.URL, APIKey: "jv_live_testkey"}) // #nosec G101

	res, usage, err := j.Decide(t.Context(), "jev-latest", DecideRequest{
		Kind:     DecideChoice,
		Question: "Which kind of mission is this?",
		Options:  []string{"coding", "research", "general"},
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

func TestJevDecideChoiceOutsideOptionsIsAnError(t *testing.T) {
	t.Parallel()
	stub := newJevStub(t, `{"type":"choice","choice":"hallucinated","probabilities":{"coding":0.1,"research":0.85,"general":0.05},"confidence":0.8}`)
	j := NewJev(JevConfig{Name: "jev", BaseURL: stub.srv.URL})

	_, _, err := j.Decide(t.Context(), "jev-latest", DecideRequest{
		Kind:     DecideChoice,
		Question: "Which kind of mission is this?",
		Options:  []string{"coding", "research", "general"},
	})
	if err == nil || !strings.Contains(err.Error(), "not an allowed option") {
		t.Fatalf("err = %v, want 'not an allowed option'", err)
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
			j := NewJev(JevConfig{Name: "jev", BaseURL: stub.srv.URL})
			res, _, err := j.Decide(t.Context(), "", DecideRequest{Kind: DecideYesNo, Question: "Does the user ask for a refund?", Options: []string{"yes", "no"}})
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

func TestJevDecideScore(t *testing.T) {
	t.Parallel()

	t.Run("default criteria 4 levels", func(t *testing.T) {
		t.Parallel()
		stub := newJevStub(t, `{"type":"score","score":1.5,"probabilities":{"0":0.1,"1":0.3,"2":0.4,"3":0.2}}`)
		j := NewJev(JevConfig{Name: "jev", BaseURL: stub.srv.URL})
		res, usage, err := j.Decide(t.Context(), "", DecideRequest{Kind: DecideScore, Question: "How relevant is this document?"})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if res.Score == nil || math.Abs(*res.Score-0.5) > 1e-4 {
			t.Fatalf("res.Score = %v, want 0.5", res.Score)
		}
		if res.Choice != "" {
			t.Fatalf("res.Choice = %q, want empty", res.Choice)
		}
		if usage == nil || usage.InputTokens != 83 {
			t.Fatalf("usage = %+v", usage)
		}
		q := stub.got.Questions["q"]
		if q.Type != "score" {
			t.Fatalf("q.Type = %q, want score", q.Type)
		}
		crit, ok := q.Criteria.([]any)
		if !ok || len(crit) != 4 {
			t.Fatalf("criteria = %v, want 4 levels", q.Criteria)
		}
	})

	t.Run("custom 3 levels 0 to 2 scale", func(t *testing.T) {
		t.Parallel()
		stub := newJevStub(t, `{"type":"score","score":1.84,"probabilities":{"0":0.05,"1":0.11,"2":0.84}}`)
		j := NewJev(JevConfig{Name: "jev", BaseURL: stub.srv.URL})
		res, _, err := j.Decide(t.Context(), "", DecideRequest{
			Kind:     DecideScore,
			Question: "How urgent is this incident?",
			Options:  []string{"not urgent", "soon", "blocking"},
		})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if res.Score == nil || math.Abs(*res.Score-0.92) > 1e-4 {
			t.Fatalf("res.Score = %v, want 0.92", res.Score)
		}
		q := stub.got.Questions["q"]
		if q.Type != "score" {
			t.Fatalf("q.Type = %q, want score", q.Type)
		}
		crit, ok := q.Criteria.([]any)
		if !ok || len(crit) != 3 {
			t.Fatalf("criteria = %v, want 3 levels", q.Criteria)
		}
	})
}

func TestJevDecideScoreOutsideRangeIsAnError(t *testing.T) {
	t.Parallel()
	for _, badScore := range []string{"-0.1", "3.5"} {
		stub := newJevStub(t, `{"type":"score","score":`+badScore+`}`)
		j := NewJev(JevConfig{Name: "jev", BaseURL: stub.srv.URL})
		_, _, err := j.Decide(t.Context(), "", DecideRequest{Kind: DecideScore, Question: "score"})
		if err == nil || !strings.Contains(err.Error(), "outside [0, 3]") {
			t.Fatalf("score %s: err = %v, want 'outside [0, 3]'", badScore, err)
		}
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

	j := NewJev(JevConfig{Name: "jev", BaseURL: stub.srv.URL})
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
		j := NewJev(JevConfig{Name: "jev", BaseURL: u})
		res, _, err := j.Decide(t.Context(), "", DecideRequest{Kind: DecideChoice, Question: "q", Options: []string{"yes", "no"}})
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
	j := NewJev(JevConfig{Name: "jev"})
	models, err := j.ListModels(t.Context())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) < 1 || models[0].ID != "jev-latest" {
		t.Fatalf("models = %+v, want jev-latest", models)
	}
}
