package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/gateway/decide"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

func loadTestDataset(t *testing.T) Dataset {
	t.Helper()
	ds, err := loadDataset("dataset.json")
	if err != nil {
		t.Fatalf("load dataset: %v", err)
	}
	return ds
}

// TestDatasetShape checks the dataset is large enough to gate a swap
// and every expected answer is one the site can produce.
func TestDatasetShape(t *testing.T) {
	t.Parallel()
	ds := loadTestDataset(t)
	const minPerSite = 30
	sources := map[string]bool{"mission_store": true, "session_shape": true, "kb_store": true, "synthetic": true}

	if len(ds.Missions) < minPerSite || len(ds.Agents) < minPerSite || len(ds.KBFiling) < minPerSite {
		t.Fatalf("samples: missions %d, agents %d, kb_filing %d; want at least %d each", len(ds.Missions), len(ds.Agents), len(ds.KBFiling), minPerSite)
	}

	seen := map[string]bool{}
	unique := func(site, key string) {
		if seen[site+"\x00"+key] {
			t.Errorf("%s: duplicate sample %q", site, key)
		}
		seen[site+"\x00"+key] = true
	}
	for _, m := range ds.Missions {
		unique("missions", m.Goal)
		if !sources[m.Source] {
			t.Errorf("mission %q: unknown source %q", m.Goal, m.Source)
		}
		if m.WantKind != "coding" && m.WantKind != "general" {
			t.Errorf("mission %q: want_kind %q", m.Goal, m.WantKind)
		}
		if m.WantKind == "coding" && m.WantLight {
			t.Errorf("mission %q: a coding mission is never light", m.Goal)
		}
	}
	for _, a := range ds.Agents {
		unique("agents", a.Message)
		if !sources[a.Source] {
			t.Errorf("agent sample %q: unknown source %q", a.Message, a.Source)
		}
		if len(a.Candidates) < 2 {
			t.Errorf("agent sample %q: dispatch skips decide with fewer than two candidates", a.Message)
		}
		found := false
		for _, c := range a.Candidates {
			found = found || c.ID == a.WantAgent
		}
		if !found {
			t.Errorf("agent sample %q: want_agent %q is not a candidate id", a.Message, a.WantAgent)
		}
	}
	for _, k := range ds.KBFiling {
		unique("kb_filing", k.Title)
		if !sources[k.Source] {
			t.Errorf("kb sample %q: unknown source %q", k.Title, k.Source)
		}
		if matchKB(k.WantChoice, k.Collections) != k.WantChoice {
			t.Errorf("kb sample %q: want_choice %q is neither a collection id nor NEW", k.Title, k.WantChoice)
		}
	}
}

// TestDatasetNoPersonalData guards against real addresses, links and
// secrets leaking in from session data.
func TestDatasetNoPersonalData(t *testing.T) {
	t.Parallel()
	ds := loadTestDataset(t)
	var texts []string
	for _, m := range ds.Missions {
		texts = append(texts, m.Goal)
	}
	for _, a := range ds.Agents {
		texts = append(texts, a.Message)
	}
	for _, k := range ds.KBFiling {
		texts = append(texts, k.Title, k.Text)
	}
	banned := []*regexp.Regexp{
		regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.]+`),                // email
		regexp.MustCompile(`(?i)https?://|www\.`),                    // link
		regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`), // IPv4
		regexp.MustCompile(`(?i)\b(api[_-]?key|password|secret|token)\s*[:=]`),
	}
	for _, s := range texts {
		for _, re := range banned {
			if m := re.FindString(s); m != "" {
				t.Errorf("dataset text contains %q (%s)", m, re)
			}
		}
	}
}

// oracle answers the eval's gateway calls from the dataset. right
// picks the expected answer; otherwise a different allowed one.
type oracle struct {
	t      *testing.T
	right  bool
	native bool
	kind   map[string]string // goal -> kind
	light  map[string]bool   // goal -> light
	agent  map[string]AgentSample
	kb     map[string]KBFilingSample // kbState -> sample
	calls  atomic.Int64
}

func newOracle(t *testing.T, ds Dataset, right, native bool) *oracle {
	o := &oracle{t: t, right: right, native: native, kind: map[string]string{}, light: map[string]bool{}, agent: map[string]AgentSample{}, kb: map[string]KBFilingSample{}}
	for _, m := range ds.Missions {
		o.kind[m.Goal] = m.WantKind
		o.light[m.Goal] = m.WantLight
	}
	for _, a := range ds.Agents {
		o.agent[a.Message] = a
	}
	for _, k := range ds.KBFiling {
		o.kb[kbState(k)] = k
	}
	return o
}

// wantID is the option id the oracle should pick for a decide call.
func (o *oracle) wantID(purpose, state string) (string, bool) {
	switch purpose {
	case "kind":
		k, ok := o.kind[state]
		return k, ok
	case "light":
		l, ok := o.light[state]
		if l {
			return "yes", ok
		}
		return "no", ok
	case "dispatch":
		a, ok := o.agent[state]
		for _, c := range a.Candidates {
			if c.ID == a.WantAgent {
				return c.Name, ok
			}
		}
	case "kb_classify":
		k, ok := o.kb[state]
		return k.WantChoice, ok
	}
	return "", false
}

// replyStyles are the ways a chat model echoes an option; Parse must
// map each back to the option id.
var replyStyles = []func(o provider.DecideOption) string{
	func(o provider.DecideOption) string { return o.ID },
	func(o provider.DecideOption) string { return " \"" + strings.ToUpper(o.ID) + "\".\n" },
	func(o provider.DecideOption) string { return "**" + o.Label() + "**" },
	func(o provider.DecideOption) string { return "`" + o.ID + "`" },
}

func (o *oracle) handleDecide(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind     string                  `json:"kind"`
		Question string                  `json:"question"`
		State    string                  `json:"state"`
		Options  []provider.DecideOption `json:"options"`
		Purpose  string                  `json:"purpose"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req := provider.DecideRequest{Kind: body.Kind, Question: body.Question, State: body.State, Options: body.Options}
	sent := len(body.Options)
	if err := decide.Validate(&req); err != nil {
		o.t.Errorf("%s: Validate: %v", body.Purpose, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Kind == provider.DecideChoice && len(req.Options) != sent {
		o.t.Errorf("%s: Validate dropped %d of %d options for %q", body.Purpose, sent-len(req.Options), sent, truncateRunes(body.State, 60))
	}
	_, user := decide.Prompt(req)
	for _, opt := range req.Options {
		if !strings.Contains(user, "- "+opt.Label()+"\n") {
			o.t.Errorf("%s: prompt does not list option %q", body.Purpose, opt.Label())
		}
	}

	want, ok := o.wantID(body.Purpose, body.State)
	if !ok {
		o.t.Errorf("%s: no fixture for state %q", body.Purpose, truncateRunes(body.State, 60))
		http.Error(w, "no fixture", http.StatusBadGateway)
		return
	}
	var pick provider.DecideOption
	for _, opt := range req.Options {
		if strings.EqualFold(opt.ID, want) == o.right {
			pick = opt
			break
		}
	}
	n := o.calls.Add(1)
	reply := replyStyles[int(n)%len(replyStyles)](pick)
	res, err := decide.Parse(req, reply)
	if err != nil {
		o.t.Errorf("%s: Parse(%q): %v", body.Purpose, reply, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if res.Choice != pick.ID {
		o.t.Errorf("%s: Parse(%q) = %q, want %q", body.Purpose, reply, res.Choice, pick.ID)
	}
	writeJSONBody(w, map[string]any{
		"choice": res.Choice, "provider": "fixture", "model": "oracle", "native": o.native,
		"usage": map[string]int{"output_tokens": 1},
	})
}

// classifierReply is what a shipping classifier would answer.
func (o *oracle) classifierReply(purpose, prompt string) (string, bool) {
	after := func(marker string) string {
		_, rest, _ := strings.Cut(prompt, marker)
		return rest
	}
	switch purpose {
	case "mission_classify":
		goal := after("Goal: ")
		kind, ok := o.kind[goal]
		light := o.light[goal]
		if !o.right {
			kind, light = map[string]string{"coding": "general", "general": "coding"}[kind], false
		}
		if light {
			return kind + " light", ok
		}
		return kind + " full", ok
	case "dispatch":
		a, ok := o.agent[after("\nMessage: ")]
		for i, c := range a.Candidates {
			if (c.ID == a.WantAgent) == o.right {
				return fmt.Sprintf("%d.", i+1), ok
			}
		}
	case "kb_classify":
		title, _, _ := strings.Cut(after("Document title: "), "\n")
		for _, k := range o.kb {
			if k.Title != title {
				continue
			}
			for _, c := range k.Collections {
				if (c.ID == k.WantChoice) == o.right {
					return c.ID, true
				}
			}
			if k.WantChoice == kbNewOption && o.right {
				return "NEW: Topic | A broad topic", true
			}
			return kbNewOption + ": Other | Something else", true
		}
	}
	return "", false
}

func (o *oracle) handleStream(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Purpose  string `json:"purpose"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 1 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	reply, ok := o.classifierReply(body.Purpose, body.Messages[0].Content)
	if !ok {
		o.t.Errorf("stream %s: no fixture for prompt", body.Purpose)
	}
	ev, _ := json.Marshal(map[string]any{"type": "chunk", "text": reply})
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", ev)
}

func writeJSONBody(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func runFixture(t *testing.T, right, native bool) siteResults {
	t.Helper()
	ds := loadTestDataset(t)
	o := newOracle(t, ds, right, native)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/decide", o.handleDecide)
	mux.HandleFunc("POST /v1/stream", o.handleStream)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return runEval(context.Background(), NewEvaluator(srv.URL, ""), ds)
}

// TestFixtureEval runs the whole eval against an oracle gateway with no
// model: every site's option shaping must pass Validate, every reply
// style must Parse back to the right id, and every matcher must credit
// it, so each site scores 100% on both paths.
func TestFixtureEval(t *testing.T) {
	t.Parallel()
	r := runFixture(t, true, true)
	for name, m := range map[string]Metric{
		"kind decide": r.KindDecide, "kind classify": r.KindClassify,
		"light decide": r.LightDecide, "light classify": r.LightClassify,
		"agent decide": r.AgentDecide, "agent classify": r.AgentClassify,
		"kb decide": r.KBDecide, "kb classify": r.KBClassify,
	} {
		t.Logf("%s: %s", name, m.Report())
		if m.Total == 0 || m.Correct != m.Total {
			t.Errorf("%s: %s, want every sample correct", name, m.Report())
		}
	}
	for _, m := range r.decideMetrics() {
		if m.Native != m.Total || m.Fallback != 0 {
			t.Errorf("decide %s/%s: native %d fallback %d of %d", m.Provider, m.Model, m.Native, m.Fallback, m.Total)
		}
	}
	if f := gateFailures(r.swapped(), true, r.decideMetrics()); len(f) != 0 {
		t.Errorf("gate failed on a perfect native run: %v", f)
	}
}

// TestFixtureEvalWrongAnswers feeds every site a valid but wrong
// answer: no matcher may credit it.
func TestFixtureEvalWrongAnswers(t *testing.T) {
	t.Parallel()
	r := runFixture(t, false, true)
	for name, m := range map[string]Metric{
		"kind decide": r.KindDecide, "kind classify": r.KindClassify,
		"light decide": r.LightDecide, "light classify": r.LightClassify,
		"agent decide": r.AgentDecide, "agent classify": r.AgentClassify,
		"kb decide": r.KBDecide, "kb classify": r.KBClassify,
	} {
		if m.Correct != 0 {
			t.Errorf("%s: %s, want no sample credited", name, m.Report())
		}
	}
	if f := gateFailures(r.swapped(), false, r.decideMetrics()); len(f) == 0 {
		t.Error("gate passed with zero decide accuracy")
	}
}

// TestFixtureEvalFallback serves every decide answer through the llm
// fallback: -require-native must fail the gate, and only then.
func TestFixtureEvalFallback(t *testing.T) {
	t.Parallel()
	r := runFixture(t, true, false)
	for _, m := range r.decideMetrics() {
		if m.Fallback != m.Total || m.Native != 0 {
			t.Errorf("decide: native %d fallback %d of %d, want all fallback", m.Native, m.Fallback, m.Total)
		}
	}
	if f := gateFailures(r.swapped(), false, r.decideMetrics()); len(f) != 0 {
		t.Errorf("gate without -require-native: %v", f)
	}
	f := gateFailures(r.swapped(), true, r.decideMetrics())
	if len(f) != 1 || !strings.Contains(f[0], "llm fallback") {
		t.Errorf("gate with -require-native = %v, want one llm fallback failure", f)
	}
}

// TestFixtureMalformedReply checks a reply naming no option is
// ErrMalformed, never resolved to a guess.
func TestFixtureMalformedReply(t *testing.T) {
	t.Parallel()
	req := provider.DecideRequest{Kind: provider.DecideChoice, Question: missions.KindQuestion}
	for _, o := range missions.KindOptions() {
		req.Options = append(req.Options, provider.DecideOption{ID: o.ID, Description: o.Description})
	}
	if err := decide.Validate(&req); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, reply := range []string{"", "coding general", "I think coding", "codingx", "produces or changes code"} {
		if _, err := decide.Parse(req, reply); !errors.Is(err, decide.ErrMalformed) {
			t.Errorf("Parse(%q) err = %v, want ErrMalformed", reply, err)
		}
	}
}

func TestWilson(t *testing.T) {
	t.Parallel()
	tests := []struct {
		correct, total int
		lo, hi         float64
	}{
		{0, 0, 0, 1},
		{5, 6, 0.4365, 0.9699},
		{6, 6, 0.6097, 1},
		{0, 10, 0, 0.2775},
		{30, 32, 0.7988, 0.9827},
	}
	for _, tc := range tests {
		lo, hi := wilson(tc.correct, tc.total)
		if math.Abs(lo-tc.lo) > 0.0005 || math.Abs(hi-tc.hi) > 0.0005 {
			t.Errorf("wilson(%d, %d) = [%.4f, %.4f], want [%.4f, %.4f]", tc.correct, tc.total, lo, hi, tc.lo, tc.hi)
		}
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		decide, classif Metric
		want            string
	}{
		{"5/6 vs 5/6 is a tie", Metric{Correct: 5, Total: 6}, Metric{Correct: 5, Total: 6}, "TIE"},
		{"6/6 vs 5/6 is still a tie", Metric{Correct: 6, Total: 6}, Metric{Correct: 5, Total: 6}, "TIE"},
		{"clear win", Metric{Correct: 31, Total: 32}, Metric{Correct: 12, Total: 32}, "DECIDE BETTER"},
		{"clear loss", Metric{Correct: 10, Total: 32}, Metric{Correct: 30, Total: 32}, "DECIDE WORSE"},
	}
	for _, tc := range tests {
		if got := compare(tc.decide, tc.classif); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: compare = %q, want %s", tc.name, got, tc.want)
		}
	}
}

// TestGateFailures pins the gate thresholds the auto-dispatch swap
// shipped on (#1267): decide at or above the classifier, nonzero.
func TestGateFailures(t *testing.T) {
	t.Parallel()
	native := []Metric{{Total: 6, Native: 6}}
	mixed := []Metric{{Total: 6, Native: 5, Fallback: 1}}
	failed := []Metric{{Total: 6}}
	tests := []struct {
		name          string
		decide, class Metric
		requireNative bool
		metrics       []Metric
		want          int
	}{
		{"tie passes", Metric{Correct: 5, Total: 6}, Metric{Correct: 5, Total: 6}, false, native, 0},
		{"above passes", Metric{Correct: 6, Total: 6}, Metric{Correct: 5, Total: 6}, false, native, 0},
		{"below fails", Metric{Correct: 4, Total: 6}, Metric{Correct: 5, Total: 6}, false, native, 1},
		{"zero correct fails", Metric{Correct: 0, Total: 6}, Metric{Correct: 0, Total: 6}, false, native, 1},
		{"no samples fails", Metric{}, Metric{}, false, native, 1},
		{"native required and native", Metric{Correct: 5, Total: 6}, Metric{Correct: 5, Total: 6}, true, native, 0},
		{"native required, one fallback answer", Metric{Correct: 5, Total: 6}, Metric{Correct: 5, Total: 6}, true, mixed, 1},
		{"native required, no answer at all", Metric{Correct: 5, Total: 6}, Metric{Correct: 5, Total: 6}, true, failed, 1},
	}
	for _, tc := range tests {
		if got := gateFailures([]swappedSite{{"Agent Auto-Dispatch", tc.decide, tc.class}}, tc.requireNative, tc.metrics); len(got) != tc.want {
			t.Errorf("%s: failures = %v, want %d", tc.name, got, tc.want)
		}
	}
	sites := []swappedSite{
		{"Mission Kind", Metric{Correct: 4, Total: 6}, Metric{Correct: 5, Total: 6}},
		{"Agent Auto-Dispatch", Metric{Correct: 6, Total: 6}, Metric{Correct: 5, Total: 6}},
	}
	if got := gateFailures(sites, false, native); len(got) != 1 || !strings.Contains(got[0], "Mission Kind") {
		t.Errorf("one site below its classifier: failures = %v, want one naming Mission Kind", got)
	}
}

func TestMatchers(t *testing.T) {
	t.Parallel()
	cols := []KBCollection{{ID: "c-a"}, {ID: "c-b"}}
	tests := []struct {
		name, got, want string
	}{
		{"kb id", matchKB("C-B", cols), "c-b"},
		{"kb new", matchKB("new", cols), "NEW"},
		{"kb unknown", matchKB("c-z", cols), ""},
		{"kb reply id", parseKBReply("c-a\nbecause", cols), "c-a"},
		{"kb reply new", parseKBReply("NEW: Topic | scope", cols), "NEW"},
		{"kb reply unknown", parseKBReply("c-a, c-b", cols), ""},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
