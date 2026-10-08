package lib

// Real-shell round trips for scripts/lib/ecosystem-matrix.sh (issue
// #1018): the functions run under bash with stub gh, docker and curl
// on PATH that log their argv, so quoting bugs show as wrong argv.

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sha = "50b5d5ce49f7863ed70c82dad2652bc8cb147859"

// stubLog logs each call as NUL-terminated program name and args,
// ended by a record separator.
const stubLog = `printf '%s\0' "$(basename "$0")" "$@" >> "$STUB_LOG"
printf '\036' >> "$STUB_LOG"
`

// setupStubs puts gh, docker and curl stubs first on PATH. gh prints
// STUB_PRS for the open-PR query and fails the branch lookup unless
// STUB_BRANCH_EXISTS is set; curl writes a fake zip to its -o path.
func setupStubs(t *testing.T) (logFile string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	logFile = filepath.Join(t.TempDir(), "calls.log")
	stubs := map[string]string{
		"gh": stubLog + `case "$2" in
  *"/pulls?state=open"*) [ -n "$STUB_PRS" ] && printf '%s\n' $STUB_PRS ;;
  *"/git/ref/heads/"*) [ -n "$STUB_BRANCH_EXISTS" ] || exit 1 ;;
esac
exit 0
`,
		"docker": stubLog,
		"curl": stubLog + `while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then printf 'zip' > "$2"; fi
  shift
done
`,
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // test stub must be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB_LOG", logFile)
	t.Setenv("STUB_PRS", "")
	t.Setenv("STUB_BRANCH_EXISTS", "")
	return logFile
}

// runLib sources the library in bash and runs script with args as $@.
func runLib(t *testing.T, script string, args ...string) (string, error) {
	t.Helper()
	lib, err := filepath.Abs("ecosystem-matrix.sh")
	if err != nil {
		t.Fatal(err)
	}
	argv := append([]string{"-c", `source "$1"; shift; ` + script, "bash", lib}, args...)
	out, err := exec.Command("bash", argv...).CombinedOutput() //nolint:gosec // test runs the library under test
	return string(out), err
}

// calls parses the stub log into one argv per call, program name first.
func calls(t *testing.T, logFile string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(logFile) //nolint:gosec // test-owned path
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, rec := range strings.Split(strings.TrimSuffix(string(raw), "\x1e"), "\x1e") {
		out = append(out, strings.Split(strings.TrimSuffix(rec, "\x00"), "\x00"))
	}
	return out
}

func TestMatrixResetFork(t *testing.T) {
	logFile := setupStubs(t)
	if out, err := runLib(t, `matrix_reset_fork "$@"`, "timothy-agent/cheerio", "main", sha); err != nil {
		t.Fatalf("reset: %v\n%s", err, out)
	}
	want := [][]string{{"gh", "api", "-X", "PATCH", "repos/timothy-agent/cheerio/git/refs/heads/main", "-f", "sha=" + sha, "-F", "force=true", "--silent"}}
	if got := calls(t, logFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestMatrixResetForkRejectsBadInput(t *testing.T) {
	cases := [][]string{
		{"timothy-agent/cheerio", "main", "HEAD"},
		{"timothy-agent/cheerio", "main; rm -rf /", sha},
		{"timothy-agent/cheerio", "../main", sha},
		{"timothy-agent/cheerio/x", "main", sha},
		{"timothy-agent/$(id)", "main", sha},
	}
	for _, args := range cases {
		logFile := setupStubs(t)
		if _, err := runLib(t, `matrix_reset_fork "$@"`, args...); err == nil {
			t.Errorf("reset %q succeeded", args)
		}
		if got := calls(t, logFile); got != nil {
			t.Errorf("reset %q called %q", args, got)
		}
	}
}

func TestMatrixCleanupBranch(t *testing.T) {
	logFile := setupStubs(t)
	t.Setenv("STUB_PRS", "12 34")
	t.Setenv("STUB_BRANCH_EXISTS", "1")
	branch := "timothy/audit-deps-1a2b"
	if out, err := runLib(t, `matrix_cleanup_branch "$@"`, "timothy-agent/echo", branch, "master"); err != nil {
		t.Fatalf("cleanup: %v\n%s", err, out)
	}
	want := [][]string{
		{"gh", "api", "repos/timothy-agent/echo/pulls?state=open&head=timothy-agent:" + branch, "--jq", ".[].number"},
		{"gh", "api", "-X", "PATCH", "repos/timothy-agent/echo/pulls/12", "-f", "state=closed", "--silent"},
		{"gh", "api", "-X", "PATCH", "repos/timothy-agent/echo/pulls/34", "-f", "state=closed", "--silent"},
		{"gh", "api", "repos/timothy-agent/echo/git/ref/heads/" + branch, "--silent"},
		{"gh", "api", "-X", "DELETE", "repos/timothy-agent/echo/git/refs/heads/" + branch, "--silent"},
	}
	if got := calls(t, logFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q\nwant %q", got, want)
	}
}

func TestMatrixCleanupBranchAbsent(t *testing.T) {
	logFile := setupStubs(t)
	if out, err := runLib(t, `matrix_cleanup_branch "$@"`, "timothy-agent/echo", "timothy/x", "master"); err != nil {
		t.Fatalf("cleanup: %v\n%s", err, out)
	}
	for _, c := range calls(t, logFile) {
		if strings.Contains(strings.Join(c, " "), "DELETE") || strings.Contains(strings.Join(c, " "), "PATCH") {
			t.Errorf("unexpected call %q with no PR and no branch", c)
		}
	}
}

func TestMatrixCleanupRefusesDefaultBranch(t *testing.T) {
	logFile := setupStubs(t)
	t.Setenv("STUB_BRANCH_EXISTS", "1")
	if _, err := runLib(t, `matrix_cleanup_branch "$@"`, "timothy-agent/echo", "master", "master"); err == nil {
		t.Fatal("cleanup of the default branch succeeded")
	}
	if got := calls(t, logFile); got != nil {
		t.Fatalf("calls = %q, want none", got)
	}
}

func TestMatrixRefreshOSVDB(t *testing.T) {
	logFile := setupStubs(t)
	if out, err := runLib(t, `matrix_refresh_osv_db "$@"`, "timothy_sandbox-caches", "timothy-sandbox:latest", "2026-10-08", "npm", "PyPI"); err != nil {
		t.Fatalf("refresh: %v\n%s", err, out)
	}
	fetch, err := runLib(t, `printf '%s' "$MATRIX_OSV_FETCH"`)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"docker", "run", "--rm", "-v", "timothy_sandbox-caches:/cache", "timothy-sandbox:latest", "sh", "-c", fetch, "sh", "/cache", "2026-10-08", "npm", "PyPI"}}
	if got := calls(t, logFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q\nwant %q", got, want)
	}
	if _, err := runLib(t, `matrix_refresh_osv_db "$@"`, "v", "img", "2026-10-08", "npm;id"); err == nil {
		t.Error("refresh accepted an unsafe ecosystem name")
	}
	if _, err := runLib(t, `matrix_refresh_osv_db "$@"`, "v", "img", "today", "npm"); err == nil {
		t.Error("refresh accepted a malformed day")
	}
}

// TestMatrixOSVFetchScript runs the fetch script under /bin/sh: one
// download per ecosystem per day, osv-scanner's offline layout.
func TestMatrixOSVFetchScript(t *testing.T) {
	logFile := setupStubs(t)
	root := t.TempDir()
	run := func(day string) {
		if out, err := runLib(t, `sh -c "$MATRIX_OSV_FETCH" sh "$@"`, root, day, "npm", "Go"); err != nil {
			t.Fatalf("fetch %s: %v\n%s", day, err, out)
		}
	}
	run("2026-10-08")
	run("2026-10-08")
	for _, eco := range []string{"npm", "Go"} {
		zip, err := os.ReadFile(filepath.Join(root, "osv-scanner", eco, "all.zip")) //nolint:gosec // test-owned path
		if err != nil || string(zip) != "zip" {
			t.Fatalf("%s all.zip = %q, %v", eco, zip, err)
		}
	}
	got := calls(t, logFile)
	if len(got) != 2 {
		t.Fatalf("curl calls on one day = %d, want 2: %q", len(got), got)
	}
	if url := got[0][len(got[0])-1]; url != "https://osv-vulnerabilities.storage.googleapis.com/npm/all.zip" {
		t.Errorf("url = %q", url)
	}
	run("2026-10-09")
	if got := calls(t, logFile); len(got) != 4 {
		t.Fatalf("curl calls after a new day = %d, want 4", len(got))
	}
}
