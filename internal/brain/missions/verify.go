package missions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
// full digest and stored per unit in the plan (D-094): enough to see
// what failed without storing megabytes.
const verifyExcerptCap = 4 << 10

// VerifyResult is a plan unit's verification evidence. Only
// RunVerifyWithBackend produces this — never model output — and only
// this evidence may flip a PlanUnit's Passes flag.
type VerifyResult struct {
	ExitCode     int
	OutputSHA256 string
	Excerpt      string
	Passed       bool
	// TimedOut marks a verify_cmd that hit verifyTimeout: a failure
	// with the timeout named in Excerpt, never an infrastructure error.
	TimedOut bool
}

// UnitVerification is one plan unit's outcome from a batch verify pass
// (D-094): the driver collects these after a worker turn or a review
// approval and Step folds them into the plan through ApplyTransition.
type UnitVerification struct {
	Unit    int
	Passed  bool
	Check   string // artifacts, citations, verify_cmd, timeout
	Excerpt string
}

// RunVerifyWithBackend executes a plan unit's verify_cmd via backend
// (the mission's sandbox container) in the work root. Output is
// streamed into a sha256 hash and a bounded tail buffer rather than
// collected in full — a verify_cmd with runaway output must not
// balloon memory. Evidence recorded: exit code, sha256 digest of the
// full output, and a trailing excerpt — "done is auditable from
// events alone."
func RunVerifyWithBackend(ctx context.Context, backend verifyBackend, workRoot, verifyCmd string) (VerifyResult, error) {
	return runVerifyTimed(ctx, backend, workRoot, verifyCmd, verifyTimeout)
}

// runVerifyTimed is RunVerifyWithBackend with the timeout as a
// parameter so tests can exercise the hung-command path.
func runVerifyTimed(ctx context.Context, backend verifyBackend, workRoot, verifyCmd string, timeout time.Duration) (VerifyResult, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	hash := sha256.New()
	tail := &tailBuffer{max: verifyExcerptCap}
	exitCode, err := backend(cctx, workRoot, verifyCmd, timeout, io.MultiWriter(hash, tail))
	if err != nil {
		// Our own deadline firing (not the caller's cancel), or sandboxd's
		// server-side timeout for the same duration, is a hung verify_cmd:
		// real evidence the unit did not pass, not infra.
		sandboxTimeout := strings.Contains(err.Error(), "timed out")
		if ctx.Err() == nil && (cctx.Err() != nil || sandboxTimeout) {
			return VerifyResult{
				ExitCode:     exitCode,
				OutputSHA256: hex.EncodeToString(hash.Sum(nil)),
				Excerpt:      tail.String() + fmt.Sprintf("\nverify_cmd timed out after %s", timeout),
				TimedOut:     true,
			}, nil
		}
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

// citedURLPattern matches both markdown links ([text](http...)) and
// bare http(s) URLs, plus kb://<document_id> refs (kbsearch.go's
// formatKBHits "Source:" line) in either form. Markdown link targets
// are captured first so a bare-ref scan doesn't also pick up the same
// URL/ref again from inside the parens (D-059).
var citedURLPattern = regexp.MustCompile(`\[[^\]]*\]\(((?:https?|kb)://[^\s)]+)\)|((?:https?|kb)://[^\s)\]]+)`)

// ExtractCitedURLs pulls every http(s) URL and kb:// reference cited in
// text — markdown link targets and bare refs alike — in first-seen
// order, deduplicated.
func ExtractCitedURLs(text string) []string {
	matches := citedURLPattern.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool, len(matches))
	var urls []string
	for _, m := range matches {
		u := m[1]
		if u == "" {
			u = m[2]
		}
		if u == "" {
			continue
		}
		// A bare kb:// ref is a bounded UUID, so anything past it —
		// "kb://<id>;" at a clause boundary — is prose punctuation the
		// bare-ref branch swallowed, never part of the ref.
		if rest, ok := strings.CutPrefix(u, "kb://"); ok {
			u = "kb://" + strings.TrimRightFunc(rest, func(r rune) bool {
				return r != '-' && (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F')
			})
		}
		if seen[u] {
			continue
		}
		seen[u] = true
		urls = append(urls, u)
	}
	return urls
}

// NormalizeURL canonicalizes a URL for citation comparison: lowercase
// scheme+host, fragment stripped, one trailing slash collapsed. Query
// strings are left exactly as given — a query difference means a
// different resource, not the same one differently written. An
// unparseable URL normalizes to its trimmed original so it still
// compares (and fails) predictably rather than vanishing silently.
func NormalizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String()
}

// CheckCitations verifies every http(s) URL and kb:// reference cited
// in a unit's declared artifacts was actually seen by the worker this
// turn — via fetch_url's url arg, a search_web result URL, or a
// search_kb result's kb:// ref — never merely claimed. Scoped to
// "general" missions only (D-059): coding missions cite source, not
// the web/knowledge base. seenURLs empty and the artifact cites
// nothing passes trivially; seenURLs empty with citations present
// fails everything, since no tool call at all cannot have produced any
// of them.
func CheckCitations(workRoot string, artifacts, seenURLs []string) []string {
	allowed := make(map[string]bool, len(seenURLs))
	for _, u := range seenURLs {
		allowed[NormalizeURL(u)] = true
	}

	var problems []string
	for _, a := range artifacts {
		rel := strings.TrimSpace(a)
		if rel == "" {
			continue
		}
		cleaned := filepath.Clean(rel)
		if filepath.IsAbs(rel) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			continue // reported by CheckArtifacts; not this check's job
		}
		abs := filepath.Join(workRoot, cleaned)
		if err := tools.WithinRoot(workRoot, abs); err != nil {
			continue
		}
		content, err := os.ReadFile(abs) // #nosec G304 -- cleaned and WithinRoot-vetted above
		if err != nil {
			continue // missing/unreadable artifact is CheckArtifacts' problem
		}

		var unknown []string
		seenUnknown := map[string]bool{}
		for _, cited := range ExtractCitedURLs(string(content)) {
			norm := NormalizeURL(cited)
			if allowed[norm] || seenUnknown[norm] {
				continue
			}
			seenUnknown[norm] = true
			unknown = append(unknown, cited)
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			problems = append(problems, fmt.Sprintf("%s: %s — %s", rel, unknownCitationSummary(unknown), unknownCitationFix(unknown)))
		}
	}
	return problems
}

// unknownCitationSummary labels an unknown-citation list "URL(s)",
// "kb reference(s)", or a mix — the failure message only names kb
// refs when at least one is actually present, so a citations-web-only
// unit's message never mentions a tool it had no reason to use.
func unknownCitationSummary(unknown []string) string {
	hasKB, hasURL := false, false
	for _, u := range unknown {
		if strings.HasPrefix(u, "kb://") {
			hasKB = true
		} else {
			hasURL = true
		}
	}
	switch {
	case hasKB && hasURL:
		return "cited URL(s)/kb reference(s) never seen this turn: " + strings.Join(unknown, ", ")
	case hasKB:
		return "cited kb reference(s) never seen via search_kb this turn: " + strings.Join(unknown, ", ")
	default:
		return "cited URL(s) never seen via fetch_url/search_web this turn: " + strings.Join(unknown, ", ")
	}
}

// unknownCitationFix names the fix matching unknownCitationSummary's
// scope.
func unknownCitationFix(unknown []string) string {
	hasKB, hasURL := false, false
	for _, u := range unknown {
		if strings.HasPrefix(u, "kb://") {
			hasKB = true
		} else {
			hasURL = true
		}
	}
	switch {
	case hasKB && hasURL:
		return "fetch a source with fetch_url, or search for it with search_kb, before citing it"
	case hasKB:
		return "search for it with search_kb before citing it"
	default:
		return "fetch a source with fetch_url before citing it"
	}
}
