package missions

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// missionStatusToolName is the one tool call a worker turn must end
// with — DONE requests verification (never itself a completion
// claim), RETRY reports failure analysis, BLOCKED asks an exact
// question.
const missionStatusToolName = "mission_status"

// MissionStatusTool defines the worker's end-of-turn sentinel call —
// registered per-turn via loop.Request.ExtraTools, never in the shared
// agent tool surface, so chat sessions never see it. Execute itself
// does nothing but acknowledge the call; the driver reads the actual
// verdict from the tool call's captured arguments (runner.go), not
// from this return value.
func MissionStatusTool() *tools.Tool {
	return &tools.Tool{
		Name:        missionStatusToolName,
		Description: "Report this turn's outcome. Call this exactly once, as your final action. DONE means you believe the current unit is complete and ready for the harness to verify and review — it is a request for verification, not a claim that the work is accepted. RETRY means you hit a problem and are explaining what you learned before the next attempt. BLOCKED means you need a specific answer from the user before you can continue.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"outcome": {
					"type": "string",
					"enum": ["done", "retry", "blocked"],
					"description": "This turn's outcome."
				},
				"evidence": {
					"type": "string",
					"description": "Required for done: what you did and how it can be verified."
				},
				"analysis": {
					"type": "string",
					"description": "Required for retry: what went wrong and what you'll try next."
				},
				"question": {
					"type": "string",
					"description": "Required for blocked: the exact question the user must answer."
				},
				"handoff": {
					"type": "string",
					"description": "Optional: a concise note to the next worker session — current state of the work, what remains, and any gotchas discovered. This is the ONLY context the next session receives besides the plan and git log, so include anything it must know."
				}
			},
			"required": ["outcome"]
		}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "status recorded", nil
		},
	}
}

// planToolName is the planner's end-of-turn sentinel call — forcing
// the plan through a tool call (like mission_status and review_verdict
// already do) instead of asking the model to reply with bare JSON
// prose eliminates the fence-stripping fragility that caused plans to
// fail on stray prose or wrapping the model's provider doesn't emit
// consistently.
const planToolName = "submit_plan"

// PlanTool defines the planner's one tool call — registered per-turn
// via loop.Request.ExtraTools, never in the shared agent tool surface.
func PlanTool() *tools.Tool {
	return &tools.Tool{
		Name:        planToolName,
		Description: "Submit the mission plan. Call this exactly once, as your final action.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"units": {
					"type": "array",
					"description": "The smallest ordered list of verifiable units that achieves the goal.",
					"items": {
						"type": "object",
						"properties": {
							"title": {"type": "string", "description": "One-line summary of the unit."},
							"artifacts": {
								"type": "array",
								"items": {"type": "string"},
								"description": "Workspace-relative file path(s) this unit must produce. Required: at least one. Files only — the harness rejects directories."
							},
							"verify_cmd": {
								"type": "string",
								"description": "A real POSIX shell command, run as /bin/sh -c \"<verify_cmd>\" in the mission's workspace, that checks the CONTENT of the artifacts."
							}
						},
						"required": ["title", "artifacts", "verify_cmd"]
					}
				}
			},
			"required": ["units"]
		}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "plan recorded", nil
		},
	}
}

// exploreNotesToolName is the explore phase's end-of-turn sentinel
// call — advisory, unlike mission_status: a turn that never produces
// it degrades to the raw turn text rather than failing the phase (see
// runner.go's ExploreSession).
const exploreNotesToolName = "explore_notes"

// ExploreNotesTool defines the explorer's one tool call —
// registered per-turn via loop.Request.ExtraTools, never in the shared
// agent tool surface.
func ExploreNotesTool() *tools.Tool {
	return &tools.Tool{
		Name:        exploreNotesToolName,
		Description: "Report your exploration findings. Call this exactly once, as your final action.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"findings": {
					"type": "string",
					"description": "Everything the planner needs: what exists, what's relevant, constraints, unknowns."
				}
			},
			"required": ["findings"]
		}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "findings recorded", nil
		},
	}
}

// parseExploreFindings decodes an explore_notes tool call's arguments.
func parseExploreFindings(args json.RawMessage) (string, error) {
	var v struct {
		Findings string `json:"findings"`
	}
	if err := json.Unmarshal(args, &v); err != nil {
		return "", err
	}
	return v.Findings, nil
}

// WorkerVerdict is the parsed mission_status call.
type WorkerVerdict struct {
	Outcome  string // done | retry | blocked
	Evidence string
	Analysis string
	Question string
	Handoff  string
	// Forced marks a verdict the runner fabricated because NEITHER a
	// tool call NOR a text-form sentinel (extractTextSentinel) could be
	// found after the recovery re-run — set true ONLY on that path
	// (runner.go's RunWorker). The driver fingerprints this case
	// distinctly (GapFingerprint "no_sentinel") so the stall brake
	// pauses after StallRounds consecutive sentinel-less turns instead
	// of burning to max_iterations on a model that never learns to call
	// the tool.
	Forced bool
	// SeenURLs is every URL the worker actually observed this turn via
	// web_fetch/web_search (D-059) — nativeRunner's own runTurn evidence,
	// never model-reported. Empty for the delegated (CLI harness) path,
	// which has no stream to observe; citations verification is native-
	// runner-only for now.
	SeenURLs []string
	// FinalMessage is the assistant text written since the last
	// non-sentinel tool call (nativeRunner's runTurn) — the deliverable
	// for a light mission (D-069), as opposed to the full turn text
	// (RunWorker's second return), which includes every intermediate
	// tool-retry narration across the whole session. Never populated by
	// the mission_status tool call's own JSON args; set by RunWorker
	// after parsing. Empty for the delegated (CLI harness) path.
	FinalMessage string `json:"-"`
}

// parseWorkerVerdict decodes a mission_status tool call's arguments.
func parseWorkerVerdict(args json.RawMessage) (WorkerVerdict, error) {
	var v WorkerVerdict
	if err := json.Unmarshal(args, &v); err != nil {
		return WorkerVerdict{}, err
	}
	return v, nil
}

// sentinelAttrs lists the known attribute/field names extractTextSentinel
// will pull out of a text-form sentinel, per tool — anything else in
// the tag or JSON object is ignored rather than round-tripped, since
// the only fields WorkerVerdict/ReviewVerdict-parsing code ever reads
// are these.
var sentinelAttrs = map[string][]string{
	missionStatusToolName: {"outcome", "evidence", "analysis", "question", "handoff"},
	reviewVerdictToolName: {"decision"},
	exploreNotesToolName:  {"findings"},
}

// sentinelDiscriminator names the field whose value is validated
// against sentinelDiscriminatorValues before a text-form match is
// trusted — the same field the tool's own JSON schema marks required.
var sentinelDiscriminator = map[string]string{
	missionStatusToolName: "outcome",
	reviewVerdictToolName: "decision",
	exploreNotesToolName:  "findings",
}

// sentinelDiscriminatorValues enumerates the valid values for a
// discriminator field with a fixed enum (mission_status's outcome,
// review_verdict's decision). explore_notes' discriminator (findings)
// is free text, not an enum — its absence here means extractTextSentinel
// falls back to a plain non-empty check for that tool instead of an
// enum membership test.
var sentinelDiscriminatorValues = map[string]map[string]bool{
	missionStatusToolName: {"done": true, "retry": true, "blocked": true},
	reviewVerdictToolName: {"approve": true, "rework": true},
}

// init guards the three maps' convention: every tool named in
// sentinelDiscriminator must also have an entry in sentinelAttrs, or
// extractTextSentinel bails silently for that tool with no signal a
// tool was ever added incompletely. sentinelDiscriminatorValues is
// deliberately NOT checked the same way — explore_notes has no enum
// (see its doc comment above) and is expected to be absent from it.
func init() {
	for tool := range sentinelDiscriminator {
		if _, ok := sentinelAttrs[tool]; !ok {
			panic(fmt.Sprintf("sentinel.go: %q registered in sentinelDiscriminator but missing from sentinelAttrs", tool))
		}
	}
}

// xmlTagPattern matches an XML-ish sentinel tag: <toolName attr="val"
// attr2='val2' ... /> or <toolName ...>, attribute values in either
// quote style, attributes in any order. %s is the exact tool name —
// callers build one pattern per tool so an unrelated tag never matches.
const xmlTagPatternFmt = `<%s\b((?:\s+[a-zA-Z_][a-zA-Z0-9_]*\s*=\s*(?:"[^"]*"|'[^']*'))*)\s*/?>`

// attrPattern pulls one name="value" or name='value' pair out of a
// matched tag's attribute string.
var attrPattern = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*(?:"([^"]*)"|'([^']*)')`)

// extractTextSentinel scans model text for a text-form end-of-turn
// sentinel when the tool call itself never arrived — observed on
// non-frontier models (GLM-5.2, qwen3:30b) that express the same
// intent as prose instead of a structured call. Two forms are
// recognized:
//
//  1. An XML-ish tag named exactly toolName, self-closing or not, with
//     known attributes: `<mission_status outcome="done" .../>`.
//  2. The tool name as a bare token followed by a JSON object (or a
//     ```json fenced one) whose fields are read directly, e.g.
//     `mission_status\n{"outcome":"done",...}`.
//
// When a form matches more than once, the LAST valid occurrence wins —
// models sometimes repeat or revise the tag within one reply, and the
// final one is the turn's actual verdict. Returns ok=false when
// nothing recognizable — carrying the required discriminator field
// with a valid enum value — is found.
//
// Trust note: a text-form sentinel is trust-equivalent to the tool-call
// form — both are the model's own self-report of its own turn, neither
// is verified here. The harness's own evidence (CheckArtifacts,
// verify_cmd, RunVerify) is what actually gates a unit's Passes flag;
// this function only saves a turn from being misread as "said nothing"
// when it in fact reported an outcome in the wrong shape.
func extractTextSentinel(text, toolName string) (json.RawMessage, bool) {
	knownAttrs := sentinelAttrs[toolName]
	discriminator := sentinelDiscriminator[toolName]
	if discriminator == "" || len(knownAttrs) == 0 {
		return nil, false
	}
	// A tool with no enum entry here (explore_notes) has a free-text
	// discriminator: any non-empty value is valid, rather than membership
	// in a fixed set.
	validValues, hasEnum := sentinelDiscriminatorValues[toolName]
	isValid := func(v string) bool {
		if !hasEnum {
			return v != ""
		}
		return validValues[v]
	}

	var best map[string]string
	bestPos := -1

	// Form 1: XML-ish tag.
	tagPattern := regexp.MustCompile(fmt.Sprintf(xmlTagPatternFmt, regexp.QuoteMeta(toolName)))
	for _, m := range tagPattern.FindAllStringSubmatchIndex(text, -1) {
		attrsText := text[m[2]:m[3]]
		fields := map[string]string{}
		for _, am := range attrPattern.FindAllStringSubmatch(attrsText, -1) {
			name := am[1]
			val := am[2]
			if am[3] != "" {
				val = am[3]
			}
			fields[name] = val
		}
		if v, ok := fields[discriminator]; ok && isValid(v) && m[0] > bestPos {
			best, bestPos = fields, m[0]
		}
	}

	// Form 2: bare token followed by a JSON object, or a ```json fenced
	// one — position-compared against form 1 so whichever form's match
	// starts LATER in the text wins overall (models sometimes repeat or
	// revise the sentinel; the final occurrence is the turn's actual
	// verdict, regardless of which form it's expressed in).
	tokenPattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(toolName) + `\b\s*:?\s*(?:` + "```(?:json)?" + `)?\s*`)
	for _, loc := range tokenPattern.FindAllStringIndex(text, -1) {
		rest := text[loc[1]:]
		start := strings.IndexByte(rest, '{')
		if start == -1 || (start > 0 && strings.TrimSpace(rest[:start]) != "") {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(rest[start:]))
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			continue
		}
		v, ok := obj[discriminator].(string)
		if !ok || !isValid(v) {
			continue
		}
		if loc[0] <= bestPos {
			continue
		}
		fields := map[string]string{}
		for _, attr := range knownAttrs {
			if s, ok := obj[attr].(string); ok {
				fields[attr] = s
			}
		}
		best, bestPos = fields, loc[0]
	}

	if best == nil {
		return nil, false
	}
	raw, err := json.Marshal(best)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(raw), true
}

// bailPatterns are the known-weak stopgap layer for detecting a
// premature stop when the sentinel tool call is missing entirely.
// Intentionally NOT treated as load-bearing — a bail match and a
// bail miss both end in a forced RETRY either way; this only affects
// what gets logged, never whether the turn is trusted at face value.
var bailPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ready for review`),
	regexp.MustCompile(`(?i)i'll check back later`),
	regexp.MustCompile(`(?i)unable to proceed`),
	regexp.MustCompile(`(?i)let me know (if|when|how) you'd like`),
	regexp.MustCompile(`(?i)i('ll| will) (wait|pause) for`),
	regexp.MustCompile(`(?i)please (let me know|confirm|advise)`),
	regexp.MustCompile(`(?i)i('m| am) (done|finished) for now`),
	regexp.MustCompile(`(?i)this (should|ought to) (work|be sufficient)`),
	regexp.MustCompile(`(?i)i believe (this|that) (is|should be) complete`),
}

// detectBail scans the last paragraph of a worker's final text for
// premature-stop phrasing, used only when the sentinel call is
// missing entirely (after one recovery re-run already failed to elicit
// it).
func detectBail(text string) bool {
	paras := strings.Split(strings.TrimSpace(text), "\n\n")
	last := paras[len(paras)-1]
	for _, p := range bailPatterns {
		if p.MatchString(last) {
			return true
		}
	}
	return false
}

// neutralizePattern matches the exact framing sequences a self-
// injection attempt would need — model-produced text (progress notes,
// git log messages, review findings) that re-enters a LATER prompt is
// passed through NeutralizeSlot first.
var neutralizePattern = regexp.MustCompile(`</system|<system|\{\{`)

// NeutralizeSlot breaks zero-width-space sequences of </system,
// <system, {{ inside model-produced text — prompt-injection-via-self
// hardening. The zero-width space is invisible when rendered but
// breaks exact-string matching against these framing sequences.
func NeutralizeSlot(s string) string {
	return neutralizePattern.ReplaceAllStringFunc(s, func(match string) string {
		var b strings.Builder
		for i, r := range match {
			if i > 0 {
				b.WriteRune('​')
			}
			b.WriteRune(r)
		}
		return b.String()
	})
}
