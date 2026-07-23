package missions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

const (
	reviewVerdictToolName = "review_verdict"
	baselineDiffCap       = 256 << 10
	baselineDiffTimeout   = 30 * time.Second
)

// ReviewVerdictTool defines the reviewer's one tool call. The
// reviewer's system prompt instructs it to REFUTE — approval is the
// fallthrough the driver takes only on an explicit approve, never the
// default read of an ambiguous response.
func ReviewVerdictTool() provider.ToolDef {
	return provider.ToolDef{
		Name:        reviewVerdictToolName,
		Description: "Report your review verdict. Call this exactly once. Look for reasons to reject before approving — approve only when you cannot find a real gap.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"decision": {
					"type": "string",
					"enum": ["approve", "rework"],
					"description": "approve only if the unit's evidence and diff hold up under scrutiny."
				},
				"findings": {
					"type": "array",
					"description": "Required for rework: what's wrong, one entry per distinct gap.",
					"items": {
						"type": "object",
						"properties": {
							"title": {"type": "string", "description": "One-line summary of the gap."},
							"file": {"type": "string", "description": "The file the gap is in, if applicable."},
							"detail": {"type": "string", "description": "Why this is a gap and what would fix it."}
						},
						"required": ["title"]
					}
				}
			},
			"required": ["decision"]
		}`),
	}
}

// Finding is one reviewer-reported gap.
type Finding struct {
	Title  string `json:"title"`
	File   string `json:"file"`
	Detail string `json:"detail"`
}

// ReviewVerdict is the parsed review_verdict call.
type ReviewVerdict struct {
	Approved bool
	Findings []Finding
}

// parseReviewVerdict decodes a review_verdict tool call's arguments.
func parseReviewVerdict(args json.RawMessage) (ReviewVerdict, error) {
	var raw struct {
		Decision string    `json:"decision"`
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal(args, &raw); err != nil {
		return ReviewVerdict{}, err
	}
	return ReviewVerdict{Approved: raw.Decision == "approve", Findings: raw.Findings}, nil
}

// GapFingerprint is sha256 of sorted, normalized (title, file) pairs,
// EXCLUDING free-text detail — the same defect described in different
// words still collides, which is what makes stall detection (two
// consecutive IDENTICAL-fingerprint reworks) meaningful. Empty
// findings -> empty string, never a hash of nothing.
func GapFingerprint(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	keys := make([]string, len(findings))
	for i, f := range findings {
		keys[i] = strings.ToLower(strings.TrimSpace(f.Title)) + "|" + strings.ToLower(strings.TrimSpace(f.File))
	}
	sort.Strings(keys)
	h := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(h[:])
}

// BaselineDiff runs `git diff <baseCommit>` in the mission's own
// worktree — the reviewer judges the actual diff, never the worker's
// account of it — capped at baselineDiffCap with a truncation marker
// that never splits mid-line.
func BaselineDiff(ctx context.Context, worktree, baseCommit string) (string, error) {
	if baseCommit == "" || baseCommit == unavailableCommit {
		return "", fmt.Errorf("review: no base commit recorded for this worktree")
	}
	cctx, cancel := context.WithTimeout(ctx, baselineDiffTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "diff", baseCommit) //nolint:gosec // baseCommit is a git commit hash captured by our own Provision, not user input
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("review: git diff: %w", err)
	}
	if len(out) <= baselineDiffCap {
		return string(out), nil
	}
	cut := lastNewlineBefore(out, baselineDiffCap)
	return string(out[:cut]) + fmt.Sprintf("\n[truncated at %d bytes: diff is %d bytes]", cut, len(out)), nil
}

// lastNewlineBefore returns the index of the last '\n' at or before
// limit, so truncation never splits a line — falls back to limit
// itself if no newline exists that early.
func lastNewlineBefore(b []byte, limit int) int {
	if limit > len(b) {
		limit = len(b)
	}
	for i := limit - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return i
		}
	}
	return limit
}
