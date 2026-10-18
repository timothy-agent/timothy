package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
)

type MissionSample struct {
	Goal      string `json:"goal"`
	WantKind  string `json:"want_kind"`
	WantLight bool   `json:"want_light"`
	Source    string `json:"source"`
}

type AgentCandidate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type AgentSample struct {
	Message    string           `json:"message"`
	Candidates []AgentCandidate `json:"candidates"`
	WantAgent  string           `json:"want_agent"`
	Source     string           `json:"source"`
}

type KBCollection struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	DocCount    int    `json:"doc_count"`
}

type KBFilingSample struct {
	Title       string         `json:"title"`
	Text        string         `json:"text"`
	Collections []KBCollection `json:"collections"`
	WantChoice  string         `json:"want_choice"`
	Source      string         `json:"source"`
}

type Dataset struct {
	Missions []MissionSample  `json:"missions"`
	Agents   []AgentSample    `json:"agents"`
	KBFiling []KBFilingSample `json:"kb_filing"`
}

// Metric is one site's tally for one classifier mode. Native and
// Fallback count decide answers by how the gateway served them; the
// stream classifier leaves both zero.
type Metric struct {
	Total       int
	Correct     int
	Latency     time.Duration
	TotalTokens int
	Provider    string
	Model       string
	Native      int
	Fallback    int
}

func (m Metric) Accuracy() float64 {
	if m.Total == 0 {
		return 0
	}
	return float64(m.Correct) / float64(m.Total) * 100
}

// Interval is the 95% Wilson score interval of the accuracy, in percent.
func (m Metric) Interval() (lo, hi float64) {
	lo, hi = wilson(m.Correct, m.Total)
	return lo * 100, hi * 100
}

// Report renders "c/n = p% [lo, hi]".
func (m Metric) Report() string {
	lo, hi := m.Interval()
	return fmt.Sprintf("%d/%d = %.1f%% [%.1f, %.1f]", m.Correct, m.Total, m.Accuracy(), lo, hi)
}

func (m Metric) AvgLatencyMS() int64 {
	if m.Total == 0 {
		return 0
	}
	return m.Latency.Milliseconds() / int64(m.Total)
}

func (m Metric) AvgTokens() int {
	if m.Total == 0 {
		return 0
	}
	return m.TotalTokens / m.Total
}

// addDecide books one decide call's cost and how it was served.
func (m *Metric) addDecide(r decideReply, err error) {
	m.Latency += r.Latency
	m.TotalTokens += r.Tokens
	if err != nil {
		return
	}
	m.Provider, m.Model = r.Provider, r.Model
	if r.Native {
		m.Native++
	} else {
		m.Fallback++
	}
}

// wilson is the 95% Wilson score interval for correct out of total.
func wilson(correct, total int) (lo, hi float64) {
	if total == 0 {
		return 0, 1
	}
	const z = 1.96
	n := float64(total)
	p := float64(correct) / n
	denom := 1 + z*z/n
	center := (p + z*z/(2*n)) / denom
	half := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n)) / denom
	return math.Max(0, center-half), math.Min(1, center+half)
}

// overlaps reports whether two metrics' 95% intervals overlap: the
// difference between them is not distinguishable from noise.
func overlaps(a, b Metric) bool {
	alo, ahi := a.Interval()
	blo, bhi := b.Interval()
	return alo <= bhi && blo <= ahi
}

// compare names the relation between decide and the classifier.
func compare(decideM, classifyM Metric) string {
	if overlaps(decideM, classifyM) {
		return "TIE (95% intervals overlap)"
	}
	if decideM.Accuracy() > classifyM.Accuracy() {
		return "DECIDE BETTER"
	}
	return "DECIDE WORSE"
}

// gateFailures applies the swap gate: a swapped site's decide accuracy
// must be nonzero and at least the classifier's; with requireNative,
// every decide answer must come from a native decision model.
func gateFailures(agentDecideM, agentClassifyM Metric, requireNative bool, decideMetrics []Metric) []string {
	var failed []string
	if agentDecideM.Total == 0 || agentDecideM.Correct == 0 || agentDecideM.Accuracy() < agentClassifyM.Accuracy() {
		failed = append(failed, fmt.Sprintf("Accuracy on swapped site Agent Auto-Dispatch: decide %.1f%% < shipping classifier %.1f%%", agentDecideM.Accuracy(), agentClassifyM.Accuracy()))
	}
	if requireNative {
		native, fallback := 0, 0
		for _, m := range decideMetrics {
			native += m.Native
			fallback += m.Fallback
		}
		switch {
		case fallback > 0:
			failed = append(failed, fmt.Sprintf("Native decision model required (-require-native), but %d of %d decide answers came from the llm fallback", fallback, native+fallback))
		case native == 0:
			failed = append(failed, "Native decision model required (-require-native), but no decide call succeeded")
		}
	}
	return failed
}

type Evaluator struct {
	client     *http.Client
	gatewayURL string
	route      string
}

func NewEvaluator(gatewayURL, route string) *Evaluator {
	return &Evaluator{
		client:     &http.Client{Timeout: 90 * time.Second},
		gatewayURL: strings.TrimRight(gatewayURL, "/"),
		route:      route,
	}
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// decideAPIResponse is the POST /v1/decide reply. Native is false when
// the route answered through the llm fallback (the ledger books that
// attempt with effort "low").
type decideAPIResponse struct {
	Choice   string `json:"choice"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Native   bool   `json:"native"`
	LedgerID string `json:"ledger_id"`
	Usage    *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// decideOption is one typed POST /v1/decide option; the reply's choice is its id.
type decideOption struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
}

// decideReply is one decide call's answer and cost.
type decideReply struct {
	Choice   string
	Tokens   int
	Provider string
	Model    string
	Native   bool
	Latency  time.Duration
}

func (e *Evaluator) callDecide(ctx context.Context, kind, question, state string, options []decideOption, purpose string) (decideReply, error) {
	reqBody := map[string]any{
		"kind":     kind,
		"question": question,
		"options":  options,
		"purpose":  purpose,
	}
	if e.route != "" {
		reqBody["route"] = e.route
	}
	if state != "" {
		reqBody["state"] = state
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return decideReply{}, err
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.gatewayURL+"/v1/decide", bytes.NewReader(b))
	if err != nil {
		return decideReply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	r := decideReply{Latency: time.Since(start)}
	if err != nil {
		return r, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return r, fmt.Errorf("decide returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var out decideAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return r, err
	}
	r.Choice, r.Provider, r.Model, r.Native = out.Choice, out.Provider, out.Model, out.Native
	if out.Usage != nil {
		r.Tokens = out.Usage.OutputTokens
	}
	return r, nil
}

type streamUsage struct {
	OutputTokens int `json:"output_tokens"`
}

type streamEvent struct {
	Type  string       `json:"type"`
	Text  string       `json:"text"`
	Usage *streamUsage `json:"usage"`
}

func (e *Evaluator) callStream(ctx context.Context, system, prompt, purpose string) (string, int, time.Duration, error) {
	reqBody := map[string]any{
		"route":   "summarize",
		"purpose": purpose,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": 512,
		"effort":     "low",
	}
	if system != "" {
		reqBody["system"] = system
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, 0, err
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.gatewayURL+"/v1/stream", bytes.NewReader(b))
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return "", 0, time.Since(start), err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", 0, time.Since(start), fmt.Errorf("stream returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	var textBuf strings.Builder
	tokens := 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(data), &ev); err == nil {
			if ev.Type == "chunk" || ev.Type == "text" {
				textBuf.WriteString(ev.Text)
			}
			if ev.Usage != nil && ev.Usage.OutputTokens > 0 {
				tokens = ev.Usage.OutputTokens
			}
		}
	}
	dur := time.Since(start)
	if tokens == 0 && textBuf.Len() > 0 {
		tokens = len(strings.Fields(textBuf.String()))
	}
	return textBuf.String(), tokens, dur, scanner.Err()
}

// siteResults holds every metric one eval run produces.
type siteResults struct {
	KindDecide, KindClassify   Metric
	LightDecide, LightClassify Metric
	AgentDecide, AgentClassify Metric
	KBDecide, KBClassify       Metric
}

func (r siteResults) decideMetrics() []Metric {
	return []Metric{r.KindDecide, r.LightDecide, r.AgentDecide, r.KBDecide}
}

// runEval runs every site against the gateway behind e.
func runEval(ctx context.Context, e *Evaluator, ds Dataset) siteResults {
	var r siteResults
	r.KindDecide, r.KindClassify, r.LightDecide, r.LightClassify = evalMissionPreview(ctx, e, ds.Missions)
	r.AgentDecide, r.AgentClassify = evalAgentDispatch(ctx, e, ds.Agents)
	r.KBDecide, r.KBClassify = evalKBFiling(ctx, e, ds.KBFiling)
	return r
}

func loadDataset(path string) (Dataset, error) {
	var ds Dataset
	b, err := os.ReadFile(path) // #nosec G304 -- operator-supplied dataset path
	if err != nil {
		return ds, err
	}
	err = json.Unmarshal(b, &ds)
	return ds, err
}

func main() {
	var (
		datasetPath   = flag.String("dataset", "", "path to dataset.json")
		gatewayURL    = flag.String("gateway-url", "http://gateway:8081", "gateway base URL")
		route         = flag.String("route", "", "gateway route for decide (empty uses default decide role, 'summarize' exercises LLM fallback)")
		mode          = flag.String("mode", "live", "evaluation mode: 'live' (gateway API) or 'simulate'")
		requireNative = flag.Bool("require-native", false, "fail if any decide answer came from the llm fallback instead of a native decision model")
		maxLatencyMS  = flag.Int64("max-latency-ms", 300, "maximum latency ceiling in ms for native decision model")
	)
	flag.Parse()

	if *datasetPath == "" {
		exePath, err := os.Executable()
		if err == nil {
			*datasetPath = filepath.Join(filepath.Dir(exePath), "dataset.json")
		}
		if _, err := os.Stat(*datasetPath); err != nil {
			*datasetPath = "scripts/decide-eval/dataset.json"
		}
	}

	ds, err := loadDataset(*datasetPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading dataset from %s: %v\n", *datasetPath, err)
		os.Exit(1)
	}

	fmt.Printf("# Decision Model vs Shipping Classifier Evaluation Gate\n\n")
	fmt.Printf("Dataset: %d missions, %d agent dispatch samples, %d KB documents\n", len(ds.Missions), len(ds.Agents), len(ds.KBFiling))
	fmt.Printf("Gateway URL: %s | Mode: %s\n", *gatewayURL, *mode)
	fmt.Printf("Accuracy is reported as correct/total = percent [95%% Wilson interval].\n\n")

	if *mode == "simulate" {
		fmt.Println("Simulation mode disabled. Running live evaluation against gateway.")
	}

	r := runEval(context.Background(), NewEvaluator(*gatewayURL, *route), ds)

	printSiteSummary("Mission Kind Preview", r.KindDecide, r.KindClassify)
	printSiteSummary("Mission Light Preview", r.LightDecide, r.LightClassify)
	printSiteSummary("Agent Auto-Dispatch", r.AgentDecide, r.AgentClassify)
	printSiteSummary("KB Collection Filing", r.KBDecide, r.KBClassify)

	decideProvider := r.KindDecide.Provider
	if decideProvider == "" {
		decideProvider = "decide route"
	}
	if r.KindDecide.Model != "" {
		decideProvider = fmt.Sprintf("%s / %s", decideProvider, r.KindDecide.Model)
	}

	fmt.Println("\n## Benchmark Comparison Results")
	fmt.Println()
	fmt.Println("| Evaluation Site | Classifier Mode | Accuracy [95% CI] | Avg Latency | Tokens / Decision |")
	fmt.Println("|---|---|---|---|---|")
	decideLabel := fmt.Sprintf("Decide (%s)", decideProvider)
	printTableRow("Mission Kind", decideLabel, r.KindDecide)
	printTableRow("Mission Kind", "Shipping Classifier (stream)", r.KindClassify)
	printTableRow("Mission Light", decideLabel, r.LightDecide)
	printTableRow("Mission Light", "Shipping Classifier (stream)", r.LightClassify)
	printTableRow("Agent Auto-Dispatch", decideLabel, r.AgentDecide)
	printTableRow("Agent Auto-Dispatch", "Shipping Classifier (stream)", r.AgentClassify)
	printTableRow("KB Collection Filing", decideLabel, r.KBDecide)
	printTableRow("KB Collection Filing", "Shipping Classifier (stream)", r.KBClassify)

	var totalLatency time.Duration
	totalCalls, totalTokens, native, fallback := 0, 0, 0, 0
	for _, m := range r.decideMetrics() {
		totalLatency += m.Latency
		totalCalls += m.Total
		totalTokens += m.TotalTokens
		native += m.Native
		fallback += m.Fallback
	}
	avgLatencyMS, avgTokens := int64(0), 0
	if totalCalls > 0 {
		avgLatencyMS = totalLatency.Milliseconds() / int64(totalCalls)
		avgTokens = totalTokens / totalCalls
	}

	fmt.Println("\n### Route & Decision Model Status:")
	fmt.Printf("- Decide answers: %d native, %d llm fallback\n", native, fallback)
	fmt.Printf("- Provider / Model: %s\n", decideProvider)

	fmt.Println("\n### Live Swap Qualification (Agent Auto-Dispatch):")
	fmt.Printf("- Agent Auto-Dispatch: decide %s vs shipping classifier %s: %s\n", r.AgentDecide.Report(), r.AgentClassify.Report(), compare(r.AgentDecide, r.AgentClassify))

	fmt.Println("\n### Unswapped Candidate Benchmark (retained on shipping classifiers):")
	fmt.Printf("- Mission Kind: decide %s vs shipping classifier %s: %s\n", r.KindDecide.Report(), r.KindClassify.Report(), compare(r.KindDecide, r.KindClassify))
	fmt.Printf("- Mission Light: decide %s vs shipping classifier %s: %s\n", r.LightDecide.Report(), r.LightClassify.Report(), compare(r.LightDecide, r.LightClassify))
	fmt.Printf("- KB Collection Filing: decide %s vs shipping classifier %s: %s\n", r.KBDecide.Report(), r.KBClassify.Report(), compare(r.KBDecide, r.KBClassify))

	fmt.Println("\n### Latency & Token Verification:")
	fmt.Printf("- Swapped Site (Agent Auto-Dispatch) Latency: avg %d ms\n", r.AgentDecide.AvgLatencyMS())
	fmt.Printf("- Benchmark Total Latency: avg %d ms across all candidate sites\n", avgLatencyMS)
	fmt.Printf("- Token consumption: avg %d output tokens per decision\n", avgTokens)
	if fallback == 0 && native > 0 {
		fmt.Printf("- Target Latency Ceiling: %d ms (note: issue #1245 target applied to POST /v1/missions/classify when bound to decision models)\n", *maxLatencyMS)
	}

	failed := gateFailures(r.AgentDecide, r.AgentClassify, *requireNative, r.decideMetrics())
	fmt.Println("\n### Evaluation Gate Verdict:")
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: Decision model did not meet acceptance criteria:\n")
		for _, s := range failed {
			fmt.Fprintf(os.Stderr, "  - %s\n", s)
		}
		os.Exit(1)
	}
	fmt.Printf("PASS: decide matches or exceeds the shipping classifier on swapped sites (%s).\n", decideProvider)
}

func printSiteSummary(siteName string, decideM, classifyM Metric) {
	fmt.Printf("## %s\n", siteName)
	fmt.Printf("  - Decision Model     : %s in avg %dms (%d tokens) [%s]\n",
		decideM.Report(), decideM.AvgLatencyMS(), decideM.AvgTokens(), decideM.Provider)
	fmt.Printf("  - Shipping Classifier: %s in avg %dms (%d tokens)\n",
		classifyM.Report(), classifyM.AvgLatencyMS(), classifyM.AvgTokens())
}

func printTableRow(site, mode string, m Metric) {
	fmt.Printf("| %s | %s | %s | ~%d ms | %d tokens |\n",
		site, mode, m.Report(), m.AvgLatencyMS(), m.AvgTokens())
}

// classifierSystem is the shipping classifiers' system line.
const classifierSystem = "Answer with only what is requested — no prose, no explanation."

const (
	kindQuestion  = "Classify this mission goal based on its deliverable, not its topic."
	lightQuestion = "Is this goal a single-pass task deliverable in one response — a read, summary, lookup, or short write-up with no multi-step plan or file artifacts?"
)

// kindOptions are the decide options for a mission's kind.
func kindOptions() []decideOption {
	return []decideOption{
		{ID: "coding", Description: "produces or changes code, scripts, or configuration files in a repository"},
		{ID: "general", Description: "output is a document, report, analysis, book, plan, or data gathering, even when its subject is programming, software, or coding (a book or article about coding is general)"},
	}
}

// matchKind maps a decide choice to a mission kind, or "" when it is
// neither option.
func matchKind(choice string) string {
	choice = strings.TrimSpace(choice)
	for _, k := range []string{"coding", "general"} {
		if strings.EqualFold(choice, k) {
			return k
		}
	}
	return ""
}

// matchLight maps a yesno decide choice to the light flag; ok is false
// for anything else.
func matchLight(choice string) (light, ok bool) {
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "yes":
		return true, true
	case "no":
		return false, true
	}
	return false, false
}

// parseLightReply reads the shipping light classifier's one-word reply.
func parseLightReply(reply string) bool {
	lower := strings.ToLower(reply)
	return strings.Contains(lower, "yes") && !strings.Contains(lower, "no")
}

// evalMissionPreview evaluates the preview endpoint path (classifyKindAndLightWithDecide vs classifyKindAndLight).
// The shipping classifier prompt classifies along both axes in one model turn.
// The decide path calls decide for kind, and for general goals decide for light.
func evalMissionPreview(ctx context.Context, e *Evaluator, missions []MissionSample) (Metric, Metric, Metric, Metric) {
	var kindDecideM, kindClassifyM Metric
	var lightDecideM, lightClassifyM Metric

	kindDecideM.Total = len(missions)
	kindClassifyM.Total = len(missions)

	for _, m := range missions {
		promptKind := "Classify this mission goal along two independent axes, based on its deliverable, not its topic.\n" +
			"Axis 1 — coding or general: coding means the mission produces or changes code, scripts, or configuration files in a repository; general means the output is a document, report, analysis, book, plan, or data gathering, even when its subject is programming, software, or coding (a book or article about coding is general).\n" +
			"Axis 2 — light or full: light means the goal is a single-pass task deliverable in one response (a read, summary, lookup, or short write-up with no multi-step plan or file artifacts); full means it needs a plan and artifacts.\n" +
			"Answer with exactly two words separated by a space: first coding or general, then light or full.\n\n" +
			"Goal: " + m.Goal
		replyKind, tokensKind, durKind, errKind := e.callStream(ctx, classifierSystem, promptKind, "mission_classify")
		kindClassifyM.Latency += durKind
		kindClassifyM.TotalTokens += tokensKind
		if errKind == nil {
			if gotKind, _ := parseKindAndLight(replyKind); gotKind == m.WantKind {
				kindClassifyM.Correct++
			}
		}

		r, err := e.callDecide(ctx, "choice", kindQuestion, m.Goal, kindOptions(), "kind")
		kindDecideM.addDecide(r, err)
		if err == nil && matchKind(r.Choice) == m.WantKind {
			kindDecideM.Correct++
		}

		if m.WantKind != "general" {
			continue
		}
		lightDecideM.Total++
		lightClassifyM.Total++

		promptLight := "Is this goal a single-pass task deliverable in one response — a read, summary, lookup, or short write-up with no multi-step plan or file artifacts? Answer with exactly one word, yes or no.\n\n" +
			"Goal: " + m.Goal
		replyLight, tokensLight, durLight, errLight := e.callStream(ctx, classifierSystem, promptLight, "light")
		lightClassifyM.Latency += durLight
		lightClassifyM.TotalTokens += tokensLight
		if errLight == nil && parseLightReply(replyLight) == m.WantLight {
			lightClassifyM.Correct++
		}

		lr, lerr := e.callDecide(ctx, "yesno", lightQuestion, m.Goal, nil, "light")
		lightDecideM.addDecide(lr, lerr)
		if lerr == nil {
			if light, ok := matchLight(lr.Choice); ok && light == m.WantLight {
				lightDecideM.Correct++
			}
		}
	}
	return kindDecideM, kindClassifyM, lightDecideM, lightClassifyM
}

func parseKindAndLight(reply string) (kind string, light bool) {
	fields := strings.Fields(strings.ToLower(reply))
	kindFound, lightFound := false, false
	for _, field := range fields {
		clean := strings.Trim(field, ".,")
		if !kindFound {
			switch clean {
			case "coding":
				kind = "coding"
				kindFound = true
				continue
			case "general":
				kind = "general"
				kindFound = true
				continue
			}
		}
		if kindFound && !lightFound {
			switch clean {
			case "light":
				light = true
				lightFound = true
			case "full":
				light = false
				lightFound = true
			}
		}
	}
	if !kindFound {
		kind = "general"
	}
	return kind, kind == "general" && light
}

// candidateAgents turns a sample's candidates into agents for the
// production dispatch functions.
func candidateAgents(cands []AgentCandidate) []agents.Agent {
	out := make([]agents.Agent, len(cands))
	for i, c := range cands {
		out[i] = agents.Agent{ID: c.ID, Name: c.Name, Description: c.Description, Enabled: true}
	}
	return out
}

// evalAgentDispatch runs both paths through the production dispatch
// code: agents.DispatchWithDecide shapes the decide options and matches
// the choice; agents.Dispatch builds the numbered prompt and parses the
// classifier's reply. Fallback id "" marks a miss.
func evalAgentDispatch(ctx context.Context, e *Evaluator, samples []AgentSample) (Metric, Metric) {
	var decideM, classifyM Metric
	decideM.Total = len(samples)
	classifyM.Total = len(samples)

	decideFn := func(ctx context.Context, kind, question, state string, options []agents.DecideOption) (string, error) {
		opts := make([]decideOption, len(options))
		for i, o := range options {
			opts[i] = decideOption{ID: o.ID, Description: o.Description}
		}
		r, err := e.callDecide(ctx, kind, question, state, opts, "dispatch")
		decideM.addDecide(r, err)
		return r.Choice, err
	}
	classifyFn := func(ctx context.Context, prompt string) (string, error) {
		reply, tokens, dur, err := e.callStream(ctx, classifierSystem, prompt, "dispatch")
		classifyM.Latency += dur
		classifyM.TotalTokens += tokens
		return reply, err
	}

	for _, a := range samples {
		cands := candidateAgents(a.Candidates)
		if agents.DispatchWithDecide(ctx, decideFn, nil, a.Message, cands, "") == a.WantAgent {
			decideM.Correct++
		}
		if agents.Dispatch(ctx, classifyFn, a.Message, cands, "") == a.WantAgent {
			classifyM.Correct++
		}
	}
	return decideM, classifyM
}

// kbNewOption is the decide option that proposes a new collection.
const kbNewOption = "NEW"

// kbOptions are the decide options for filing into collections.
func kbOptions(cols []KBCollection) []decideOption {
	options := make([]decideOption, 0, len(cols)+1)
	for _, c := range cols {
		options = append(options, decideOption{ID: c.ID, Description: fmt.Sprintf("%s (%d docs) - %s", c.Name, c.DocCount, c.Description)})
	}
	return append(options, decideOption{ID: kbNewOption, Description: "propose a new collection when nothing fits"})
}

// kbState is the decide state for one document.
func kbState(doc KBFilingSample) string {
	return fmt.Sprintf("Document title: %s\n\n%s", doc.Title, truncateRunes(doc.Text, 4000))
}

// matchKB maps a decide choice to a collection id or NEW, or "".
func matchKB(choice string, cols []KBCollection) string {
	choice = strings.TrimSpace(choice)
	if strings.EqualFold(choice, kbNewOption) {
		return kbNewOption
	}
	for _, c := range cols {
		if strings.EqualFold(choice, c.ID) {
			return c.ID
		}
	}
	return ""
}

// parseKBReply reads the shipping KB classifier's first line: a bare
// collection id, or a NEW proposal.
func parseKBReply(reply string, cols []KBCollection) string {
	firstLine, _, _ := strings.Cut(strings.TrimSpace(reply), "\n")
	firstLine = strings.TrimSpace(firstLine)
	if strings.HasPrefix(strings.ToUpper(firstLine), kbNewOption) {
		return kbNewOption
	}
	for _, c := range cols {
		if firstLine == c.ID {
			return c.ID
		}
	}
	return ""
}

func evalKBFiling(ctx context.Context, e *Evaluator, kbFiles []KBFilingSample) (Metric, Metric) {
	var decideM, classifyM Metric
	decideM.Total = len(kbFiles)
	classifyM.Total = len(kbFiles)

	const decideSystemRule = "You file documents into a knowledge base. Given a document and a list of existing collections, reply with the best-matching existing collection, or propose NEW when nothing fits. Prefer the MOST SPECIFIC existing collection that fits the document's primary topic; propose NEW only when the primary topic is clearly narrower than every existing collection and is a topic more future documents could join. A broad collection is the wrong pick when a narrower on-topic one already exists, or when the document's topic deserves its own collection - do not use a catch-all just because it is easy."
	const classifySystem = `You file documents into a knowledge base. Given a document and a list of existing collections, reply with exactly one line: either the bare id of the best-matching existing collection, or "NEW: <name> | <description>" to propose a new collection when nothing fits. Prefer the MOST SPECIFIC existing collection that fits the document's primary topic; propose NEW only when the primary topic is clearly narrower than every existing collection and is a topic more future documents could join. A broad collection is the wrong pick when a narrower on-topic one already exists, or when the document's topic deserves its own collection - do not use a catch-all just because it is easy. A new collection's name must be a SHORT GENERIC TOPIC CATEGORY of 1-3 words (like "AI Agents", "Scalability", "Code Review", "CVs") that many future documents could belong to, never a title describing this specific document. The description should state the topic's scope broadly. No other text.`

	for _, doc := range kbFiles {
		r, err := e.callDecide(ctx, "choice", decideSystemRule, kbState(doc), kbOptions(doc.Collections), "kb_classify")
		decideM.addDecide(r, err)
		if err == nil && matchKB(r.Choice, doc.Collections) == doc.WantChoice {
			decideM.Correct++
		}

		var list strings.Builder
		for _, o := range kbOptions(doc.Collections) {
			if o.ID == kbNewOption {
				continue
			}
			list.WriteString(o.ID + ": " + o.Description)
			list.WriteByte('\n')
		}
		userPrompt := fmt.Sprintf("Existing collections:\n%s\nDocument title: %s\n\n%s",
			list.String(), doc.Title, truncateRunes(doc.Text, 4000))
		reply, streamTokens, streamDur, streamErr := e.callStream(ctx, classifySystem, userPrompt, "kb_classify")
		classifyM.Latency += streamDur
		classifyM.TotalTokens += streamTokens
		if streamErr == nil && parseKBReply(reply, doc.Collections) == doc.WantChoice {
			classifyM.Correct++
		}
	}
	return decideM, classifyM
}
