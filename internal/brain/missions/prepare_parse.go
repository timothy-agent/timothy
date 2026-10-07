package missions

// Parsers for prepare evidence (D-130): test runner summaries and the
// osv-scanner JSON report.

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// TestSummary is what the baseline test run reported, parsed from the
// runner's summary line. Parsed is false when no known summary matched;
// the counts are then zero and only the exit code speaks.
type TestSummary struct {
	Passed   int  `json:"passed"`
	Failed   int  `json:"failed"`
	Warnings int  `json:"warnings"`
	Parsed   bool `json:"parsed"`
}

// ansiEscape matches OSC sequences (hyperlinks, titles), CSI sequences
// (colors, cursor moves), charset selections and other two-byte escapes.
var ansiEscape = regexp.MustCompile(`\x1b(?:\][^\x07\x1b]*(?:\x07|\x1b\\)|\[[0-?]*[ -/]*[@-~]|[()][0-9A-Za-z]|[=>@-Z\\-_])`)

// stripANSI removes terminal escape sequences from command output. Some
// runners (Collision) color output whatever NO_COLOR says.
func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// summaryPair matches "36 passed", "2 failing", "36 warnings", "40
// examples", "0 failures" (pytest, Pest, jest, vitest, mocha, cargo,
// rspec). Collision's skipped, deprecated, risky, incomplete, todo and
// notice counts mark a summary line without being counted.
var summaryPair = regexp.MustCompile(`(?i)\b(\d+)\s+(passed|passing|failed|failing|failures?|errors?|warnings?|examples?|tests?|skipped|deprecated|risky|incomplete|todos?|notices?)\b`)

// summaryKV matches PHPUnit's classic "Tests: 36, Assertions: 72,
// Warnings: 36, Failures: 2." and jest's "Tests: 36 passed" prefix.
var summaryKV = regexp.MustCompile(`(?i)\b(Tests|Warnings|Failures|Errors):\s+(\d+)\b`)

// summaryOK matches PHPUnit's "OK (36 tests, 72 assertions)".
var summaryOK = regexp.MustCompile(`^OK \((\d+) tests?`)

// goPass and goFail match go test package lines and -v test lines.
var (
	goPass = regexp.MustCompile(`(?m)^(ok[ \t]+\S|--- PASS:)`)
	goFail = regexp.MustCompile(`(?m)^(FAIL\t|--- FAIL:)`)
)

// parseTestSummary reads the last summary line in a test run's output.
// Lines are scanned from the end so a runner's final totals win over
// per-file lines. go test output is counted by package or test line.
// Escape sequences are stripped first.
func parseTestSummary(out string) TestSummary {
	out = stripANSI(out)
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if m := summaryOK.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			return TestSummary{Passed: n, Parsed: true}
		}
		if s, ok := summaryFromLine(line); ok {
			return s
		}
	}
	if goPass.MatchString(out) || goFail.MatchString(out) {
		return TestSummary{Passed: len(goPass.FindAllString(out, -1)), Failed: len(goFail.FindAllString(out, -1)), Parsed: true}
	}
	return TestSummary{}
}

// summaryFromLine parses one candidate line: pairs first, then the
// PHPUnit key-value form. rspec's "N examples, M failures" reports
// passed = examples - failures.
func summaryFromLine(line string) (TestSummary, bool) {
	var s TestSummary
	var examples, total int
	matched := false
	for _, m := range summaryPair.FindAllStringSubmatch(line, -1) {
		n, _ := strconv.Atoi(m[1])
		switch strings.ToLower(m[2]) {
		case "passed", "passing":
			s.Passed += n
		case "failed", "failing", "failure", "failures", "error", "errors":
			s.Failed += n
		case "warning", "warnings":
			s.Warnings += n
		case "example", "examples":
			examples = n
		case "test", "tests":
			total = n
			continue
		}
		matched = true
	}
	if !matched {
		kv := summaryKV.FindAllStringSubmatch(line, -1)
		if len(kv) == 0 {
			return TestSummary{}, false
		}
		for _, m := range kv {
			n, _ := strconv.Atoi(m[2])
			switch strings.ToLower(m[1]) {
			case "tests":
				total = n
			case "warnings":
				s.Warnings = n
			case "failures", "errors":
				s.Failed += n
			}
		}
		if total == 0 {
			return TestSummary{}, false
		}
		s.Passed = total - s.Failed
	}
	if examples > 0 && s.Passed == 0 {
		s.Passed = examples - s.Failed
	}
	s.Parsed = true
	return s, true
}

// AuditFact is osv-scanner's result for one lockfile: packages scanned
// and distinct advisories (one per vulnerability group).
type AuditFact struct {
	Path            string `json:"path"`
	Packages        int    `json:"packages"`
	Vulnerabilities int    `json:"vulnerabilities"`
}

// osvReport is the subset of osv-scanner's JSON output the audit reads.
type osvReport struct {
	Results []struct {
		Source struct {
			Path string `json:"path"`
		} `json:"source"`
		Packages []struct {
			Groups          []json.RawMessage `json:"groups"`
			Vulnerabilities []json.RawMessage `json:"vulnerabilities"`
		} `json:"packages"`
	} `json:"results"`
}

// parseOSVReport counts packages and advisories per lockfile. Paths
// under worktree are made relative to it.
func parseOSVReport(raw []byte, worktree string) ([]AuditFact, error) {
	var rep osvReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, err
	}
	var out []AuditFact
	for _, r := range rep.Results {
		f := AuditFact{Path: r.Source.Path, Packages: len(r.Packages)}
		if worktree != "" {
			if rel, err := filepath.Rel(worktree, r.Source.Path); err == nil && !strings.HasPrefix(rel, "..") {
				f.Path = filepath.ToSlash(rel)
			}
		}
		for _, p := range r.Packages {
			if len(p.Groups) > 0 {
				f.Vulnerabilities += len(p.Groups)
			} else {
				f.Vulnerabilities += len(p.Vulnerabilities)
			}
		}
		out = append(out, f)
	}
	return out, nil
}
