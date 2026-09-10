package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestGuardSubject(t *testing.T) {
	t.Parallel()
	const root = "/workspace"
	tests := []struct {
		name    string
		command string
		blocked string // empty = allowed
	}{
		{name: "env file", command: "cat .env", blocked: "env files"},
		{name: "env variant", command: "cat .env.production", blocked: "env files"},
		{name: "ssh dir", command: "ls ~/.ssh/", blocked: "ssh keys"},
		{name: "ssh key by name", command: "cat id_rsa", blocked: "ssh keys"},
		{name: "pem file", command: "openssl x509 -in server.pem", blocked: "key material"},
		{name: "aws creds", command: "cat ~/.aws/credentials", blocked: "ssh keys|credential stores|home dotfiles"},
		{name: "etc passwd", command: "cat /etc/passwd", blocked: "system dirs"},
		{name: "proc", command: "cat /proc/self/environ", blocked: "system dirs"},
		{name: "home dotfile", command: "cat ~/.zshrc", blocked: "home dotfiles"},
		{name: "secrets dir", command: "ls secrets/", blocked: "credential stores"},
		{name: "outside workspace", command: "cat /Users/someone/notes.txt", blocked: "outside the workspace"},
		{name: "flag-embedded path", command: "tar -cf out.tar --directory=/opt/data .", blocked: "outside the workspace"},
		{name: "relative parent escape", command: "cat ../../../../etc/passwd", blocked: ".."},
		{name: "dotdot mid-path", command: "cat sub/../../private", blocked: ".."},
		{name: "bare dotdot", command: "ls ..", blocked: ".."},

		{name: "plain listing", command: "ls -la"},
		{name: "workspace absolute", command: "cat /workspace/notes.txt"},
		{name: "relative path", command: "grep -rn TODO src/"},
		{name: "dev null", command: "grep x file > /dev/null"},
		{name: "environment word", command: "grep environment docs/config.md"},
		{name: "html closing tag", command: `echo "</head>" >> summary.md`},
		{name: "html tag no quotes", command: "printf '<html>\\n</html>'"},
		{name: "redirect outside workspace", command: "cat file 2>&1 >/etc/passwd", blocked: "system dirs|outside the workspace"},

		{name: "quoted regex leading slash", command: "awk '/^#{1,6}/ {print}' README.md"},
		{name: "quoted regex alternation", command: "grep -E '^(foo|bar)$' /workspace/file"},
		{name: "awk program with spaces and braces", command: "awk '/R1/{ok=1} END{exit !ok}' rules-checklist.md"},
		{name: "grep pattern with leading space", command: "grep -c '^### ' ideas.md"},
		{name: "quoted sed pattern", command: "sed 's/^#//' notes.md"},
		{name: "quoted secret path still denied", command: "cat '/etc/passwd'", blocked: "system dirs"},
		{name: "quoted plain path still denied", command: "cat '/Users/someone/x'", blocked: "outside the workspace"},
		{name: "quoted brace path exempt", command: "cat '/tmp/{a,b}'"},
		{name: "unquoted brace path denied", command: "cat /tmp/{a,b}", blocked: "outside the workspace"},
		{name: "quoted metachars relative redirect", command: `echo "^foo$" > out.md`},

		// A guarded name inside ordinary command text names no such
		// path: the guard matches path-like tokens, not raw substrings.
		{name: "builder.aws in grep pattern", command: "grep -n 'builder.aws' plan.md"},
		{name: "builder.aws in heredoc line", command: "python3 - <<'PY'\nreq=['builder.aws','README']\nPY"},
		{name: "credentials as prose", command: "grep -rn 'rotate the credentials' docs/"},
		{name: "secrets as prose", command: "grep -rn 'secrets management' docs/"},
		{name: "aws sdk import path", command: "go get github.com/aws/aws-sdk-go-v2"},
		{name: "key as a word", command: "grep -rn 'api key rotation' notes.md"},

		// Real paths stay denied however they are wrapped.
		{name: "aws dir relative", command: "ls .aws/", blocked: "credential stores"},
		{name: "npmrc in home", command: "cat ~/.npmrc", blocked: "credential stores|home dotfiles"},
		{name: "credentials file punctuated", command: `cat "~/.aws/credentials";`, blocked: "credential stores|home dotfiles"},
		{name: "env file in subdir", command: "cat deploy/.env", blocked: "env files"},
		{name: "keystore file", command: "keytool -list -keystore app.keystore", blocked: "key material"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := guardSubject(root, "shell", tc.command)
			if tc.blocked == "" {
				if got != "" {
					t.Fatalf("guardSubject(%q) = %q, want allowed", tc.command, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("guardSubject(%q) allowed, want blocked (%s)", tc.command, tc.blocked)
			}
			matched := false
			for _, alt := range strings.Split(tc.blocked, "|") {
				if strings.Contains(got, alt) {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("guardSubject(%q) = %q, want reason matching %q", tc.command, got, tc.blocked)
			}
		})
	}

	// The guard only applies to shell.
	if got := guardSubject(root, "fetch_url", "https://example.com/.env"); got != "" {
		t.Fatalf("non-shell tool guarded: %q", got)
	}
}

// TestGuardedCommandsRunAsIntended is a real /bin/sh round-trip for
// the two commands issue #653 found the guard misreading as paths:
// a fake pass here would mean the guard's tokenizer parsed something
// the shell itself does not, since quoting bugs forgive themselves in
// mocks (see real-shell-tests-for-composed-commands.md).
func TestGuardedCommandsRunAsIntended(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	checklist := "R1: ok\nR2: ok\n"
	ideas := "### one\n### two\nnot a heading\n"
	if err := os.WriteFile(dir+"/rules-checklist.md", []byte(checklist), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/ideas.md", []byte(ideas), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "awk", command: "awk '/R1/{ok=1} END{exit !ok}' rules-checklist.md; echo $?", want: "0\n"},
		{name: "grep", command: "grep -c '^### ' ideas.md", want: "2\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := guardSubject("/workspace", "shell", tc.command); got != "" {
				t.Fatalf("guardSubject(%q) = %q, want allowed", tc.command, got)
			}
			cmd := exec.Command("/bin/sh", "-c", tc.command) //nolint:gosec // test-table command, not external input
			cmd.Dir = dir
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("sh -c %q: %v", tc.command, err)
			}
			if string(out) != tc.want {
				t.Fatalf("sh -c %q output = %q, want %q", tc.command, out, tc.want)
			}
		})
	}
}

func TestCallSubject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tool string
		args string
		want string
	}{
		{tool: "shell", args: `{"command":"ls -la"}`, want: "ls -la"},
		{tool: "fetch_url", args: `{"url":"https://example.com"}`, want: "https://example.com"},
		{tool: "calculate", args: `{"expression":"1+1"}`, want: "calculate"},
		{tool: "shell", args: `not json`, want: "shell"},
	}
	for _, tc := range tests {
		if got := callSubject(tc.tool, json.RawMessage(tc.args)); got != tc.want {
			t.Errorf("callSubject(%s, %s) = %q, want %q", tc.tool, tc.args, got, tc.want)
		}
	}
}

func TestGlobMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern, subject string
		want             bool
	}{
		{pattern: "git status*", subject: "git status --short", want: true},
		{pattern: "git status*", subject: "git stash drop", want: false},
		{pattern: "ls*", subject: "ls -la", want: true},
		{pattern: "*", subject: "anything at all", want: true},
		{pattern: "https://example.com/*", subject: "https://example.com/a/b", want: true},
		{pattern: "https://example.com/*", subject: "https://evil.com/", want: false},
		{pattern: "exact", subject: "exact", want: true},
		{pattern: "exact", subject: "exactly not", want: false},
	}
	for _, tc := range tests {
		if got := globMatch(tc.pattern, tc.subject); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
		}
	}
}

func TestToolMatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		tool, rowTool string
		want          bool
	}{
		{name: "exact match", tool: "gmail_search", rowTool: "gmail_search", want: true},
		{name: "connector suffix match", tool: "google-calendar_list_calendar_events", rowTool: "list_calendar_events", want: true},
		{name: "connector suffix match gmail", tool: "gmail_gmail_search", rowTool: "gmail_search", want: true},
		{name: "sandbox sentinel never suffix-matches", tool: "foo___sandbox__", rowTool: SandboxGrantTool, want: false},
		{name: "sandbox sentinel exact still matches", tool: SandboxGrantTool, rowTool: SandboxGrantTool, want: true},
		{name: "no underscore boundary rejected", tool: "notlist_calendar_events", rowTool: "list_calendar_events", want: false},
		{name: "trailing extra text rejected", tool: "list_calendar_events_extra", rowTool: "list_calendar_events", want: false},
		{name: "unrelated tool", tool: "shell", rowTool: "list_calendar_events", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ToolMatches(tc.tool, tc.rowTool); got != tc.want {
				t.Errorf("ToolMatches(%q, %q) = %v, want %v", tc.tool, tc.rowTool, got, tc.want)
			}
		})
	}
}

// The chain short-circuits before any DB access for policy-guard
// denials, exempt tools, and danger-classified commands — so these
// paths are testable without Postgres (nil pool).
func TestResolveShortCircuits(t *testing.T) {
	t.Parallel()
	p := NewPermissions(nil, "/workspace")
	ctx := context.Background()

	t.Run("policy guard denies", func(t *testing.T) {
		t.Parallel()
		res, err := p.Resolve(ctx, "s1", "shell", json.RawMessage(`{"command":"cat /etc/passwd"}`))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if res.Decision != DecisionDeny || !strings.Contains(res.Rationale, "policy guard") {
			t.Fatalf("res = %+v, want hard deny", res)
		}
	})

	t.Run("exempt tool allows", func(t *testing.T) {
		t.Parallel()
		res, err := p.Resolve(ctx, "s1", "calculate", json.RawMessage(`{"expression":"1+1"}`))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if res.Decision != DecisionAllow {
			t.Fatalf("res = %+v, want allow", res)
		}
	})

	// search_kb, read_kb and search_memory are pure reads over stores
	// scoped in Go; a prompt on them parks a mission on nothing (#648).
	t.Run("store read tools exempt", func(t *testing.T) {
		t.Parallel()
		for _, tool := range []string{"search_kb", "read_kb", "search_memory"} {
			res, err := p.Resolve(ctx, "s1", tool, json.RawMessage(`{"query":"q"}`))
			if err != nil {
				t.Fatalf("Resolve(%s): %v", tool, err)
			}
			if res.Decision != DecisionAllow {
				t.Fatalf("Resolve(%s) = %+v, want allow", tool, res)
			}
		}
	})

	t.Run("mission sentinel tools exempt", func(t *testing.T) {
		t.Parallel()
		for _, tool := range []string{"mission_status", "review_verdict", "submit_plan", "discover_notes", "ask_user"} {
			res, err := p.Resolve(ctx, "s1", tool, json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("Resolve(%s): %v", tool, err)
			}
			if res.Decision != DecisionAllow {
				t.Fatalf("Resolve(%s) = %+v, want allow", tool, res)
			}
		}
	})

	// list_missions/get_mission are pure reads (list/status snapshot)
	// and exempt, same reasoning as search_web; push_mission_branch must
	// never be exempt (see TestResolvePushMissionBranchAsksWithoutGrant,
	// the integration counterpart proving its full no-grant path) —
	// this only pins the exempt-map membership the short-circuit above
	// depends on.
	t.Run("list_missions and get_mission tools exempt", func(t *testing.T) {
		t.Parallel()
		for _, tool := range []string{"list_missions", "get_mission"} {
			res, err := p.Resolve(ctx, "s1", tool, json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("Resolve(%s): %v", tool, err)
			}
			if res.Decision != DecisionAllow {
				t.Fatalf("Resolve(%s) = %+v, want allow", tool, res)
			}
		}
	})

	t.Run("destructive forces ask", func(t *testing.T) {
		t.Parallel()
		res, err := p.Resolve(ctx, "s1", "shell", json.RawMessage(`{"command":"rm -rf build/"}`))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if res.Decision != DecisionAsk || res.Danger != DangerDestructive {
			t.Fatalf("res = %+v, want ask+destructive", res)
		}
		if !strings.Contains(res.Rationale, "rm") {
			t.Fatalf("rationale %q does not name the rule", res.Rationale)
		}
	})

	// D-050: without a registered sandbox (chat sessions never register
	// one — nil db here means sandboxFor always returns "") an opaque
	// command still forces the ask exactly as before. Chat's behavior
	// must not change.
	t.Run("opaque command with no sandbox still asks (chat unchanged)", func(t *testing.T) {
		t.Parallel()
		res, err := p.Resolve(ctx, "s1", "shell", json.RawMessage(`{"command":"python3 -c 'print(1)'"}`))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if res.Decision != DecisionAsk || res.Danger != DangerDestructive {
			t.Fatalf("res = %+v, want ask+destructive", res)
		}
		if !strings.Contains(res.Rationale, "opaque command") {
			t.Fatalf("rationale %q does not name the opaque rule", res.Rationale)
		}
	})
}

// TestSandboxAllowsGating covers the pieces of the sandbox downgrade
// that need no database: rules that are not file-scoped must never
// downgrade, and without a registered sandbox (nil db here) nothing
// downgrades at all.
func TestSandboxAllowsGating(t *testing.T) {
	t.Parallel()
	p := NewPermissions(nil, "/workspace")
	ctx := context.Background()

	t.Run("no sandbox registered keeps the ask", func(t *testing.T) {
		t.Parallel()
		if p.sandboxAllows(ctx, "s1", "rm -rf ./build", []string{"rm"}) {
			t.Fatal("sandboxAllows = true with no sandbox registered")
		}
	})

	t.Run("non-file-scoped rule keeps the ask before any lookup", func(t *testing.T) {
		t.Parallel()
		if p.sandboxAllows(ctx, "s1", "git push origin main", []string{"git-push"}) {
			t.Fatal("sandboxAllows = true for git-push — not a file-scoped rule")
		}
	})

	t.Run("opaque classification keeps the ask", func(t *testing.T) {
		t.Parallel()
		_, rules := ClassifyCommand("eval $CMD")
		if p.sandboxAllows(ctx, "s1", "eval $CMD", rules) {
			t.Fatal("sandboxAllows = true for an opaque command")
		}
	})
}

func TestGuardEmbeddedPath(t *testing.T) {
	t.Parallel()
	const root = "/workspace"
	cases := []struct {
		cmd  string
		deny bool
	}{
		{`python3 -c "open('/etc/passwd').read()"`, true},
		{`python3 -c "open('~/.aws/credentials').read()"`, true},
		{`node -e "require('fs').readFileSync('.env')"`, true},
		{"grep -n 'builder.aws' plan.md", false},
		{"python3 - <<'PY'\nreq=['builder.aws','README']\nPY", false},
		{"grep -rn 'rotate the credentials' docs/", false},
	}
	for _, c := range cases {
		got := guardSubject(root, "shell", c.cmd)
		if c.deny && got == "" {
			t.Errorf("want deny, got allow: %q", c.cmd)
		}
		if !c.deny && got != "" {
			t.Errorf("want allow, got %q: %q", got, c.cmd)
		}
	}
}

// TestConnectorLoadToolExemption pins both halves of issue #643's
// permission story: the deferred-index entry point is exempt under
// every connector's namespaced form, the way load_skill is, while a
// tool loaded THROUGH it stays fully inside the chain. The suffix
// must not leak to the rest of the exempt map either, or a remote
// server could name its way out by ending a tool in "_search_kb".
func TestConnectorLoadToolExemption(t *testing.T) {
	t.Parallel()
	exempt := []string{"load_tool", "github_load_tool", "some-other-mcp_load_tool"}
	notExempt := []string{
		"github_create_issue",
		"github_load_toolbox",  // suffix must land on a "_" boundary
		"github_search_kb",     // an exempt raw name must NOT match by suffix
		"github_remember",      // same
		"my_load_tool_wrapper", // suffix must be at the end
	}

	p := NewPermissions(nil, "/workspace")
	for _, name := range exempt {
		if !isConnectorLoadTool(name) {
			t.Errorf("%s: want exempt as a connector index entry point", name)
		}
	}
	for _, name := range notExempt {
		if isConnectorLoadTool(name) || p.exempt[name] {
			t.Errorf("%s: must stay inside the permission chain", name)
		}
	}
}
