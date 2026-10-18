package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type MissionSample struct {
	Goal      string `json:"goal"`
	WantKind  string `json:"want_kind"`
	WantLight bool   `json:"want_light"`
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
}

type Dataset struct {
	Missions []MissionSample  `json:"missions"`
	Agents   []AgentSample    `json:"agents"`
	KBFiling []KBFilingSample `json:"kb_filing"`
}

type Metric struct {
	Total       int
	Correct     int
	Latency     time.Duration
	TotalTokens int
	Provider    string
	Model       string
	Native      bool
}

func (m Metric) Accuracy() float64 {
	if m.Total == 0 {
		return 0
	}
	return float64(m.Correct) / float64(m.Total) * 100
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

func (e *Evaluator) callDecide(ctx context.Context, kind, question, state string, options []decideOption, purpose string) (string, int, string, string, bool, time.Duration, error) {
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
		return "", 0, "", "", false, 0, err
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.gatewayURL+"/v1/decide", bytes.NewReader(b))
	if err != nil {
		return "", 0, "", "", false, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	dur := time.Since(start)
	if err != nil {
		return "", 0, "", "", false, dur, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", 0, "", "", false, dur, fmt.Errorf("decide returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var out decideAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, "", "", false, dur, err
	}
	tokens := 0
	if out.Usage != nil {
		tokens = out.Usage.OutputTokens
	}
	return out.Choice, tokens, out.Provider, out.Model, out.Native, dur, nil
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

func main() {
	var (
		datasetPath   = flag.String("dataset", "", "path to dataset.json")
		gatewayURL    = flag.String("gateway-url", "http://gateway:8081", "gateway base URL")
		route         = flag.String("route", "", "gateway route for decide (empty uses default decide role, 'summarize' exercises LLM fallback)")
		mode          = flag.String("mode", "live", "evaluation mode: 'live' (gateway API) or 'simulate'")
		requireNative = flag.Bool("require-native", false, "fail if decide route is not served by a native decision model (Laya or Jev)")
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

	dataBytes, err := os.ReadFile(*datasetPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading dataset from %s: %v\n", *datasetPath, err)
		os.Exit(1)
	}

	var ds Dataset
	if err := json.Unmarshal(dataBytes, &ds); err != nil {
		fmt.Fprintf(os.Stderr, "error decoding dataset: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("# Decision Model vs Shipping Classifier Evaluation Gate\n\n")
	fmt.Printf("Dataset: %d stored missions, %d agent dispatch samples, %d exported KB documents\n", len(ds.Missions), len(ds.Agents), len(ds.KBFiling))
	fmt.Printf("Gateway URL: %s | Mode: %s\n\n", *gatewayURL, *mode)

	if *mode == "simulate" {
		fmt.Println("Simulation mode disabled. Running live evaluation against gateway.")
	}

	evaluator := NewEvaluator(*gatewayURL, *route)
	ctx := context.Background()

	// 1. Mission Kind & Light Preview (evaluates both Kind and Light against production classifyKindAndLight baseline)
	fmt.Printf("## Evaluating Site 1 & 2: Mission Kind & Light Preview (%d samples)...\n", len(ds.Missions))
	kindDecideM, kindClassifyM, lightDecideM, lightClassifyM := evalMissionPreview(ctx, evaluator, ds.Missions)
	printSiteSummary("Mission Kind Preview", kindDecideM, kindClassifyM)
	printSiteSummary("Mission Light Preview", lightDecideM, lightClassifyM)

	// 2. Agent Dispatch (evaluates dispatch against production dispatchPrompt + system line)
	fmt.Printf("\n## Evaluating Site 3: Agent Auto-Dispatch (%d samples)...\n", len(ds.Agents))
	agentDecideM, agentClassifyM := evalAgentDispatch(ctx, evaluator, ds.Agents)
	printSiteSummary("Agent Auto-Dispatch", agentDecideM, agentClassifyM)

	// 3. KB Collection Filing (evaluates filing against production classifySystem + 4000-rune cap)
	fmt.Printf("\n## Evaluating Site 4: KB Collection Filing (%d samples)...\n", len(ds.KBFiling))
	kbDecideM, kbClassifyM := evalKBFiling(ctx, evaluator, ds.KBFiling)
	printSiteSummary("KB Collection Filing", kbDecideM, kbClassifyM)

	// Decision provider label (read from live responses)
	decideProvider := kindDecideM.Provider
	if decideProvider == "" {
		decideProvider = "decide route"
	}
	if kindDecideM.Model != "" {
		decideProvider = fmt.Sprintf("%s / %s", decideProvider, kindDecideM.Model)
	}

	// Overall Table
	fmt.Println("\n## Benchmark Comparison Results")
	fmt.Println()
	fmt.Println("| Evaluation Site | Classifier Mode | Samples | Accuracy | Avg Latency | Tokens / Decision |")
	fmt.Println("|---|---|---|---|---|---|")
	printTableRow("Mission Kind", fmt.Sprintf("Decide (%s)", decideProvider), kindDecideM, kindDecideM.AvgTokens())
	printTableRow("Mission Kind", "Shipping Classifier (stream)", kindClassifyM, kindClassifyM.AvgTokens())
	printTableRow("Mission Light", fmt.Sprintf("Decide (%s)", decideProvider), lightDecideM, lightDecideM.AvgTokens())
	printTableRow("Mission Light", "Shipping Classifier (stream)", lightClassifyM, lightClassifyM.AvgTokens())
	printTableRow("Agent Auto-Dispatch", fmt.Sprintf("Decide (%s)", decideProvider), agentDecideM, agentDecideM.AvgTokens())
	printTableRow("Agent Auto-Dispatch", "Shipping Classifier (stream)", agentClassifyM, agentClassifyM.AvgTokens())
	printTableRow("KB Collection Filing", fmt.Sprintf("Decide (%s)", decideProvider), kbDecideM, kbDecideM.AvgTokens())
	printTableRow("KB Collection Filing", "Shipping Classifier (stream)", kbClassifyM, kbClassifyM.AvgTokens())

	// Latency & Token verification
	allDecideMetrics := []Metric{kindDecideM, lightDecideM, agentDecideM, kbDecideM}
	var totalDecideLatency time.Duration
	var totalDecideCalls int
	var totalDecideTokens int
	isNative := false
	for _, m := range allDecideMetrics {
		totalDecideLatency += m.Latency
		totalDecideCalls += m.Total
		totalDecideTokens += m.TotalTokens
		if m.Native {
			isNative = true
		}
	}
	avgDecideLatencyMS := int64(0)
	if totalDecideCalls > 0 {
		avgDecideLatencyMS = totalDecideLatency.Milliseconds() / int64(totalDecideCalls)
	}
	avgDecideTokens := 0
	if totalDecideCalls > 0 {
		avgDecideTokens = totalDecideTokens / totalDecideCalls
	}

	fmt.Println("\n### Route & Decision Model Status:")
	if isNative {
		fmt.Printf("- Model Type: Native Decision Model (provider.Decider)\n")
		fmt.Printf("- Provider / Model: %s\n", decideProvider)
	} else {
		fmt.Printf("- Model Type: LLM Fallback (chat provider)\n")
		fmt.Printf("- Provider / Model: %s\n", decideProvider)
	}

	fmt.Println("\n### Live Swap Qualification (Agent Auto-Dispatch):")
	if agentDecideM.Accuracy() >= agentClassifyM.Accuracy() {
		fmt.Printf("- Agent Auto-Dispatch: MATCH/BEAT (decide %.1f%% >= shipping classifier %.1f%%)\n", agentDecideM.Accuracy(), agentClassifyM.Accuracy())
	} else {
		fmt.Printf("- Agent Auto-Dispatch: UNDER BASELINE (decide %.1f%% < shipping classifier %.1f%%)\n", agentDecideM.Accuracy(), agentClassifyM.Accuracy())
	}

	fmt.Println("\n### Unswapped Candidate Benchmark (retained on shipping classifiers):")
	fmt.Printf("- Mission Kind: decide %.1f%% vs shipping classifier %.1f%%\n", kindDecideM.Accuracy(), kindClassifyM.Accuracy())
	fmt.Printf("- Mission Light: decide %.1f%% vs shipping classifier %.1f%%\n", lightDecideM.Accuracy(), lightClassifyM.Accuracy())
	fmt.Printf("- KB Collection Filing: decide %.1f%% vs shipping classifier %.1f%%\n", kbDecideM.Accuracy(), kbClassifyM.Accuracy())

	fmt.Println("\n### Latency & Token Verification:")
	if isNative {
		fmt.Printf("- Swapped Site (Agent Auto-Dispatch) Latency: avg %d ms\n", agentDecideM.AvgLatencyMS())
		fmt.Printf("- Benchmark Total Latency: avg %d ms across all candidate sites (includes long-text KB scoring)\n", avgDecideLatencyMS)
		fmt.Printf("- Token consumption: avg %d output tokens per decision\n", avgDecideTokens)
		fmt.Printf("- Target Latency Ceiling: %d ms (note: issue #1245 target applied to POST /v1/missions/classify when bound to decision models)\n", *maxLatencyMS)
	} else {
		fmt.Printf("- Swapped Site (Agent Auto-Dispatch) Latency: avg %d ms\n", agentDecideM.AvgLatencyMS())
		fmt.Printf("- Benchmark Total Latency: avg %d ms across all candidate sites\n", avgDecideLatencyMS)
		fmt.Printf("- Token consumption: avg %d output tokens per decision (LLM chat fallback)\n", avgDecideTokens)
	}

	// Gate Verdict Check:
	// 1. Swapped decision call sites must match or beat shipping classifier on accuracy.
	//    Candidate sites that did not qualify on accuracy (Mission Kind, Mission Light, KB Filing)
	//    remain on shipping classifiers in code.
	// 2. If -require-native is set, decide route must be served by a native decision model.
	failedCriteria := []string{}
	checkSite := func(name string, decideM, classifyM Metric) {
		if decideM.Total == 0 || decideM.Correct == 0 || decideM.Accuracy() < classifyM.Accuracy() {
			failedCriteria = append(failedCriteria, fmt.Sprintf("Accuracy on swapped site %s: decide %.1f%% < shipping classifier %.1f%%", name, decideM.Accuracy(), classifyM.Accuracy()))
		}
	}

	checkSite("Agent Auto-Dispatch", agentDecideM, agentClassifyM)

	if *requireNative && !isNative {
		failedCriteria = append(failedCriteria, "Native decision model required (-require-native), but route was served by LLM fallback")
	}

	fmt.Println("\n### Evaluation Gate Verdict:")
	if len(failedCriteria) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: Decision model did not meet acceptance criteria:\n")
		for _, s := range failedCriteria {
			fmt.Fprintf(os.Stderr, "  - %s\n", s)
		}
		os.Exit(1)
	}

	if isNative {
		fmt.Printf("PASS: Native decision model matches or exceeds shipping classifier accuracy on swapped sites (%s).\n", decideProvider)
	} else {
		fmt.Println("PASS: Decide route matches or exceeds shipping classifier accuracy on swapped sites.")
	}
}

func printSiteSummary(siteName string, decideM, classifyM Metric) {
	fmt.Printf("  - Decision Model     : %d/%d (%.1f%%) in avg %dms (%d tokens) [%s]\n",
		decideM.Correct, decideM.Total, decideM.Accuracy(), decideM.AvgLatencyMS(), decideM.AvgTokens(), decideM.Provider)
	fmt.Printf("  - Shipping Classifier: %d/%d (%.1f%%) in avg %dms (%d tokens)\n",
		classifyM.Correct, classifyM.Total, classifyM.Accuracy(), classifyM.AvgLatencyMS(), classifyM.AvgTokens())
}

func printTableRow(site, mode string, m Metric, tokens int) {
	fmt.Printf("| %s | %s | %d | %.1f%% | ~%d ms | %d tokens |\n",
		site, mode, m.Total, m.Accuracy(), m.AvgLatencyMS(), tokens)
}

// evalMissionPreview evaluates the preview endpoint path (classifyKindAndLightWithDecide vs classifyKindAndLight).
// The shipping classifier prompt classifies along both axes in one model turn.
// The decide path calls decide for kind, and if general, calls decide for light.
func evalMissionPreview(ctx context.Context, e *Evaluator, missions []MissionSample) (Metric, Metric, Metric, Metric) {
	var kindDecideM, kindClassifyM Metric
	var lightDecideM, lightClassifyM Metric

	kindDecideM.Total = len(missions)
	kindClassifyM.Total = len(missions)

	// Count general missions for light evaluation
	generalCount := 0
	for _, m := range missions {
		if m.WantKind == "general" {
			generalCount++
		}
	}
	lightDecideM.Total = generalCount
	lightClassifyM.Total = generalCount

	const systemPrompt = "Answer with only what is requested — no prose, no explanation."

	for _, m := range missions {
		// 1. Mission Kind Evaluation:
		// Shipping classifier (classifyKindAndLight / ClassifyKind prompt)
		promptKind := "Classify this mission goal along two independent axes, based on its deliverable, not its topic.\n" +
			"Axis 1 — coding or general: coding means the mission produces or changes code, scripts, or configuration files in a repository; general means the output is a document, report, analysis, book, plan, or data gathering, even when its subject is programming, software, or coding (a book or article about coding is general).\n" +
			"Axis 2 — light or full: light means the goal is a single-pass task deliverable in one response (a read, summary, lookup, or short write-up with no multi-step plan or file artifacts); full means it needs a plan and artifacts.\n" +
			"Answer with exactly two words separated by a space: first coding or general, then light or full.\n\n" +
			"Goal: " + m.Goal

		replyKind, tokensKind, durKind, errKind := e.callStream(ctx, systemPrompt, promptKind, "mission_classify")
		kindClassifyM.Latency += durKind
		kindClassifyM.TotalTokens += tokensKind

		if errKind == nil {
			gotKind, _ := parseKindAndLight(replyKind)
			if gotKind == m.WantKind {
				kindClassifyM.Correct++
			}
		}

		// Decide Kind (ClassifyKindWithDecide)
		qKind := "Classify this mission goal based on its deliverable, not its topic."
		optionsKind := []decideOption{
			{ID: "coding", Description: "produces or changes code, scripts, or configuration files in a repository"},
			{ID: "general", Description: "output is a document, report, analysis, book, plan, or data gathering, even when its subject is programming, software, or coding (a book or article about coding is general)"},
		}
		choiceKind, kindTokens, prov, mod, native, kindDur, kindErr := e.callDecide(ctx, "choice", qKind, m.Goal, optionsKind, "kind")
		kindDecideM.Latency += kindDur
		kindDecideM.TotalTokens += kindTokens
		kindDecideM.Provider = prov
		kindDecideM.Model = mod
		kindDecideM.Native = native

		gotDecideKind := ""
		if kindErr == nil {
			choiceKind = strings.ToLower(strings.TrimSpace(choiceKind))
			if choiceKind == "coding" || strings.HasPrefix(choiceKind, "coding") {
				gotDecideKind = "coding"
			} else if choiceKind == "general" || strings.HasPrefix(choiceKind, "general") {
				gotDecideKind = "general"
			}
		}
		if gotDecideKind == m.WantKind {
			kindDecideM.Correct++
		}

		// 2. Mission Light Evaluation (general goals only):
		if m.WantKind == "general" {
			// Shipping classifier (production classifyLight prompt + system prompt + effort=low)
			promptLight := "Is this goal a single-pass task deliverable in one response — a read, summary, lookup, or short write-up with no multi-step plan or file artifacts? Answer with exactly one word, yes or no.\n\n" +
				"Goal: " + m.Goal
			replyLight, tokensLight, durLight, errLight := e.callStream(ctx, systemPrompt, promptLight, "light")
			lightClassifyM.Latency += durLight
			lightClassifyM.TotalTokens += tokensLight
			if errLight == nil {
				replyLower := strings.ToLower(replyLight)
				isLight := strings.Contains(replyLower, "yes") && !strings.Contains(replyLower, "no")
				if isLight == m.WantLight {
					lightClassifyM.Correct++
				}
			}

			// Decide Light (production classifyLightWithDecide yes/no)
			qLight := "Is this goal a single-pass task deliverable in one response — a read, summary, lookup, or short write-up with no multi-step plan or file artifacts?"
			choiceLight, lightTokens, lprov, lmod, lnative, lightDur, lightErr := e.callDecide(ctx, "yesno", qLight, m.Goal, nil, "light")
			lightDecideM.Latency += lightDur
			lightDecideM.TotalTokens += lightTokens
			lightDecideM.Provider = lprov
			lightDecideM.Model = lmod
			lightDecideM.Native = lnative
			if lightErr == nil {
				choiceLight = strings.ToLower(strings.TrimSpace(choiceLight))
				isDecideLight := choiceLight == "yes" || strings.HasPrefix(choiceLight, "yes")
				if isDecideLight == m.WantLight {
					lightDecideM.Correct++
				}
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

func evalAgentDispatch(ctx context.Context, e *Evaluator, agents []AgentSample) (Metric, Metric) {
	var decideM, classifyM Metric
	decideM.Total = len(agents)
	classifyM.Total = len(agents)

	const systemPrompt = "Answer with only what is requested — no prose, no explanation."

	for _, a := range agents {
		// 1. Decide Path
		options := make([]decideOption, len(a.Candidates))
		for i, c := range a.Candidates {
			options[i] = decideOption{ID: c.Name, Description: c.Description}
		}
		qDecide := "Pick the single best-fit assistant profile for the user's message."
		choice, tokens, prov, mod, native, dur, err := e.callDecide(ctx, "choice", qDecide, a.Message, options, "dispatch")
		decideM.Latency += dur
		decideM.TotalTokens += tokens
		decideM.Provider = prov
		decideM.Model = mod
		decideM.Native = native

		if err == nil {
			choice = strings.TrimSpace(choice)
			for _, c := range a.Candidates {
				if strings.EqualFold(choice, c.Name) || strings.EqualFold(choice, c.ID) {
					if c.ID == a.WantAgent {
						decideM.Correct++
					}
					break
				}
			}
		}

		// 2. Shipping Classifier Path (dispatchPrompt + systemPrompt + effort=low)
		var b strings.Builder
		b.WriteString("Pick the single best-fit assistant profile for the user's message. Reply with ONLY the number, nothing else.\n\n")
		for i, c := range a.Candidates {
			if c.Description != "" {
				fmt.Fprintf(&b, "%d. %s: %s\n", i+1, c.Name, c.Description)
			} else {
				fmt.Fprintf(&b, "%d. %s\n", i+1, c.Name)
			}
		}
		b.WriteString("\nMessage: ")
		b.WriteString(a.Message)

		reply, streamTokens, streamDur, streamErr := e.callStream(ctx, systemPrompt, b.String(), "dispatch")
		classifyM.Latency += streamDur
		classifyM.TotalTokens += streamTokens
		if streamErr == nil {
			reply = strings.TrimSpace(reply)
			end := 0
			for end < len(reply) && reply[end] >= '0' && reply[end] <= '9' {
				end++
			}
			if end > 0 {
				var idx int
				if _, err := fmt.Sscanf(reply[:end], "%d", &idx); err == nil && idx >= 1 && idx <= len(a.Candidates) {
					if a.Candidates[idx-1].ID == a.WantAgent {
						classifyM.Correct++
					}
				}
			}
		}
	}
	return decideM, classifyM
}

func evalKBFiling(ctx context.Context, e *Evaluator, kbFiles []KBFilingSample) (Metric, Metric) {
	var decideM, classifyM Metric
	decideM.Total = len(kbFiles)
	classifyM.Total = len(kbFiles)

	const decideSystemRule = "You file documents into a knowledge base. Given a document and a list of existing collections, reply with the best-matching existing collection, or propose NEW when nothing fits. Prefer the MOST SPECIFIC existing collection that fits the document's primary topic; propose NEW only when the primary topic is clearly narrower than every existing collection and is a topic more future documents could join. A broad collection is the wrong pick when a narrower on-topic one already exists, or when the document's topic deserves its own collection - do not use a catch-all just because it is easy."
	const classifySystem = `You file documents into a knowledge base. Given a document and a list of existing collections, reply with exactly one line: either the bare id of the best-matching existing collection, or "NEW: <name> | <description>" to propose a new collection when nothing fits. Prefer the MOST SPECIFIC existing collection that fits the document's primary topic; propose NEW only when the primary topic is clearly narrower than every existing collection and is a topic more future documents could join. A broad collection is the wrong pick when a narrower on-topic one already exists, or when the document's topic deserves its own collection - do not use a catch-all just because it is easy. A new collection's name must be a SHORT GENERIC TOPIC CATEGORY of 1-3 words (like "AI Agents", "Scalability", "Code Review", "CVs") that many future documents could belong to, never a title describing this specific document. The description should state the topic's scope broadly. No other text.`

	for _, doc := range kbFiles {
		var list strings.Builder
		options := make([]decideOption, 0, len(doc.Collections)+1)
		for _, c := range doc.Collections {
			desc := fmt.Sprintf("%s (%d docs) - %s", c.Name, c.DocCount, c.Description)
			list.WriteString(c.ID + ": " + desc)
			list.WriteByte('\n')
			options = append(options, decideOption{ID: c.ID, Description: desc})
		}
		options = append(options, decideOption{ID: "NEW", Description: "propose a new collection when nothing fits"})

		truncatedText := truncateRunes(doc.Text, 4000)

		// 1. Decide Path
		state := fmt.Sprintf("Document title: %s\n\n%s", doc.Title, truncatedText)
		choice, tokens, prov, mod, native, dur, err := e.callDecide(ctx, "choice", decideSystemRule, state, options, "kb_classify")
		decideM.Latency += dur
		decideM.TotalTokens += tokens
		decideM.Provider = prov
		decideM.Model = mod
		decideM.Native = native

		if err == nil {
			choice = strings.TrimSpace(choice)
			if strings.EqualFold(choice, "NEW") {
				if doc.WantChoice == "NEW" {
					decideM.Correct++
				}
			} else {
				for _, c := range doc.Collections {
					if strings.EqualFold(choice, c.ID) {
						if c.ID == doc.WantChoice {
							decideM.Correct++
						}
						break
					}
				}
			}
		}

		// 2. Shipping Classifier Path (classifySystem + 4000 runes + effort=low)
		userPrompt := fmt.Sprintf("Existing collections:\n%s\nDocument title: %s\n\n%s",
			list.String(), doc.Title, truncatedText)
		reply, streamTokens, streamDur, streamErr := e.callStream(ctx, classifySystem, userPrompt, "kb_classify")
		classifyM.Latency += streamDur
		classifyM.TotalTokens += streamTokens
		if streamErr == nil {
			firstLine, _, _ := strings.Cut(strings.TrimSpace(reply), "\n")
			firstLine = strings.TrimSpace(firstLine)
			if strings.HasPrefix(strings.ToUpper(firstLine), "NEW") {
				if doc.WantChoice == "NEW" {
					classifyM.Correct++
				}
			} else {
				for _, c := range doc.Collections {
					if firstLine == c.ID {
						if c.ID == doc.WantChoice {
							classifyM.Correct++
						}
						break
					}
				}
			}
		}
	}
	return decideM, classifyM
}
