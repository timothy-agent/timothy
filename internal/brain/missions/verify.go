package missions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// verifyTimeout bounds one plan unit's verify_cmd.
const verifyTimeout = 10 * time.Minute

// verifyExcerptCap is the trailing slice of output kept alongside the
// full digest — enough to see what failed without storing megabytes.
const verifyExcerptCap = 2 << 10

// VerifyResult is a plan unit's verification evidence. Only RunVerify
// produces this — never model output — and only this evidence may
// flip a PlanUnit's Passes flag.
type VerifyResult struct {
	ExitCode     int
	OutputSHA256 string
	Excerpt      string
	Passed       bool
}

// RunVerify executes a plan unit's verify_cmd via /bin/sh -c in the
// work root. Evidence recorded: exit code, sha256 digest of the full
// output, and a trailing excerpt — "done is auditable from events
// alone."
func RunVerify(ctx context.Context, workRoot, verifyCmd string) (VerifyResult, error) {
	cctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "/bin/sh", "-c", verifyCmd) //nolint:gosec // verify_cmd is operator-authored plan content, not user input
	cmd.Dir = workRoot
	out, runErr := cmd.CombinedOutput()

	exitCode := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			return VerifyResult{}, runErr // context deadline, command not found, etc.
		}
	}

	digest := sha256.Sum256(out)
	excerpt := out
	if len(excerpt) > verifyExcerptCap {
		excerpt = excerpt[len(excerpt)-verifyExcerptCap:]
	}
	return VerifyResult{
		ExitCode:     exitCode,
		OutputSHA256: hex.EncodeToString(digest[:]),
		Excerpt:      string(excerpt),
		Passed:       exitCode == 0,
	}, nil
}

// CheckArtifacts verifies each declared workspace-relative artifact
// path exists under workRoot and is non-empty, returning a
// human-readable problem per failing path. This deterministic check
// runs BEFORE verify_cmd and is the harness's own evidence — a plan
// whose verify_cmd is a tautology still cannot pass a unit whose
// artifact was never written. A path that escapes workRoot (absolute,
// or climbing out via ..) is reported as a problem, never resolved.
func CheckArtifacts(workRoot string, artifacts []string) []string {
	var problems []string
	for _, a := range artifacts {
		rel := strings.TrimSpace(a)
		if rel == "" {
			continue
		}
		if filepath.IsAbs(rel) {
			problems = append(problems, fmt.Sprintf("%s: artifact paths must be relative to the workspace", rel))
			continue
		}
		cleaned := filepath.Clean(rel)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			problems = append(problems, fmt.Sprintf("%s: artifact path escapes the workspace", rel))
			continue
		}
		info, err := os.Stat(filepath.Join(workRoot, cleaned))
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s: not found in the workspace", rel))
		case info.IsDir():
			problems = append(problems, fmt.Sprintf("%s: is a directory, expected a file", rel))
		case info.Size() == 0:
			problems = append(problems, fmt.Sprintf("%s: exists but is empty", rel))
		}
	}
	return problems
}
