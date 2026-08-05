package missions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// verifyBackend runs verify_cmd via a sandbox container instead of
// brain's own process, streaming combined output to out and returning
// the exit code — RunVerifyWithBackend's counterpart to
// missionTools' shell Runner hook. err is non-nil only for an
// infrastructure failure (never a non-zero exit, which is a normal,
// evidence-bearing outcome reported via the exit code).
type verifyBackend func(ctx context.Context, workdir, command string, timeout time.Duration, out io.Writer) (exitCode int, err error)

// verifyTimeout bounds one plan unit's verify_cmd.
const verifyTimeout = 10 * time.Minute

// verifyExcerptCap is the trailing slice of output kept alongside the
// full digest — enough to see what failed without storing megabytes.
const verifyExcerptCap = 2 << 10

// VerifyResult is a plan unit's verification evidence. Only
// RunVerifyWithBackend produces this — never model output — and only
// this evidence may flip a PlanUnit's Passes flag.
type VerifyResult struct {
	ExitCode     int
	OutputSHA256 string
	Excerpt      string
	Passed       bool
}

// RunVerifyWithBackend executes a plan unit's verify_cmd via backend
// (the mission's sandbox container) in the work root. Output is
// streamed into a sha256 hash and a bounded tail buffer rather than
// collected in full — a verify_cmd with runaway output must not
// balloon memory. Evidence recorded: exit code, sha256 digest of the
// full output, and a trailing excerpt — "done is auditable from
// events alone."
func RunVerifyWithBackend(ctx context.Context, backend verifyBackend, workRoot, verifyCmd string) (VerifyResult, error) {
	cctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	hash := sha256.New()
	tail := &tailBuffer{max: verifyExcerptCap}
	exitCode, err := backend(cctx, workRoot, verifyCmd, verifyTimeout, io.MultiWriter(hash, tail))
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{
		ExitCode:     exitCode,
		OutputSHA256: hex.EncodeToString(hash.Sum(nil)),
		Excerpt:      tail.String(),
		Passed:       exitCode == 0,
	}, nil
}

// tailBuffer keeps only the last max bytes written to it — a bounded
// alternative to buffering a verify_cmd's entire (potentially huge)
// output just to keep its trailing excerpt.
type tailBuffer struct {
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

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
		abs := filepath.Join(workRoot, cleaned)
		if err := tools.WithinRoot(workRoot, abs); err != nil {
			if tools.IsViolation(err) {
				problems = append(problems, fmt.Sprintf("%s: artifact path escapes the workspace", rel))
			} else {
				problems = append(problems, fmt.Sprintf("%s: cannot verify workspace root: %v", rel, err))
			}
			continue
		}
		info, err := os.Stat(abs)
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
