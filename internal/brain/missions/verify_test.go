package missions

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckArtifacts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "summary.md"), []byte("429 means Too Many Requests"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "empty.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o750); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		artifacts []string
		problems  int
	}{
		{"present and non-empty passes", []string{"summary.md"}, 0},
		{"missing file fails", []string{"nope.md"}, 1},
		{"empty file fails", []string{"empty.md"}, 1},
		{"directory fails", []string{"adir"}, 1},
		{"absolute path fails", []string{"/etc/passwd"}, 1},
		{"escape via .. fails", []string{"../outside.md"}, 1},
		{"blank entries skipped", []string{"", "  ", "summary.md"}, 0},
		{"one good one bad reports only the bad", []string{"summary.md", "nope.md"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := CheckArtifacts(root, tc.artifacts)
			if len(problems) != tc.problems {
				t.Fatalf("CheckArtifacts(%v) = %v, want %d problem(s)", tc.artifacts, problems, tc.problems)
			}
		})
	}
}

// TestCheckArtifactsSymlinkEscape confirms a symlink that resolves
// outside workRoot is caught by the WithinRoot check, not blindly
// os.Stat'd (which would follow it and report the artifact as fine).
func TestCheckArtifactsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(target, []byte("outside content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "escape.md")); err != nil {
		t.Fatal(err)
	}

	problems := CheckArtifacts(root, []string{"escape.md"})
	if len(problems) != 1 {
		t.Fatalf("CheckArtifacts(symlink escape) = %v, want exactly 1 problem", problems)
	}
	if !strings.Contains(problems[0], "escapes the workspace") {
		t.Fatalf("problem = %q, want it to mention escaping the workspace", problems[0])
	}
}

// fakeVerifyBackend writes fixed output to whatever writer it's given
// and returns exitCode — a stand-in for sandbox.Manager.Exec.
func fakeVerifyBackend(output string, exitCode int, err error) verifyBackend {
	return func(ctx context.Context, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		if err != nil {
			return 0, err
		}
		if _, werr := out.Write([]byte(output)); werr != nil {
			return 0, werr
		}
		return exitCode, nil
	}
}

// TestRunVerifyWithBackendDigestCorrectness confirms the streamed
// evidence (digest, excerpt, passed) matches what's actually written
// by the backend, byte for byte — the sha256 hash and tail buffer must
// see exactly what a full CombinedOutput() collection would have.
func TestRunVerifyWithBackendDigestCorrectness(t *testing.T) {
	const output = "hello"
	// sha256("hello")
	const wantDigest = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

	got, err := RunVerifyWithBackend(context.Background(), fakeVerifyBackend(output, 0, nil), "/workspace", "printf hello")
	if err != nil {
		t.Fatalf("RunVerifyWithBackend: %v", err)
	}
	if !got.Passed || got.ExitCode != 0 {
		t.Fatalf("RunVerifyWithBackend(exit 0) = %+v, want passed with exit 0", got)
	}
	if got.OutputSHA256 != wantDigest {
		t.Fatalf("digest = %q, want %q", got.OutputSHA256, wantDigest)
	}
	if got.Excerpt != output {
		t.Fatalf("excerpt = %q, want %q", got.Excerpt, output)
	}
}

func TestRunVerifyWithBackendNonZeroExit(t *testing.T) {
	got, err := RunVerifyWithBackend(context.Background(), fakeVerifyBackend("boom", 3, nil), "/workspace", "exit 3")
	if err != nil {
		t.Fatalf("RunVerifyWithBackend: %v", err)
	}
	if got.Passed || got.ExitCode != 3 {
		t.Fatalf("RunVerifyWithBackend = %+v, want failed with exit 3", got)
	}
}

func TestRunVerifyWithBackendInfraErrorPropagates(t *testing.T) {
	wantErr := context.DeadlineExceeded
	_, err := RunVerifyWithBackend(context.Background(), fakeVerifyBackend("", 0, wantErr), "/workspace", "true")
	if err != wantErr {
		t.Fatalf("RunVerifyWithBackend error = %v, want %v propagated from the backend", err, wantErr)
	}
}

// TestTailBufferBoundsMemoryKeepsEnd confirms the streamed excerpt is
// bounded to the cap and keeps the TAIL of the output, not the head —
// a verify_cmd with runaway output must not balloon memory, and the
// kept slice must still show what actually failed at the end.
func TestTailBufferBoundsMemoryKeepsEnd(t *testing.T) {
	tail := &tailBuffer{max: 5}
	full := "0123456789" // 10 bytes written in one shot, cap is 5
	if _, err := tail.Write([]byte(full)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(tail.buf) != 5 {
		t.Fatalf("tailBuffer retained %d bytes, want exactly 5", len(tail.buf))
	}
	if got, want := tail.String(), "56789"; got != want {
		t.Fatalf("tailBuffer content = %q, want %q (the last 5 bytes)", got, want)
	}
}

func TestExtractCitedURLs(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"markdown link", "see [the docs](https://example.com/docs) for more", []string{"https://example.com/docs"}},
		{"bare url", "fetched from https://example.com/api/v1 directly", []string{"https://example.com/api/v1"}},
		{"mixed markdown and bare", "[a](https://a.example/x) and also https://b.example/y", []string{"https://a.example/x", "https://b.example/y"}},
		{"no urls", "just plain text, no citations here", nil},
		{"duplicate collapses to one", "https://example.com/x and again https://example.com/x", []string{"https://example.com/x"}},
		{"http scheme included", "http://example.com/plain", []string{"http://example.com/plain"}},
		{"non-http scheme ignored", "see ftp://example.com/file", nil},
		{"bare kb ref", "Source: kb://doc-123", []string{"kb://doc-123"}},
		{"markdown kb link", "see [the doc](kb://doc-123) for more", []string{"kb://doc-123"}},
		{"mixed kb ref and url", "kb://doc-123 and also https://example.com/x", []string{"kb://doc-123", "https://example.com/x"}},
		{"bare kb ref sheds trailing punctuation", "cites kb://a1b2c3d4-0000-0000-0000-000000000001; and kb://a1b2c3d4-0000-0000-0000-000000000002.", []string{"kb://a1b2c3d4-0000-0000-0000-000000000001", "kb://a1b2c3d4-0000-0000-0000-000000000002"}},
		{"trailing quote stripped", "see https://app.example.org'", []string{"https://app.example.org"}},
		{"trailing port and period stripped", "see https://example.com:443.", []string{"https://example.com:443"}},
		{"trailing comma and quote stripped", "see https://api.example.org/resource',,", []string{"https://api.example.org/resource"}},
		{"trailing backtick stripped", "see `https://example.com/docs`", []string{"https://example.com/docs"}},
		{"no-dot host ignored", "a truncated https://app fragment", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractCitedURLs(tc.text)
			if !equalStrings(got, tc.want) {
				t.Fatalf("ExtractCitedURLs(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestIsPlaceholderHost(t *testing.T) {
	cases := []struct {
		name string
		host string
		want bool
	}{
		{"example.com exact", "example.com", true},
		{"example.net exact", "example.net", true},
		{"example.org exact", "example.org", true},
		{"example.com subdomain", "app.example.com", true},
		{"example.org subdomain", "api.example.org", true},
		{"reserved tld example", "service.example", true},
		{"reserved tld test", "service.test", true},
		{"reserved tld invalid", "box.invalid", true},
		{"reserved tld localhost", "web.localhost", true},
		{"bare localhost", "localhost", true},
		{"documentation ip 192.0.2", "192.0.2.10", true},
		{"documentation ip 198.51.100", "198.51.100.20", true},
		{"documentation ip 203.0.113", "203.0.113.30", true},
		{"case insensitive", "APP.EXAMPLE.COM", true},
		{"real domain", "docs.acme.dev", false},
		{"lookalike domain not a subdomain", "notexample.com", false},
		{"lookalike suffix not reserved tld", "myservice.testing", false},
		{"real ip outside documentation ranges", "8.8.8.8", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPlaceholderHost(tc.host); got != tc.want {
				t.Fatalf("isPlaceholderHost(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"trailing slash stripped", "https://example.com/docs/", "https://example.com/docs"},
		{"fragment stripped", "https://example.com/docs#section-2", "https://example.com/docs"},
		{"scheme and host lowercased", "HTTPS://Example.COM/Docs", "https://example.com/Docs"},
		{"query preserved exactly", "https://example.com/search?q=Foo", "https://example.com/search?q=Foo"},
		{"no trailing slash unchanged", "https://example.com/docs", "https://example.com/docs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeURL(tc.in); got != tc.want {
				t.Fatalf("NormalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCheckCitations(t *testing.T) {
	writeArtifact := func(t *testing.T, root, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name      string
		content   string
		seenURLs  []string
		wantProbs int
	}{
		{
			name:      "cited url was fetched",
			content:   "source: [docs](https://docs.acme.dev/docs)",
			seenURLs:  []string{"https://docs.acme.dev/docs"},
			wantProbs: 0,
		},
		{
			name:      "invented citation fails",
			content:   "source: [docs](https://docs.acme.dev/invented)",
			seenURLs:  []string{"https://docs.acme.dev/other"},
			wantProbs: 1,
		},
		{
			name:      "bare url matched against search result",
			content:   "see https://docs.acme.dev/api for details",
			seenURLs:  []string{"https://docs.acme.dev/api"},
			wantProbs: 0,
		},
		{
			name:      "trailing slash and fragment normalize equal",
			content:   "[ref](https://docs.acme.dev/docs/#intro)",
			seenURLs:  []string{"https://docs.acme.dev/docs"},
			wantProbs: 0,
		},
		{
			name:      "no links in artifact passes trivially",
			content:   "no citations here, just prose",
			seenURLs:  nil,
			wantProbs: 0,
		},
		{
			name:      "no web calls at all but links present fails",
			content:   "[ref](https://docs.acme.dev/docs)",
			seenURLs:  nil,
			wantProbs: 1,
		},
		{
			name:      "cited kb ref was searched",
			content:   "Source: kb://doc-123",
			seenURLs:  []string{"kb://doc-123"},
			wantProbs: 0,
		},
		{
			name:      "invented kb ref fails",
			content:   "Source: kb://doc-invented",
			seenURLs:  []string{"kb://doc-other"},
			wantProbs: 1,
		},
		{
			name:      "kb ref and url both seen passes",
			content:   "Source: kb://doc-123\nsee also [docs](https://docs.acme.dev/docs)",
			seenURLs:  []string{"kb://doc-123", "https://docs.acme.dev/docs"},
			wantProbs: 0,
		},
		{
			name:      "placeholder domain skipped without being fetched",
			content:   "example: [redirect target](https://example.com/target)",
			seenURLs:  nil,
			wantProbs: 0,
		},
		{
			name:      "placeholder subdomain skipped",
			content:   "example: https://app.example.org/callback",
			seenURLs:  nil,
			wantProbs: 0,
		},
		{
			name:      "placeholder reserved tld skipped",
			content:   "example: https://service.test/health and https://box.invalid/ping and https://web.localhost/",
			seenURLs:  nil,
			wantProbs: 0,
		},
		{
			name:      "placeholder documentation ip skipped",
			content:   "example: https://192.0.2.10/ and https://198.51.100.20/ and https://203.0.113.30/",
			seenURLs:  nil,
			wantProbs: 0,
		},
		{
			name:      "mixed placeholder and real url only flags the real one",
			content:   "see https://example.com/sample and also https://docs.acme.dev/real",
			seenURLs:  nil,
			wantProbs: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeArtifact(t, root, "report.md", tc.content)
			problems := CheckCitations(root, []string{"report.md"}, tc.seenURLs)
			if len(problems) != tc.wantProbs {
				t.Fatalf("CheckCitations = %v, want %d problem(s)", problems, tc.wantProbs)
			}
		})
	}
}

// equalStrings compares two string slices where nil and empty are
// treated the same (ExtractCitedURLs returns nil, not an empty slice,
// when nothing matches).
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
