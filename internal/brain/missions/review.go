package missions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
}

// ReadArtifacts reads each declared artifact from workRoot, capped at
// reviewArtifactsCap TOTAL across files; a file that would blow the
// remaining budget is truncated with an honest marker. Missing or
// unreadable files map to an error note rather than being dropped —
// the reviewer must see the gap, not silently less material.
func ReadArtifacts(workRoot string, artifacts []string) map[string]string {
	if len(artifacts) == 0 {
		return nil
	}
	out := make(map[string]string, len(artifacts))
	remaining := reviewArtifactsCap
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
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "verdict recorded", nil
		},
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
