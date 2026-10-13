package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// layaStub answers /v1/systemone with the given answer object and
// records the last request body and headers.
type layaStub struct {
	srv    *httptest.Server
	got    layaRequest
	auth   string
	answer string
	status int
}

func newLayaStub(t *testing.T, answer string) *layaStub {
	t.Helper()
	s := &layaStub{answer: answer, status: http.StatusOK}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok","loaded":["typed-decisions"]}`))
			return
		}
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&s.got); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(`{"model":"laya-rl-agent","answers":{"q":` + s.answer + `},"usage":{"input_tokens":83,"output_tokens":0,"state_tokens":12},"routing":{"model":"typed-decisions"}}`))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func TestLayaCapabilitiesAndStream(t *testing.T) {
	t.Parallel()
	l := NewLaya(LayaConfig{Name: "laya", BaseURL: "http://laya:8000"})
	if caps := l.Capabilities(); len(caps) != 1 || caps[0] != CapDecide {
		t.Fatalf("capabilities = %v, want decide only", caps)
	}
	if _, err := l.Stream(t.Context(), CompletionRequest{Model: "typed-decisions"}); err == nil {
		t.Fatal("Stream succeeded on a decision model")
	}
}

// TestLayaDecideChoice pins the wire mapping (D-164): a choice becomes
// a Laya choice question whose criteria are the options described,
// the context is the state, the chain entry's model is forwarded, and
// the reply's probabilities come back as scores.
func TestLayaDecideChoice(t *testing.T) {
	t.Parallel()
	stub := newLayaStub(t, `{"type":"choice","choice":"research","probabilities":{"coding":0.1,"research":0.85,"general":0.05},"confidence":0.8}`)
	l := NewLaya(LayaConfig{Name: "laya", BaseURL: stub.srv.URL, APIKey: "k"})

	res, usage, err := l.Decide(t.Context(), "typed-decisions", DecideRequest{
		Kind: DecideChoice, Question: "Which kind of mission is this?", Context: "Survey the top 5 vector databases",
		Options:      []string{"coding", "research", "general"},
		Descriptions: map[string]string{"coding": "writes or changes code", "research": "gathers and compares information"},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Choice != "research" || res.Scores["research"] != 0.85 || res.Score != nil {
		t.Fatalf("res = %+v", res)
	}
	if usage == nil || usage.InputTokens != 83 {
		t.Fatalf("usage = %+v", usage)
	}
	if stub.auth != "Bearer k" {
		t.Fatalf("auth = %q", stub.auth)
	}
	if stub.got.Model != "typed-decisions" || stub.got.State != "Survey the top 5 vector databases" {
		t.Fatalf("request = %+v", stub.got)
	}
	q := stub.got.Questions["q"]
	if q.Type != "choice" || q.Instructions != "Which kind of mission is this?" {
		t.Fatalf("question = %+v", q)
	}
	if q.Criteria["coding"] != "writes or changes code" || q.Criteria["general"] != "general" || len(q.Criteria) != 3 {
		t.Fatalf("criteria = %v, want descriptions with the bare option as fallback", q.Criteria)
	}
}

func TestLayaDecideChoiceOutsideOptionsIsAnError(t *testing.T) {
	t.Parallel()
	stub := newLayaStub(t, `{"type":"choice","choice":"other","probabilities":{"other":1}}`)
	l := NewLaya(LayaConfig{Name: "laya", BaseURL: stub.srv.URL})
	_, usage, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideChoice, Question: "q", Options: []string{"a", "b"}})
	if err == nil || !strings.Contains(err.Error(), "not an allowed option") {
		t.Fatalf("err = %v", err)
	}
	if usage == nil {
		t.Fatal("usage dropped on a bad answer")
	}
}

// TestLayaDecideYesNoAndScore: both kinds ride a noul question with
// the question as its own state when no context is given; yesno
// thresholds the probability, score returns it.
func TestLayaDecideYesNoAndScore(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		kind       string
		noul       string
		wantChoice string
		wantScore  float64
	}{
		{"yes", DecideYesNo, "0.91", "yes", 0},
		{"no", DecideYesNo, "0.2", "no", 0},
		{"score", DecideScore, "0.37", "", 0.37},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := newLayaStub(t, `{"type":"noul","noul":`+tc.noul+`,"confidence":0.9}`)
			l := NewLaya(LayaConfig{Name: "laya", BaseURL: stub.srv.URL})
			res, _, err := l.Decide(t.Context(), "", DecideRequest{Kind: tc.kind, Question: "Does the user ask for a refund?", Options: []string{"yes", "no"}})
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			q := stub.got.Questions["q"]
			if q.Type != "noul" || q.Criteria != nil || stub.got.State != "Does the user ask for a refund?" || stub.got.Model != "" {
				t.Fatalf("request = %+v", stub.got)
			}
			if stub.auth != "" {
				t.Fatalf("auth = %q sent without a key", stub.auth)
			}
			if tc.kind == DecideYesNo {
				if res.Choice != tc.wantChoice || res.Scores["yes"]+res.Scores["no"] != 1 {
					t.Fatalf("res = %+v", res)
				}
				return
			}
			if res.Score == nil || *res.Score != tc.wantScore || res.Choice != "" {
				t.Fatalf("res = %+v", res)
			}
		})
	}
}

func TestLayaDecideMissingAnswerIsAnError(t *testing.T) {
	t.Parallel()
	stub := newLayaStub(t, `{"type":"choice","choice":"a"}`)
	l := NewLaya(LayaConfig{Name: "laya", BaseURL: stub.srv.URL})
	if _, _, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideYesNo, Question: "q", Options: []string{"yes", "no"}}); err == nil || !strings.Contains(err.Error(), "no noul probability") {
		t.Fatalf("err = %v", err)
	}
}

// TestLayaDecideHTTPErrorsSurface: a 4xx is permanent and a 503 is
// retried, both end as errors the chain can fail over on.
func TestLayaDecideHTTPErrorsSurface(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnprocessableEntity, http.StatusServiceUnavailable} {
		stub := newLayaStub(t, `{}`)
		stub.status = status
		l := NewLaya(LayaConfig{Name: "laya", BaseURL: stub.srv.URL})
		if _, _, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideYesNo, Question: "q", Options: []string{"yes", "no"}}); err == nil {
			t.Fatalf("status %d: no error", status)
		}
	}
}

func TestLayaListModels(t *testing.T) {
	t.Parallel()
	stub := newLayaStub(t, `{}`)
	l := NewLaya(LayaConfig{Name: "laya", BaseURL: stub.srv.URL})
	models, err := l.ListModels(t.Context())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "typed-decisions" {
		t.Fatalf("models = %+v", models)
	}
}
