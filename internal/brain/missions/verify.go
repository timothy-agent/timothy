package missions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
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
