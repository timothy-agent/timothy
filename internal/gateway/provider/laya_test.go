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
	got    systemoneRequest
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
	l := NewLaya(SystemOneConfig{Name: "laya", BaseURL: "http://laya:8000"})
	if caps := l.Capabilities(); len(caps) != 1 || caps[0] != CapDecide {
		t.Fatalf("capabilities = %v, want decide only", caps)
	}
	if _, err := l.Stream(t.Context(), CompletionRequest{Model: "typed-decisions"}); err == nil {
		t.Fatal("Stream succeeded on a decision model")
	}
}

// TestLayaDecideChoice pins the wire mapping: a choice becomes
// a Laya choice question whose criteria are the options, the question
// is the state, the chain entry's model is forwarded, and the reply's
// probabilities come back as scores.
func TestLayaDecideChoice(t *testing.T) {
	t.Parallel()
	stub := newLayaStub(t, `{"type":"choice","choice":"research","probabilities":{"coding":0.1,"research":0.85,"general":0.05},"confidence":0.8}`)
	l := NewLaya(SystemOneConfig{Name: "laya", BaseURL: stub.srv.URL, APIKey: "k"})

	res, usage, err := l.Decide(t.Context(), "typed-decisions", DecideRequest{
		Kind:     DecideChoice,
		Question: "Which kind of mission is this?",
		Options:  optIDs("coding", "research", "general"),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Choice != "research" || res.Scores["research"] != 0.85 {
		t.Fatalf("res = %+v", res)
	}
	if usage == nil || usage.InputTokens != 83 {
		t.Fatalf("usage = %+v", usage)
	}
	if stub.auth != "Bearer k" {
		t.Fatalf("auth = %q", stub.auth)
	}
	if stub.got.Model != "typed-decisions" || stub.got.State != "Which kind of mission is this?" {
		t.Fatalf("request = %+v", stub.got)
	}
	q := stub.got.Questions["q"]
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

func TestLayaDecideChoiceWithStateAndDescriptions(t *testing.T) {
	t.Parallel()
	stub := newLayaStub(t, `{"type":"choice","choice":"coding","probabilities":{"coding":0.9,"general":0.1}}`)
	l := NewLaya(SystemOneConfig{Name: "laya", BaseURL: stub.srv.URL})

	res, _, err := l.Decide(t.Context(), "typed-decisions", DecideRequest{
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

func TestLayaDecideChoiceOutsideOptionsIsAnError(t *testing.T) {
	t.Parallel()
	stub := newLayaStub(t, `{"type":"choice","choice":"other","probabilities":{"other":1}}`)
	l := NewLaya(SystemOneConfig{Name: "laya", BaseURL: stub.srv.URL})
	_, usage, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideChoice, Question: "q", Options: optIDs("a", "b")})
	if err == nil || !strings.Contains(err.Error(), "not an allowed option") {
		t.Fatalf("err = %v, want 'not an allowed option'", err)
	}
	if usage == nil {
		t.Fatal("usage dropped on a bad answer")
	}
}

// TestLayaDecideYesNo pins that yesno questions ride a noul question,
// thresholding the probability at 0.5.
func TestLayaDecideYesNo(t *testing.T) {
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
			stub := newLayaStub(t, `{"type":"noul","noul":`+tc.noul+`,"confidence":0.9}`)
			l := NewLaya(SystemOneConfig{Name: "laya", BaseURL: stub.srv.URL})
			res, _, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideYesNo, Question: "Does the user ask for a refund?", Options: optIDs("yes", "no")})
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			q := stub.got.Questions["q"]
			if q.Type != "noul" || q.Criteria != nil || stub.got.State != "Does the user ask for a refund?" || stub.got.Model != "" {
				t.Fatalf("request = %+v", stub.got)
			}
			if res.Choice != tc.wantChoice || res.Scores["yes"]+res.Scores["no"] != 1 {
				t.Fatalf("res = %+v", res)
			}
		})
	}
}

func TestLayaDecideMissingAnswerIsAnError(t *testing.T) {
	t.Parallel()
	stub := newLayaStub(t, `{"type":"choice","choice":"a"}`)
	l := NewLaya(SystemOneConfig{Name: "laya", BaseURL: stub.srv.URL})
	if _, _, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideYesNo, Question: "q", Options: optIDs("yes", "no")}); err == nil || !strings.Contains(err.Error(), "no noul probability") {
		t.Fatalf("err = %v", err)
	}
}

func TestLayaBaseURLNormalization(t *testing.T) {
	t.Parallel()
	paths := make(chan string, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"q":{"type":"choice","choice":"coding"}},"usage":{"input_tokens":10}}`))
	}))
	defer srv.Close()

	for _, u := range []string{srv.URL, srv.URL + "/v1", srv.URL + "/v1/systemone"} {
		l := NewLaya(SystemOneConfig{Name: "laya", BaseURL: u})
		_, _, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideChoice, Question: "q", Options: optIDs("coding", "research")})
		if err != nil {
			t.Fatalf("Decide with BaseURL %q: %v", u, err)
		}
		if p := <-paths; p != "/v1/systemone" {
			t.Fatalf("BaseURL %q hit path %q, want /v1/systemone", u, p)
		}
	}
}

func TestLayaDecideHTTPErrorsSurface(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnprocessableEntity, http.StatusServiceUnavailable} {
		stub := newLayaStub(t, `{}`)
		stub.status = status
		l := NewLaya(SystemOneConfig{Name: "laya", BaseURL: stub.srv.URL})
		if _, _, err := l.Decide(t.Context(), "", DecideRequest{Kind: DecideYesNo, Question: "q", Options: optIDs("yes", "no")}); err == nil {
			t.Fatalf("status %d: no error", status)
		}
	}
}

func TestLayaListModels(t *testing.T) {
	t.Parallel()
	stub := newLayaStub(t, `{}`)
	l := NewLaya(SystemOneConfig{Name: "laya", BaseURL: stub.srv.URL})
	models, err := l.ListModels(t.Context())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "typed-decisions" {
		t.Fatalf("models = %+v", models)
	}
}
