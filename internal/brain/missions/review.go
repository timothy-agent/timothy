package missions

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	reviewVerdictToolName = "review_verdict"
	baselineDiffCap       = 256 << 10
	baselineDiffTimeout   = 30 * time.Second

	// reviewArtifactsCap bounds the total artifact bytes a reviewer
	// sees — enough for real research/config artifacts, small enough
	// to never balloon the review turn.
	reviewArtifactsCap = 32 << 10
	// reviewListingCap bounds the workspace file listing.
	reviewListingCap = 2 << 10
)

// ReviewPacket is everything a reviewer turn judges: the mission's
// goal and plan (an earlier failure mode was reviewers rejecting with
// "missing original mission goal"), the ACTUAL artifact contents read
// by the harness from the workspace (never the worker's description
// of them), the baseline diff when a worktree exists, and the
// worker's evidence text last.
type ReviewPacket struct {
	Goal      string
	UnitTitle string
	Plan      Plan
	Diff      string
	// Artifacts maps workspace-relative path -> file contents, read by
	// the harness (ReadArtifacts), capped at reviewArtifactsCap total.
	Artifacts map[string]string
	// Listing is a flat file listing of the workspace (name + size) so
	// the reviewer can see what exists even when a unit declares no
	// artifacts.
	Listing  string
	Evidence string
	// Progress is the mission's progress log, rendered so an operator
	// steering note posted mid-mission reaches the reviewer too: a
	// rework-triggering note ("skip the CSS polish") must not be invisible
	// to the round deciding whether the unit passes.
	Progress []ProgressNote
	// OpenFindings are the prior rounds' still-open findings (D-092):
	// the reviewer marks the ids it considers closed in resolved. This
	// replaces the old in-process reviewer session carry-over.
	OpenFindings []Finding
}

// ReadArtifacts reads each declared artifact from workRoot, capped at
// reviewArtifactsCap TOTAL across files; a file that would blow the
// remaining budget is truncated with an honest marker. Missing or
// unreadable files map to an error note rather than being dropped —
// the reviewer must see the gap, not silently less material.
func ReadArtifacts(workRoot string, artifacts []string) map[string]string {
	return ReadArtifactsCapped(workRoot, artifacts, reviewArtifactsCap)
}

// ReadArtifactsCapped is ReadArtifacts with the total byte cap as a
// parameter, so a reviewer round that overflowed the model's context
// can retry with a smaller one (see driver.go's reviewWithShrink).
func ReadArtifactsCapped(workRoot string, artifacts []string, limit int) map[string]string {
	if len(artifacts) == 0 {
		return nil
	}
	out := make(map[string]string, len(artifacts))
	remaining := limit
	for _, a := range artifacts {
		rel := strings.TrimSpace(a)
		if rel == "" {
			continue
		}
		cleaned := filepath.Clean(rel)
		if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			out[rel] = "[not readable: path escapes the workspace]"
			continue
		}
		abs := filepath.Join(workRoot, cleaned)
		if err := tools.WithinRoot(workRoot, abs); err != nil {
			if tools.IsViolation(err) {
				out[rel] = "[not readable: path escapes the workspace]"
			} else {
				out[rel] = "[not readable: cannot verify workspace root: " + err.Error() + "]"
			}
			continue
		}
		b, err := os.ReadFile(abs) //nolint:gosec // path is workspace-relative, cleaned, .. / absolute forms rejected above, and containment re-verified by tools.WithinRoot
		if err != nil {
			out[rel] = "[not readable: " + err.Error() + "]"
			continue
		}
		if len(b) > remaining {
			out[rel] = string(b[:remaining]) + fmt.Sprintf("\n[truncated: file is %d bytes, review cap reached]", len(b))
			remaining = 0
			continue
		}
		out[rel] = string(b)
		remaining -= len(b)
	}
	return out
}

// ListWorkspace returns a flat "path (size)" listing of regular files
// under workRoot, capped — the reviewer's map of what actually exists
// on disk, independent of what anyone claims.
func ListWorkspace(workRoot string) string {
	var b strings.Builder
	_ = filepath.WalkDir(workRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || b.Len() >= reviewListingCap {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(workRoot, path)
		if relErr != nil {
			return nil
		}
		info, infoErr := d.Info()
		size := int64(0)
		if infoErr == nil {
			size = info.Size()
		}
		fmt.Fprintf(&b, "%s (%d bytes)\n", rel, size)
		return nil
	})
	return b.String()
}

// ReviewVerdictTool defines the reviewer's one tool call — registered
// per-turn via loop.Request.ExtraTools, never in the shared agent tool
// surface. The reviewer's system prompt instructs it to REFUTE —
// approval is the fallthrough the driver takes only on an explicit
// approve, never the default read of an ambiguous response.
func ReviewVerdictTool() *tools.Tool {
	return &tools.Tool{
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
					"description": "Required for rework: what's wrong, one entry per distinct NEW gap. Do not repeat a prior-round finding that is still open; it stays open unless you list its id in resolved.",
					"items": {
						"type": "object",
						"properties": {
							"title": {"type": "string", "description": "One-line summary of the gap."},
							"file": {"type": "string", "description": "The file the gap is in, if applicable."},
							"detail": {"type": "string", "description": "Why this is a gap and what would fix it."},
							"severity": {"type": "string", "enum": ["blocking", "minor"], "description": "blocking (default) prevents approval; minor is advisory."}
						},
						"required": ["title"]
					}
				},
				"resolved": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Ids of prior-round findings (F1, F2, ...) this round's work closed."
				}
			},
			"required": ["decision"]
		}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "verdict recorded", nil
		},
	}
}

// Finding severities and statuses (D-092, issue #512).
const (
	SeverityBlocking = "blocking"
	SeverityMinor    = "minor"

	FindingOpen     = "open"
	FindingResolved = "resolved"
	FindingAccepted = "accepted"
)

// Finding is one reviewer-reported gap, tracked as mission state
// (missions.review_findings, D-092) rather than as event text. The
// reviewer supplies title/file/detail/severity; the harness owns ID,
// Unit, Status, RoundOpened and UntouchedRounds (statemachine.go's
// mergeFindings), so a reworded repeat never becomes a fresh finding.
type Finding struct {
	// ID is F1, F2, ... in order of first appearance, stable for the
	// mission's life; the reviewer names it in resolved.
	ID     string `json:"id,omitempty"`
	Unit   int    `json:"unit"`
	Title  string `json:"title"`
	File   string `json:"file"`
	Detail string `json:"detail"`
	// Severity is blocking (default when absent) or minor.
	Severity string `json:"severity,omitempty"`
	// Status is open, resolved, or accepted (operator won't-fix).
	Status      string `json:"status,omitempty"`
	RoundOpened int    `json:"round_opened,omitempty"`
	// UntouchedRounds counts consecutive worker turns since the finding
	// opened that never touched File; the id-based stall input.
	UntouchedRounds int `json:"untouched_rounds,omitempty"`
}

// Open reports whether f still needs work.
func (f Finding) Open() bool { return f.Status == FindingOpen }

// Blocking reports whether f prevents approval. An empty severity is
// blocking: the reviewer's default is the strict reading.
func (f Finding) Blocking() bool { return f.Severity != SeverityMinor }

// OpenFindings filters findings to those still open, preserving order.
func OpenFindings(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Open() {
			out = append(out, f)
		}
	}
	return out
}

// ReviewVerdict is the parsed review_verdict call.
type ReviewVerdict struct {
	Approved bool
	Findings []Finding
	// Resolved names prior-round finding ids the reviewer considers
	// closed.
	Resolved []string
	// Provider/Model (issue #507) are who served the review turn; set by
	// RunReview after parsing.
	Provider string
	Model    string
}

// parseReviewVerdict decodes a review_verdict tool call's arguments.
// An unknown severity is read as blocking, same as an absent one.
func parseReviewVerdict(args json.RawMessage) (ReviewVerdict, error) {
	var raw struct {
		Decision string    `json:"decision"`
		Findings []Finding `json:"findings"`
		Resolved []string  `json:"resolved"`
	}
	if err := json.Unmarshal(args, &raw); err != nil {
		return ReviewVerdict{}, err
	}
	for i := range raw.Findings {
		if raw.Findings[i].Severity != SeverityMinor {
			raw.Findings[i].Severity = SeverityBlocking
		}
	}
	return ReviewVerdict{Approved: raw.Decision == "approve", Findings: raw.Findings, Resolved: raw.Resolved}, nil
}

// baselineDiffExcludes are pathspecs excluded from the reviewer's
// baseline diff: lockfiles and build output carry no reviewable
// content and were the single biggest contributor to oversized review
// prompts.
var baselineDiffExcludes = []string{
	"**/package-lock.json", "**/yarn.lock", "**/pnpm-lock.yaml",
	"**/Cargo.lock", "**/poetry.lock", "**/composer.lock", "**/Gemfile.lock",
	"**/dist/**", "**/build/**", "**/node_modules/**", "**/vendor/**", "**/target/**",
	"**/*.min.js", "**/*.min.css", "**/*.map",
}

// baselineDiffExcludeTrailer marks a diff as having lockfiles/build
// output excluded, appended unconditionally to any non-empty diff,
// since detecting whether an exclusion actually matched anything isn't
// cheap enough to bother with.
const baselineDiffExcludeTrailer = "\n[diff excludes lockfiles, dist/, build/, node_modules/, vendor/, target/, minified and source-map files]"

// BaselineDiff runs `git diff <baseCommit>` in the mission's own
// worktree — the reviewer judges the actual diff, never the worker's
// account of it — capped at baselineDiffCap with a truncation marker
// that never splits mid-line.
func BaselineDiff(ctx context.Context, worktree, baseCommit string) (string, error) {
	return baselineDiffCapped(ctx, worktree, baseCommit, baselineDiffCap)
}

// baselineDiffCapped is BaselineDiff with the byte cap as a parameter,
// so a reviewer round that overflowed the model's context can retry
// with a smaller one (see driver.go's reviewWithShrink).
func baselineDiffCapped(ctx context.Context, worktree, baseCommit string, limit int) (string, error) {
	if baseCommit == "" || baseCommit == unavailableCommit {
		return "", fmt.Errorf("review: no base commit recorded for this worktree")
	}
	cctx, cancel := context.WithTimeout(ctx, baselineDiffTimeout)
	defer cancel()
	args := []string{"diff", baseCommit, "--", "."}
	for _, pattern := range baselineDiffExcludes {
		args = append(args, ":(exclude,glob)"+pattern)
	}
	cmd := exec.CommandContext(cctx, "git", args...) //nolint:gosec // baseCommit is a git commit hash captured by our own Provision, not user input
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("review: git diff: %w", err)
	}
	if len(out) == 0 {
		return "", nil
	}
	if len(out) <= limit {
		return string(out) + baselineDiffExcludeTrailer, nil
	}
	cut := lastNewlineBefore(out, limit)
	return string(out[:cut]) + fmt.Sprintf("\n[truncated at %d bytes: diff is %d bytes]", cut, len(out)) + baselineDiffExcludeTrailer, nil
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
