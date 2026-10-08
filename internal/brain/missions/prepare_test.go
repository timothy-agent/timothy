package missions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestTomlKeys: the scanner lists tables, keys, dotted and quoted keys,
// inline-table members, and skips multi-line strings.
func TestTomlKeys(t *testing.T) {
	src := `# comment
[settings]
experimental = true

[tools]
node = "24"
"aqua:google/osv-scanner" = "2.6.0"

[env]
_.path = ["bin"]
APP_ENV = """
multi
[fake.table]
"""

[deps.npm]
[deps.env-template]
run = "x"

[tasks.test]
run = "composer test"
env = { CI = "1" }

[[daemons]]
name = "pg"
`
	keys := tomlKeys(src)
	for _, want := range []string{"settings", "settings.experimental", "tools", "tools.node", "tools.aqua:google/osv-scanner", "env", "env._.path", "env.APP_ENV", "deps", "deps.npm", "deps.env-template", "deps.env-template.run", "tasks", "tasks.test", "tasks.test.run", "tasks.test.env", "tasks.test.env.CI", "daemons", "daemons.name"} {
		if !keys[want] {
			t.Errorf("missing key %q in %v", want, keys)
		}
	}
	if keys["fake"] || keys["fake.table"] {
		t.Errorf("multi-line string content was parsed as a table: %v", keys)
	}
}

// TestRenderMiseLocal: the repo config absent, present without
// conflict, and conflicting. A key the repo declares is never written,
// so the repo's value wins although mise loads the local file over it.
func TestRenderMiseLocal(t *testing.T) {
	in := miseLocalInput{Providers: []string{"composer", "npm"}, EnvTemplate: ".env.example", Artisan: true, TestCmd: "composer test", Lockfiles: []string{"composer.lock", "package-lock.json"}}
	cases := []struct {
		name     string
		repo     string
		want     []string
		wantGone []string
	}{
		{
			"repo config absent",
			"",
			[]string{"experimental = true", `disable_tools = ["php"]`, `"aqua:google/osv-scanner" = "2.6.0"`, "[deps.composer]", "[deps.npm]", "[deps.env-template]", `depends = ["composer"]`, "[tasks.test]\nrun = \"composer test\"", "[tasks.audit]"},
			nil,
		},
		{
			"repo config present without conflict",
			"[tools]\nnode = \"20\"\n[env]\nAPP_ENV = \"testing\"\n",
			[]string{"experimental = true", "[deps.npm]", "[tasks.test]"},
			nil,
		},
		{
			"repo declares the same keys: repo wins",
			"[settings]\nexperimental = false\ndisable_tools = []\n[tools]\n\"aqua:google/osv-scanner\" = \"2.0.0\"\n[deps.composer]\nauto = true\n[deps.env-template]\nrun = \"true\"\n[tasks.test]\nrun = \"vendor/bin/pest\"\n[tasks.audit]\nrun = \"true\"\n",
			[]string{"[deps.npm]"},
			[]string{"experimental", "disable_tools", "osv-scanner", "[deps.composer]", "[deps.env-template]", "[tasks.test]", "[tasks.audit]", "[settings]", "[tools]"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.repo != "" {
				if err := os.WriteFile(filepath.Join(dir, "mise.toml"), []byte(tc.repo), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := renderMiseLocal(in, repoMiseKeys(dir))
			if !strings.HasPrefix(got, miseLocalHeader+"\n") {
				t.Fatalf("missing header:\n%s", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, g := range tc.wantGone {
				if strings.Contains(got, g) {
					t.Errorf("repo-declared %q was written in:\n%s", g, got)
				}
			}
		})
	}
}

// TestRepoMiseKeysLocalFile: a mise.local.toml the repo ships counts as
// the repo's config; one the harness wrote does not.
func TestRepoMiseKeysLocalFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, miseLocalFile), []byte("[tasks.test]\nrun = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !repoMiseKeys(dir)["tasks.test"] {
		t.Fatal("repo-shipped mise.local.toml ignored")
	}
	if err := os.WriteFile(filepath.Join(dir, miseLocalFile), []byte(renderMiseLocal(miseLocalInput{TestCmd: "x"}, nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	if repoMiseKeys(dir)["tasks.test"] {
		t.Fatal("harness-written mise.local.toml read as the repo's")
	}
}

// TestPlanPrepare: providers from root lockfiles (one node provider),
// env template only without an env file, lockfiles from the whole list.
func TestPlanPrepare(t *testing.T) {
	cases := []struct {
		name          string
		files         map[string]string
		wantProviders []string
		wantTemplate  string
		wantLockfiles []string
		wantTests     []string
	}{
		{
			"laravel vite",
			map[string]string{
				"composer.json": `{"scripts":{"test":["@php artisan test"]}}`, "composer.lock": "{}",
				"package.json": `{"scripts":{"build":"vite build"}}`, "package-lock.json": "{}",
				".env.example": "APP_KEY=\n", "artisan": "<?php\n",
			},
			[]string{"composer", "npm"}, ".env.example", []string{"composer.lock", "package-lock.json"},
			[]string{"composer test", "php artisan test"},
		},
		{
			"pnpm with yarn lock too picks the first node provider only",
			map[string]string{"package.json": `{"scripts":{"test":"vitest run"}}`, "pnpm-lock.yaml": "", "yarn.lock": ""},
			[]string{"pnpm"}, "", []string{"pnpm-lock.yaml", "yarn.lock"}, []string{"pnpm test"},
		},
		{
			"uv project",
			map[string]string{"pyproject.toml": "[tool.pytest.ini_options]\n", "uv.lock": ""},
			[]string{"uv"}, "", []string{"uv.lock"}, []string{"uv run pytest -q"},
		},
		{
			"go module, nested lockfile audited, env file already present",
			map[string]string{"go.mod": "module x\n", "go.sum": "", ".env": "A=1\n", ".env.example": "A=\n", "web/package.json": "{}", "web/package-lock.json": "{}"},
			[]string{"go"}, "", []string{"go.mod", "web/package-lock.json"}, []string{"go test ./..."},
		},
		{
			"npm placeholder test script and Makefile target",
			map[string]string{"package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`, "package-lock.json": "{}", "Makefile": "build:\n\tgo build\ntest:\n\tgo test\n"},
			[]string{"npm"}, "", []string{"package-lock.json"}, []string{"make test"},
		},
		{"nothing", map[string]string{"README.md": ""}, nil, "", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, tc.files)
			spec := planPrepare(dir, walkManifests(dir))
			if !reflect.DeepEqual(spec.Providers, tc.wantProviders) {
				t.Errorf("Providers = %v, want %v", spec.Providers, tc.wantProviders)
			}
			if spec.EnvTemplate != tc.wantTemplate {
				t.Errorf("EnvTemplate = %q, want %q", spec.EnvTemplate, tc.wantTemplate)
			}
			if !reflect.DeepEqual(spec.Lockfiles, tc.wantLockfiles) {
				t.Errorf("Lockfiles = %v, want %v", spec.Lockfiles, tc.wantLockfiles)
			}
			var cmds []string
			for _, c := range spec.TestCandidates {
				cmds = append(cmds, c.Cmd)
			}
			if !reflect.DeepEqual(cmds, tc.wantTests) {
				t.Errorf("TestCandidates = %v, want %v", cmds, tc.wantTests)
			}
			if spec.empty() != (tc.name == "nothing") {
				t.Errorf("empty() = %v", spec.empty())
			}
		})
	}
}

// TestTestLadderOrder: a repo mise task comes first, then manifest
// rules, then the Makefile.
func TestTestLadderOrder(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"mise.toml": "[tasks.test]\nrun = \"vendor/bin/pest\"\n", "composer.json": `{"scripts":{"test":"pest"}}`,
		"Cargo.toml": "", "Gemfile": "gem 'rspec'\n", "Makefile": "test:\n\ttrue\n",
	})
	spec := planPrepare(dir, walkManifests(dir))
	var got []string
	for _, c := range spec.TestCandidates {
		got = append(got, c.Cmd+" <- "+c.Source)
	}
	want := []string{"mise run test <- repo mise task", "composer test <- composer.json scripts.test", "cargo test <- Cargo.toml", "bundle exec rspec <- Gemfile rspec", "make test <- Makefile test target"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ladder = %v, want %v", got, want)
	}
	if dir2 := writeTree(t, map[string]string{"mise-tasks/test": "#!/bin/sh\n"}); len(planPrepare(dir2, nil).TestCandidates) != 1 {
		t.Fatal("file task under mise-tasks/ not detected")
	}
}

// TestParseTestSummary covers the runners the ladder can pick.
func TestParseTestSummary(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want TestSummary
	}{
		{"pest passed", "  PASS  Tests\\Unit\\A\n\n  Tests:    36 passed (72 assertions)\n  Duration: 1.2s\n", TestSummary{Passed: 36, Parsed: true}},
		{"pest warnings", "Tests:    36 warnings, 2 passed (72 assertions)\n", TestSummary{Passed: 2, Warnings: 36, Parsed: true}},
		{"pest failed", "Tests:    2 failed, 34 passed (72 assertions)\n", TestSummary{Passed: 34, Failed: 2, Parsed: true}},
		{"phpunit ok", "PHPUnit 10.5\n\n....\n\nOK (36 tests, 72 assertions)\n", TestSummary{Passed: 36, Parsed: true}},
		{"phpunit issues", "OK, but there were issues!\nTests: 36, Assertions: 72, Warnings: 36.\n", TestSummary{Passed: 36, Warnings: 36, Parsed: true}},
		{"phpunit failures", "FAILURES!\nTests: 36, Assertions: 70, Failures: 2.\n", TestSummary{Passed: 34, Failed: 2, Parsed: true}},
		{"pytest", "===== 12 passed, 3 warnings in 0.42s =====\n", TestSummary{Passed: 12, Warnings: 3, Parsed: true}},
		{"jest", "Tests:       5 passed, 5 total\nSnapshots:   0 total\nTime:        1 s\n", TestSummary{Passed: 5, Parsed: true}},
		{"cargo", "test result: ok. 8 passed; 0 failed; 1 ignored; 0 measured\n", TestSummary{Passed: 8, Parsed: true}},
		{"rspec", "Finished in 0.1 seconds\n40 examples, 2 failures\n", TestSummary{Passed: 38, Failed: 2, Parsed: true}},
		{"go test packages", "ok  \tx/a\t0.1s\nFAIL\tx/b\t0.2s\nFAIL\n", TestSummary{Passed: 1, Failed: 1, Parsed: true}},
		{"unknown", "all good\n", TestSummary{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseTestSummary(tc.out); got != tc.want {
				t.Fatalf("parseTestSummary = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// collisionRun is Collision's colored output for one passing file and
// the given recap items, the way Symfony console renders it: gray
// labels, bold colored counts, an OSC 8 file hyperlink.
func collisionRun(recap string) string {
	return "\n  \x1b[30;42;1m PASS \x1b[39;49;22m\x1b[39m Tests\\Unit\\ExampleTest\x1b[39m\n" +
		"  \x1b[32;1m✓\x1b[39;22m\x1b[90m that true is true\x1b[39m \x1b[90m0.01s\x1b[39m\n" +
		"  \x1b]8;;file:///app/tests/Unit/ExampleTest.php\x1b\\tests/Unit/ExampleTest.php\x1b]8;;\x1b\\\n\n" +
		"  \x1b[90mTests:\x1b[39m    " + recap + "\x1b[90m (60 assertions)\x1b[39m\n" +
		"  \x1b[90mDuration:\x1b[39m \x1b[39m0.52s\x1b[39m\n\n"
}

// TestParseTestSummaryCollision: Pest and `php artisan test` output
// through Collision keeps its escapes whatever NO_COLOR says; the recap
// parses after stripping, for every count Collision prints.
func TestParseTestSummaryCollision(t *testing.T) {
	item := func(color, s string) string { return "\x1b[" + color + ";1m" + s + "\x1b[39;22m" }
	sep := "\x1b[90m,\x1b[39m "
	cases := []struct {
		name  string
		recap string
		want  TestSummary
	}{
		{"passed", item("32", "36 passed"), TestSummary{Passed: 36, Parsed: true}},
		{"warnings only", item("33", "36 warnings"), TestSummary{Warnings: 36, Parsed: true}},
		{"warnings and passed", item("33", "36 warnings") + sep + item("32", "2 passed"), TestSummary{Passed: 2, Warnings: 36, Parsed: true}},
		{"every count", item("31", "1 failed") + sep + item("33", "2 deprecated") + sep + item("33", "1 warnings") + sep + item("33", "1 risky") + sep +
			item("33", "1 notice") + sep + item("33", "1 incomplete") + sep + item("34", "1 todo") + sep + item("33", "2 skipped") + sep + item("32", "28 passed"),
			TestSummary{Passed: 28, Failed: 1, Warnings: 1, Parsed: true}},
		{"skipped only", item("33", "3 skipped"), TestSummary{Parsed: true}},
		{"deprecated and passed", item("33", "2 deprecated") + sep + item("32", "34 passed"), TestSummary{Passed: 34, Parsed: true}},
	}
	for _, tc := range cases {
		out := collisionRun(tc.recap)
		for runner, prefix := range map[string]string{"php artisan test": "", "composer test": "\x1b[32m> @php artisan test\x1b[39m\n"} {
			t.Run(tc.name+" via "+runner, func(t *testing.T) {
				if got := parseTestSummary(prefix + out); got != tc.want {
					t.Fatalf("parseTestSummary = %+v, want %+v", got, tc.want)
				}
			})
		}
	}
}

// TestStripANSI removes CSI, OSC (BEL and ST terminated), charset and
// two-byte escapes and keeps the text, UTF-8 included.
func TestStripANSI(t *testing.T) {
	cases := map[string]string{
		"\x1b[32;1m36 passed\x1b[39;22m":                    "36 passed",
		"\x1b]8;;file:///a.php\x1b\\a.php\x1b]8;;\x1b\\":    "a.php",
		"\x1b]0;title\x07done":                              "done",
		"\x1b[1G\x1b[2K  - Installing psr/log (3.0.2): ...": "  - Installing psr/log (3.0.2): ...",
		"\x1b(B\x1b[m✓ ok\x1b=":                             "✓ ok",
		"plain\ntext":                                       "plain\ntext",
	}
	for in, want := range cases {
		if got := stripANSI(in); got != want {
			t.Errorf("stripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPlanPrepareNpmNoLock: a root package.json without any node
// lockfile takes the no-lockfile path; any root node lockfile, or a
// nested package.json only, does not.
func TestPlanPrepareNpmNoLock(t *testing.T) {
	cases := []struct {
		name          string
		files         map[string]string
		want          bool
		wantProviders []string
		wantLockfiles []string
	}{
		{
			"laravel without an npm lockfile",
			map[string]string{
				"composer.json": `{"scripts":{"test":["@php artisan test"]}}`, "composer.lock": "{}", "package.json": `{"scripts":{"build":"vite build"}}`,
				"artisan": "<?php\n", "phpunit.xml": "<phpunit/>", ".env.example": "APP_KEY=\n",
			},
			true, []string{"composer"}, []string{"composer.lock"},
		},
		{"package.json only", map[string]string{"package.json": "{}"}, true, nil, nil},
		{"package-lock.json", map[string]string{"package.json": "{}", "package-lock.json": "{}"}, false, []string{"npm"}, []string{"package-lock.json"}},
		{"npm-shrinkwrap.json", map[string]string{"package.json": "{}", "npm-shrinkwrap.json": "{}"}, false, nil, []string{"npm-shrinkwrap.json"}},
		{"yarn.lock", map[string]string{"package.json": "{}", "yarn.lock": ""}, false, []string{"yarn"}, []string{"yarn.lock"}},
		{"bun.lockb", map[string]string{"package.json": "{}", "bun.lockb": ""}, false, nil, nil},
		{"nested package.json only", map[string]string{"go.mod": "module x\n", "web/package.json": "{}"}, false, nil, []string{"go.mod"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, tc.files)
			spec := planPrepare(dir, walkManifests(dir))
			if spec.NpmNoLock != tc.want {
				t.Errorf("NpmNoLock = %v, want %v", spec.NpmNoLock, tc.want)
			}
			if !reflect.DeepEqual(spec.Providers, tc.wantProviders) || !reflect.DeepEqual(spec.Lockfiles, tc.wantLockfiles) {
				t.Errorf("Providers = %v, Lockfiles = %v, want %v and %v", spec.Providers, spec.Lockfiles, tc.wantProviders, tc.wantLockfiles)
			}
			if tc.want && spec.empty() {
				t.Error("empty() = true with a package.json to install")
			}
		})
	}
}

// npmNoLockFixture is the SumonMSelim/solid-principles-example-laravel
// shape: composer.json and composer.lock, package.json with no
// lockfile, artisan, phpunit.xml, an env template, plus an .npmrc.
func npmNoLockFixture(t *testing.T) (Mission, string) {
	t.Helper()
	ws := t.TempDir()
	wt := filepath.Join(ws, "wt")
	if err := os.MkdirAll(wt, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"composer.json": `{"scripts":{"test":["@php artisan test"]}}`, "composer.lock": "{}",
		"package.json": `{"scripts":{"build":"vite build"}}`, ".npmrc": "fund=false\n",
		".env.example": "APP_KEY=\n", "artisan": "<?php\n", "phpunit.xml": "<phpunit/>",
	} {
		if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Mission{ID: "m-nolock", Kind: KindCoding, Workspace: ws, EnvFacts: &EnvFacts{Manifests: walkManifests(wt)}}, wt
}

// TestPrepareWorkspaceNpmNoLock: package.json without a lockfile gets
// node_modules installed without a lockfile in the worktree, and is
// audited through a package-lock.json generated under the workspace,
// labeled as harness-generated in the facts.
func TestPrepareWorkspaceNpmNoLock(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	stubPrepareTools(t, logFile)
	m, wt := npmNoLockFixture(t)
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: fakeSandboxExec}

	got := p.prepareWorkspace(context.Background(), m)
	f := got.EnvFacts.Prepare
	if f == nil {
		t.Fatal("no prepare facts")
	}
	if !reflect.DeepEqual(f.Installed, []string{"composer", npmNoLockInstalled}) || !dirExists(wt, "node_modules") {
		t.Errorf("Installed = %v", f.Installed)
	}
	if fileExists(wt, "package-lock.json") {
		t.Error("package-lock.json written into the worktree")
	}
	npmDir := filepath.Join(m.Workspace, prepareDir, "npm")
	if !fileExists(npmDir, "package-lock.json") || !fileExists(npmDir, "package.json") || !fileExists(npmDir, ".npmrc") {
		t.Errorf("generated lockfile dir incomplete under %s", npmDir)
	}
	wantAudit := []AuditFact{{Path: "composer.lock", Packages: 1, Vulnerabilities: 1}, {Path: npmGeneratedLockLabel, Packages: 1, Vulnerabilities: 1}}
	if !reflect.DeepEqual(f.Audit, wantAudit) {
		t.Errorf("Audit = %+v, want %+v", f.Audit, wantAudit)
	}
	if len(f.Failures) != 0 {
		t.Errorf("Failures = %v", f.Failures)
	}
	if f.Tests == nil || *f.Tests != (TestSummary{Passed: 36, Parsed: true}) {
		t.Errorf("Tests = %+v", f.Tests)
	}
	var steps []string
	for _, s := range f.Steps {
		steps = append(steps, s.Name)
	}
	if want := []string{"tools", "deps:composer", "deps:npm (no lockfile)", "env-template", "test", "audit:npm-lockfile", "audit"}; !reflect.DeepEqual(steps, want) {
		t.Errorf("steps = %v, want %v", steps, want)
	}
	calls, _ := os.ReadFile(logFile) //nolint:gosec // test-owned log path
	for _, want := range []string{
		"npm install --no-package-lock --no-audit --no-fund\n",
		"npm install --package-lock-only --ignore-scripts --no-audit --no-fund\n",
		"-L composer.lock -L " + filepath.Join(npmDir, "package-lock.json") + " --output-file ",
	} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("missing call %q in:\n%s", want, calls)
		}
	}
	block := renderEnvFacts(got)
	for _, want := range []string{"composer, npm (no lockfile)", npmGeneratedLockLabel + " 1 packages, 1 known advisories"} {
		if !strings.Contains(block, want) {
			t.Errorf("facts block missing %q:\n%s", want, block)
		}
	}
}

// TestPrepareWorkspaceOSVOffline: with osvOffline set, the harness
// audit and the mise audit task both scan the local database only, and
// the counts are still read back (issue #1018).
func TestPrepareWorkspaceOSVOffline(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	stubPrepareTools(t, logFile)
	m, wt := npmNoLockFixture(t)
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: fakeSandboxExec, osvOffline: true}

	got := p.prepareWorkspace(context.Background(), m)
	if f := got.EnvFacts.Prepare; f == nil || len(f.Audit) != 2 || len(f.Failures) != 0 {
		t.Fatalf("prepare facts = %+v", got.EnvFacts.Prepare)
	}
	calls, _ := os.ReadFile(logFile) //nolint:gosec // test-owned log path
	if !strings.Contains(string(calls), "--all-packages --offline-vulnerabilities -L composer.lock") {
		t.Errorf("audit not offline:\n%s", calls)
	}
	local, _ := os.ReadFile(filepath.Join(wt, miseLocalFile)) //nolint:gosec // test-owned path
	if !strings.Contains(string(local), "--offline-vulnerabilities") {
		t.Errorf("audit task not offline:\n%s", local)
	}
}

// TestPrepareWorkspaceNpmNoLockFails: failing npm runs are facts; the
// composer.lock audit still runs and the worktree gets no lockfile.
func TestPrepareWorkspaceNpmNoLockFails(t *testing.T) {
	stubPrepareTools(t, filepath.Join(t.TempDir(), "calls.log"))
	t.Setenv("STUB_NPM_FAIL", "1")
	m, wt := npmNoLockFixture(t)
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: fakeSandboxExec}
	f := p.prepareWorkspace(context.Background(), m).EnvFacts.Prepare
	if f == nil {
		t.Fatal("no prepare facts")
	}
	if !reflect.DeepEqual(f.Installed, []string{"composer"}) || fileExists(wt, "package-lock.json") {
		t.Errorf("Installed = %v", f.Installed)
	}
	joined := strings.Join(f.Failures, "\n")
	for _, want := range []string{"npm install without a lockfile failed", "package.json is not audited"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Failures missing %q: %v", want, f.Failures)
		}
	}
	if want := []AuditFact{{Path: "composer.lock", Packages: 1, Vulnerabilities: 1}}; !reflect.DeepEqual(f.Audit, want) {
		t.Errorf("Audit = %+v, want %+v", f.Audit, want)
	}
}

// TestBuildNpmCmdsRoundTrip runs both npm commands in a real shell: the
// install leaves node_modules and no lockfile in the worktree; the
// lockfile generation copies package.json and .npmrc into a quoted
// directory outside it and writes the lockfile there only.
func TestBuildNpmCmdsRoundTrip(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	stubPrepareTools(t, logFile)
	wt := writeTree(t, map[string]string{"package.json": "{}", ".npmrc": "fund=false\n"})
	run := func(command string) {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-c", command) //nolint:gosec // test runs the harness-composed command
		cmd.Dir = wt
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", command, err, out)
		}
	}
	run(buildNpmNoLockInstallCmd())
	if !dirExists(wt, "node_modules") || fileExists(wt, "package-lock.json") {
		t.Fatal("install left no node_modules or wrote a lockfile into the worktree")
	}
	dir := filepath.Join(t.TempDir(), "it's a dir", "npm")
	run(buildNpmLockfileCmd(dir))
	run(buildNpmLockfileCmd(dir)) // a rerun starts from a clean directory
	for _, name := range []string{"package.json", ".npmrc", "package-lock.json"} {
		if !fileExists(dir, name) {
			t.Errorf("%s missing in %s", name, dir)
		}
	}
	if fileExists(wt, "package-lock.json") {
		t.Error("lockfile generation wrote into the worktree")
	}
	if err := os.Remove(filepath.Join(wt, ".npmrc")); err != nil {
		t.Fatal(err)
	}
	run(buildNpmLockfileCmd(dir))
	if fileExists(dir, ".npmrc") || !fileExists(dir, "package-lock.json") {
		t.Error("rerun without .npmrc kept a stale copy or lost the lockfile")
	}
}

// TestParseOSVReport counts packages and advisory groups per lockfile
// and relativizes paths under the worktree.
func TestParseOSVReport(t *testing.T) {
	raw := `{"results":[
	{"source":{"path":"/w/wt/composer.lock","type":"lockfile"},"packages":[
		{"package":{"name":"a","version":"1","ecosystem":"Packagist"},"groups":[{"ids":["GHSA-1"]},{"ids":["GHSA-2","CVE-2"]}],"vulnerabilities":[{},{},{}]},
		{"package":{"name":"b","version":"1","ecosystem":"Packagist"}}]},
	{"source":{"path":"/elsewhere/package-lock.json","type":"lockfile"},"packages":[
		{"package":{"name":"c","version":"1","ecosystem":"npm"},"vulnerabilities":[{}]}]}
	],"experimental_config":{}}`
	got, err := parseOSVReport([]byte(raw), "/w/wt")
	if err != nil {
		t.Fatal(err)
	}
	want := []AuditFact{{Path: "composer.lock", Packages: 2, Vulnerabilities: 2}, {Path: "/elsewhere/package-lock.json", Packages: 1, Vulnerabilities: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseOSVReport = %+v, want %+v", got, want)
	}
	if _, err := parseOSVReport([]byte("nope"), ""); err == nil {
		t.Fatal("malformed report parsed")
	}
}

// stubPrepareTools puts stub mise, php, composer, npm and osv-scanner
// binaries on PATH so composed commands round-trip through /bin/sh.
// php artisan test prints Collision-style colored output.
// mise handles `deps install <p>` (creates the provider's output, runs
// the env-template script), `install`, `exec -- cmd...` and `run test`.
// Every call is logged to logFile, one argv per line.
func stubPrepareTools(t *testing.T, logFile string) {
	t.Helper()
	bin := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil { //nolint:gosec // test stubs must be executable
			t.Fatal(err)
		}
	}
	write("mise", `#!/bin/sh
printf '%s\n' "mise $*" >> "$STUB_LOG"
case "$1" in
  install) echo "installed tools"; exit 0 ;;
  deps)
    p="$3"
    case "$p" in
      composer) mkdir -p vendor; exit 0 ;;
      npm) mkdir -p node_modules; exit 0 ;;
      env-template) sh -c "$(awk '/^\[deps.env-template\]/{f=1;next} /^\[/{f=0} f && /^run = /{sub(/^run = "/,""); sub(/"$/,""); print}' mise.local.toml)"; exit $? ;;
      *) echo "mise ERROR provider '$p' is inactive" >&2; exit 1 ;;
    esac ;;
  exec) shift; [ "$1" = "--" ] && shift; exec "$@" ;;
  run) echo "ran task $2"; exit 0 ;;
esac
exit 2
`)
	write("php", `#!/bin/sh
printf '%s\n' "php $*" >> "$STUB_LOG"
if [ "$1" = artisan ] && [ "$2" = key:generate ]; then
  sed -i.bak 's/^APP_KEY=$/APP_KEY=base64:stubkey/' .env && rm -f .env.bak; exit 0
fi
if [ "$1" = artisan ] && [ "$2" = test ]; then
  printf '\n  \033[30;42;1m PASS \033[39;49;22m\033[39m Tests\\Unit\\ExampleTest\033[39m\n'
  if grep -q '^APP_KEY=base64:' .env 2>/dev/null; then printf '\n  \033[90mTests:\033[39m    \033[32;1m36 passed\033[39;22m\033[90m (72 assertions)\033[39m\n  \033[90mDuration:\033[39m \033[39m0.52s\033[39m\n'; exit 0; fi
  printf '\n  \033[90mTests:\033[39m    \033[33;1m36 warnings\033[39;22m\033[90m (72 assertions)\033[39m\n'; exit 0
fi
exit 1
`)
	write("composer", `#!/bin/sh
printf '%s\n' "composer $*" >> "$STUB_LOG"
[ "$1" = test ] && printf '\033[32m> @php artisan test\033[39m\n' && exec php artisan test
exit 1
`)
	// npm writes package-lock.json unless told not to, like the real one.
	write("npm", `#!/bin/sh
printf '%s\n' "npm $*" >> "$STUB_LOG"
[ -n "$STUB_NPM_FAIL" ] && { echo "npm ERR! stub failure" >&2; exit 1; }
[ "$1" = install ] || exit 2
case " $* " in
  *" --package-lock-only "*) printf '{"lockfileVersion":3}' > package-lock.json; exit 0 ;;
  *" --no-package-lock "*) mkdir -p node_modules; exit 0 ;;
esac
mkdir -p node_modules && printf '{"lockfileVersion":3}' > package-lock.json
`)
	// osv-scanner reports one package with one advisory per -L lockfile.
	write("osv-scanner", `#!/bin/sh
printf '%s\n' "osv-scanner $*" >> "$STUB_LOG"
out=""
res=""
while [ $# -gt 0 ]; do
  case "$1" in
    --output-file) out="$2"; shift ;;
    -L) p="$2"; case "$p" in /*) ;; *) p="$PWD/$p" ;; esac
        res="$res${res:+,}{\"source\":{\"path\":\"$p\"},\"packages\":[{\"package\":{\"name\":\"a\"},\"groups\":[{\"ids\":[\"GHSA-1\"]}]}]}"; shift ;;
  esac
  shift
done
printf '{"results":[%s]}' "$res" > "$out"
exit 1
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB_LOG", logFile)
}

// laravelFixture is the be8a2860 shape: Laravel + Vite, composer and
// npm lockfiles, an env template, no env file.
func laravelFixture(t *testing.T) (Mission, string) {
	t.Helper()
	ws := t.TempDir()
	wt := filepath.Join(ws, "wt")
	for name, body := range map[string]string{
		"composer.json": `{"scripts":{"test":["@php artisan test"]}}`, "composer.lock": "{}",
		"package.json": `{"scripts":{"build":"vite build"}}`, "package-lock.json": "{}",
		".env.example": "APP_KEY=\nAPP_ENV=local\n", "artisan": "<?php\n",
	} {
		if err := os.MkdirAll(wt, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := Mission{ID: "m-prep", Kind: KindCoding, Workspace: ws, EnvFacts: &EnvFacts{Manifests: walkManifests(wt)}}
	return m, wt
}

// TestPrepareWorkspaceLaravel is the be8a2860 regression at the harness
// level: with no env file the harness copies the template, generates
// the app key, installs both lockfiles, measures the baseline with 36
// passed and 0 warnings, audits both lockfiles, writes the test task,
// and records every step as events and facts. A second call is a no-op.
func TestPrepareWorkspaceLaravel(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	stubPrepareTools(t, logFile)
	m, wt := laravelFixture(t)
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: fakeSandboxExec}

	got := p.prepareWorkspace(context.Background(), m)
	if got.EnvFacts == nil || got.EnvFacts.Prepare == nil {
		t.Fatalf("no prepare facts: %+v", got.EnvFacts)
	}
	f := got.EnvFacts.Prepare
	if !reflect.DeepEqual(f.Installed, []string{"composer", "npm"}) {
		t.Errorf("Installed = %v", f.Installed)
	}
	if f.EnvFile != ".env created from .env.example with a generated app key" {
		t.Errorf("EnvFile = %q", f.EnvFile)
	}
	env, _ := os.ReadFile(filepath.Join(wt, ".env")) //nolint:gosec // test-owned fixture path
	if !strings.Contains(string(env), "APP_KEY=base64:stubkey") {
		t.Errorf(".env = %q, want generated key", env)
	}
	if f.TestCmd != "composer test" || f.TestSource != "composer.json scripts.test" || f.Tests == nil || *f.Tests != (TestSummary{Passed: 36, Parsed: true}) {
		t.Errorf("baseline = %q from %q, %+v", f.TestCmd, f.TestSource, f.Tests)
	}
	wantAudit := []AuditFact{{Path: "composer.lock", Packages: 1, Vulnerabilities: 1}, {Path: "package-lock.json", Packages: 1, Vulnerabilities: 1}}
	if !reflect.DeepEqual(f.Audit, wantAudit) {
		t.Errorf("Audit = %+v, want %+v", f.Audit, wantAudit)
	}
	if len(f.Failures) != 0 || f.CeilingHit {
		t.Errorf("Failures = %v, ceiling %v", f.Failures, f.CeilingHit)
	}
	local, _ := os.ReadFile(filepath.Join(wt, miseLocalFile)) //nolint:gosec // test-owned fixture path
	if !strings.Contains(string(local), "[tasks.test]\nrun = \"composer test\"") || !strings.Contains(string(local), "[deps.env-template]") {
		t.Errorf("mise.local.toml = %s", local)
	}
	if stored := store.missions[m.ID].EnvFacts; stored == nil || !reflect.DeepEqual(stored.Prepare, f) {
		t.Errorf("stored facts differ: %+v", stored)
	}

	events, _ := store.Events(context.Background(), m.ID)
	var kinds, steps []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
		if e.Kind == "mission.prepare_step" {
			var p struct {
				Name     string `json:"name"`
				ExitCode int    `json:"exit_code"`
				Tail     string `json:"tail"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			steps = append(steps, p.Name)
			if p.Name == "test" && (!strings.Contains(p.Tail, "Tests:    36 passed (72 assertions)") || strings.Contains(p.Tail, "\x1b")) {
				t.Errorf("test step tail = %q, want the summary without escapes", p.Tail)
			}
		}
	}
	if kinds[0] != "mission.prepare_started" || kinds[len(kinds)-1] != "mission.prepare_complete" {
		t.Errorf("events = %v", kinds)
	}
	if want := []string{"tools", "deps:composer", "deps:npm", "env-template", "test", "audit"}; !reflect.DeepEqual(steps, want) {
		t.Errorf("steps = %v, want %v", steps, want)
	}
	calls, _ := os.ReadFile(logFile) //nolint:gosec // test-owned log path
	for _, want := range []string{"mise install\n", "mise deps install composer\n", "mise deps install npm\n", "mise deps install env-template\n", "php artisan key:generate --no-interaction\n", "composer test\n", "osv-scanner scan --format json --all-packages -L composer.lock -L package-lock.json --output-file "} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("missing call %q in:\n%s", want, calls)
		}
	}

	// Resume: a completed prepare is never redone.
	before := len(events)
	again := p.prepareWorkspace(context.Background(), got)
	if events, _ = store.Events(context.Background(), m.ID); len(events) != before {
		t.Errorf("second prepare appended events: %d -> %d", before, len(events))
	}
	if !reflect.DeepEqual(again.EnvFacts, got.EnvFacts) {
		t.Error("second prepare changed the facts")
	}
	block := renderEnvFacts(got)
	for _, want := range []string{"Dependencies installed (mise deps): composer, npm", "36 passed, 0 failed, 0 warnings", "composer.lock 1 packages, 1 known advisories"} {
		if !strings.Contains(block, want) {
			t.Errorf("facts block missing %q:\n%s", want, block)
		}
	}
}

// TestPrepareWorkspaceFailuresContinue: a failing deps provider and a
// failing test ladder are recorded as facts with output tails; the
// mission is not failed and the audit still runs.
func TestPrepareWorkspaceFailuresContinue(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	stubPrepareTools(t, logFile)
	ws := t.TempDir()
	wt := filepath.Join(ws, "wt")
	if err := os.MkdirAll(wt, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"package.json": `{"scripts":{"test":"vitest run"}}`, "pnpm-lock.yaml": ""} {
		if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := Mission{ID: "m-fail", Kind: KindCoding, Workspace: ws}
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: fakeSandboxExec}
	got := p.prepareWorkspace(context.Background(), m)
	f := got.EnvFacts.Prepare
	if f == nil {
		t.Fatal("no prepare facts")
	}
	if len(f.Installed) != 0 || f.TestCmd != "" {
		t.Errorf("Installed = %v, TestCmd = %q", f.Installed, f.TestCmd)
	}
	joined := strings.Join(f.Failures, "\n")
	for _, want := range []string{"deps pnpm failed", "no baseline test command succeeded; tried `pnpm test`"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Failures missing %q: %v", want, f.Failures)
		}
	}
	var tails int
	for _, s := range f.Steps {
		if !s.OK && s.Tail != "" {
			tails++
		}
	}
	if tails != 2 {
		t.Errorf("failed steps with tails = %d, want 2 (%+v)", tails, f.Steps)
	}
	if len(f.Audit) != 1 {
		t.Errorf("audit did not run after failures: %+v", f.Audit)
	}
	if block := renderEnvFacts(got); !strings.Contains(block, "Prepare failure: deps pnpm failed") || !strings.Contains(block, "inactive") {
		t.Errorf("facts block lacks the failure and its tail:\n%s", block)
	}
}

// TestPrepareWorkspaceSkips: general missions, missions without a
// sandbox exec and worktrees with nothing to prepare run no step.
func TestPrepareWorkspaceSkips(t *testing.T) {
	store := newFakeStore()
	log := slog.Default()
	ran := false
	run := func(context.Context, string, string, string, string, time.Duration, io.Writer) (int, error) {
		ran = true
		return 0, nil
	}
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "wt"), 0o750); err != nil {
		t.Fatal(err)
	}
	(&provisioner{store: store, log: log, sandboxExec: run}).prepareWorkspace(context.Background(), Mission{ID: "g", Kind: KindGeneral, Workspace: ws})
	(&provisioner{store: store, log: log}).prepareWorkspace(context.Background(), Mission{ID: "c", Kind: KindCoding, Workspace: ws})
	got := (&provisioner{store: store, log: log, sandboxExec: run}).prepareWorkspace(context.Background(), Mission{ID: "empty", Kind: KindCoding, Workspace: ws})
	if ran {
		t.Fatal("a step ran")
	}
	events, _ := store.Events(context.Background(), "empty")
	if len(events) != 1 || events[0].Kind != "mission.prepare_complete" || got.EnvFacts != nil {
		t.Fatalf("empty worktree: events %v, facts %+v", events, got.EnvFacts)
	}
	if _, err := os.Stat(filepath.Join(ws, "wt", miseLocalFile)); err == nil {
		t.Fatal("mise.local.toml written for a worktree with nothing to prepare")
	}
}

// TestPrepareCeiling: once the ceiling is spent no further step starts;
// the skip is recorded once as a failure and as a step event.
func TestPrepareCeiling(t *testing.T) {
	store := newFakeStore()
	r := &prepareRun{p: &provisioner{store: store, log: slog.Default()}, m: Mission{ID: "m"}, deadline: time.Now().Add(-time.Second), facts: &PrepareFacts{}}
	if _, _, ok := r.step(context.Background(), "deps:npm", "true", time.Minute); ok {
		t.Fatal("step ran past the ceiling")
	}
	r.step(context.Background(), "test", "true", time.Minute)
	if !r.facts.CeilingHit || len(r.facts.Failures) != 1 || len(r.facts.Steps) != 0 {
		t.Fatalf("facts = %+v", r.facts)
	}
	events, _ := store.Events(context.Background(), "m")
	if len(events) != 2 {
		t.Fatalf("events = %v, want two skipped step events", events)
	}
}

// TestBuildEnvTemplateRunRoundTrip runs the provider script in a real
// shell: a missing env file is copied and the key generated; an
// existing one is left alone.
func TestBuildEnvTemplateRunRoundTrip(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	stubPrepareTools(t, logFile)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte("APP_KEY=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func() {
		cmd := exec.Command("/bin/sh", "-c", buildEnvTemplateRun(".env.example", true)) //nolint:gosec // test runs the harness-composed script
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script failed: %v: %s", err, out)
		}
	}
	run()
	env, _ := os.ReadFile(filepath.Join(dir, ".env")) //nolint:gosec // test-owned fixture path
	if string(env) != "APP_KEY=base64:stubkey\n" {
		t.Fatalf(".env = %q", env)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_KEY=keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run()
	if env, _ = os.ReadFile(filepath.Join(dir, ".env")); string(env) != "APP_KEY=keep\n" { //nolint:gosec // test-owned fixture path
		t.Fatalf("existing .env was overwritten: %q", env)
	}
	if got := buildEnvTemplateRun(".env.example", false); strings.Contains(got, "artisan") {
		t.Fatalf("no artisan but key:generate composed: %s", got)
	}
}

// TestBuildTestCmdRoundTrip: a quoted candidate survives /bin/sh and
// mise exec; a repo task runs as mise run.
func TestBuildTestCmdRoundTrip(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	stubPrepareTools(t, logFile)
	cmd := exec.Command("/bin/sh", "-c", buildTestCmd(testCandidate{Cmd: `echo "it's $HOME" && printf 'a b'`})) //nolint:gosec // test runs the harness-composed command
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "it's ") || !strings.HasSuffix(string(out), "a b") {
		t.Fatalf("out = %q, err %v", out, err)
	}
	if got := buildTestCmd(testCandidate{Cmd: "mise run test"}); got != "mise run test" {
		t.Fatalf("buildTestCmd(task) = %q", got)
	}
	if got := buildAuditCmd([]string{"a's.lock", "b.lock"}, "/w/out.json", false); got != `mise exec -- osv-scanner scan --format json --all-packages -L 'a'\''s.lock' -L 'b.lock' --output-file '/w/out.json'` {
		t.Fatalf("buildAuditCmd = %s", got)
	}
	if got := buildAuditCmd([]string{"b.lock"}, "", true); got != `mise exec -- osv-scanner scan --format json --all-packages --offline-vulnerabilities -L 'b.lock'` {
		t.Fatalf("buildAuditCmd(offline) = %s", got)
	}
	in := miseLocalInput{Lockfiles: []string{"uv.lock"}, OSVOffline: true}
	if got := renderMiseLocal(in, nil); !strings.Contains(got, `--offline-vulnerabilities -L 'uv.lock'`) {
		t.Fatalf("renderMiseLocal(offline) lacks the offline audit task:\n%s", got)
	}
}

// TestDriverAdvancePreparesBeforeDiscover: Advance runs the prepare
// step once, before the discover turn, and the discover turn already
// sees the prepare facts; a second discover Advance does not redo it.
func TestDriverAdvancePreparesBeforeDiscover(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	stubPrepareTools(t, logFile)
	m, _ := laravelFixture(t)
	m.Phase, m.Status, m.MaxIterations, m.AutoApprovePlan, m.SessionID = PhaseDiscover, StatusWorking, 8, true, "s1"
	store := newFakeStore()
	store.put(m.ID, m)
	var seen *PrepareFacts
	runner := &scriptedRunner{
		discoverNotes: []string{"laravel app", "again"},
		onDiscover: func(_ context.Context, m Mission) {
			if m.EnvFacts != nil {
				seen = m.EnvFacts.Prepare
			}
		},
		plans: []Plan{{Units: []PlanUnit{{Title: "only unit"}}}},
	}
	d := testDriver(store, runner)
	if _, err := d.Advance(context.Background(), m.ID); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if seen == nil || seen.TestCmd != "composer test" {
		t.Fatalf("discover turn saw prepare facts %+v, want the baseline", seen)
	}
	kinds := func() string {
		var ks []string
		for _, e := range store.events[m.ID] {
			ks = append(ks, e.Kind)
		}
		return strings.Join(ks, ",")
	}
	joined := kinds()
	if !strings.Contains(joined, "mission.prepare_complete") || strings.Index(joined, "mission.prepare_complete") > strings.Index(joined, "mission.discover_complete") {
		t.Fatalf("events = %v, want prepare complete before discover", joined)
	}
	mm := store.missions[m.ID]
	mm.Phase = PhaseDiscover
	store.missions[m.ID] = mm
	if _, err := d.Advance(context.Background(), m.ID); err != nil {
		t.Fatalf("second Advance: %v", err)
	}
	if strings.Count(kinds(), "mission.prepare_started") != 1 {
		t.Fatalf("prepare ran again on a second discover turn: %v", kinds())
	}
}
