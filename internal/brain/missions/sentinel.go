package missions

import (
	"context"
	"encoding/json"
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
				}
			},
			"required": ["outcome"]
		}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "status recorded", nil
		},
	}
}

// WorkerVerdict is the parsed mission_status call.
type WorkerVerdict struct {
	Outcome  string // done | retry | blocked
	Evidence string
	Analysis string
	Question string
}

// parseWorkerVerdict decodes a mission_status tool call's arguments.
func parseWorkerVerdict(args json.RawMessage) (WorkerVerdict, error) {
	var v WorkerVerdict
	if err := json.Unmarshal(args, &v); err != nil {
		return WorkerVerdict{}, err
	}
	return v, nil
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
