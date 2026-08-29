package tools

import (
	"context"
	"encoding/json"
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
		{name: "redirect outside workspace", command: "cat file 2>&1 >/etc/passwd", blocked: "outside the workspace"},

		{name: "quoted regex leading slash", command: "awk '/^#{1,6}/ {print}' README.md"},
		{name: "quoted regex alternation", command: "grep -E '^(foo|bar)$' /workspace/file"},
		{name: "quoted sed pattern", command: "sed 's/^#//' notes.md"},
		{name: "quoted secret path still denied", command: "cat '/etc/passwd'", blocked: "system dirs"},
		{name: "quoted plain path still denied", command: "cat '/Users/someone/x'", blocked: "outside the workspace"},
		{name: "quoted brace path exempt", command: "cat '/tmp/{a,b}'"},
		{name: "unquoted brace path denied", command: "cat /tmp/{a,b}", blocked: "outside the workspace"},
		{name: "quoted metachars relative redirect", command: `echo "^foo$" > out.md`},
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

	t.Run("mission sentinel tools exempt", func(t *testing.T) {
		t.Parallel()
		for _, tool := range []string{"mission_status", "review_verdict", "submit_plan", "explore_notes"} {
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
