//go:build integration

package missions

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Prepare step against the real sandbox images (D-130). Needs a docker
// CLI, PREPARE_SANDBOX_IMAGE (the base image, npm fixture) and
// optionally PREPARE_SANDBOX_PHP_IMAGE (the php image, Laravel-shaped
// fixture), plus PREPARE_SANDBOX_WORKSPACE: a directory the Docker
// daemon can bind-mount at the same path this process sees it. Network
// is needed for mise, npm and the OSV database.

// dockerRunExec is a sandboxExec over `docker run`: the workspace is
// mounted at its own path, the command runs as the sandbox uid under
// coreutils timeout, exactly as sandboxd execs it.
func dockerRunExec(image string) sandboxExec {
	return func(ctx context.Context, _, _, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		ws := filepath.Dir(workdir)
		secs := strconv.Itoa(int(timeout / time.Second))
		cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-u", "65534:65534", "-v", ws+":"+ws, "-w", workdir, image, //nolint:gosec // integration test over the sandbox image
			"timeout", "-k", "5", secs, "/bin/sh", "-c", command)
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode(), nil
			}
			return 0, err
		}
		return 0, nil
	}
}

// prepareFixture writes files under a fresh mission workspace in the
// shared root and returns the mission.
func prepareFixture(t *testing.T, files map[string]string) Mission {
	t.Helper()
	root := os.Getenv("PREPARE_SANDBOX_WORKSPACE")
	if root == "" {
		t.Skip("PREPARE_SANDBOX_WORKSPACE not set")
	}
	ws, err := os.MkdirTemp(root, "prepare-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(ws) })
	if err := os.Chmod(ws, 0o777); err != nil { //nolint:gosec // the sandbox uid must write here
		t.Fatal(err)
	}
	wt := filepath.Join(ws, "wt")
	for name, body := range files {
		p := filepath.Join(wt, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil { //nolint:gosec // see above
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o666); err != nil { //nolint:gosec // see above
			t.Fatal(err)
		}
	}
	if err := os.Chmod(wt, 0o777); err != nil { //nolint:gosec // see above
		t.Fatal(err)
	}
	return Mission{ID: "it-prepare", Kind: KindCoding, Workspace: ws}
}

// TestPrepareSandboxNpm: an npm project with a vulnerable lockfile
// installs node_modules through mise deps, runs its test script as the
// baseline and audits the lockfile with advisories counted.
func TestPrepareSandboxNpm(t *testing.T) {
	image := os.Getenv("PREPARE_SANDBOX_IMAGE")
	if image == "" {
		t.Skip("PREPARE_SANDBOX_IMAGE not set")
	}
	m := prepareFixture(t, map[string]string{
		"package.json": `{"name":"fixture","version":"1.0.0","private":true,"scripts":{"test":"node --test"},"dependencies":{"lodash":"4.17.15"}}`,
		"test.js":      "const test = require('node:test');\ntest('ok', () => {});\n",
	})
	run := dockerRunExec(image)
	wt := m.WorktreePath()
	var out strings.Builder
	if code, err := run(context.Background(), m.ID, "", wt, "npm install --package-lock-only --ignore-scripts", 2*time.Minute, &out); err != nil || code != 0 {
		t.Fatalf("lockfile generation: %d %v\n%s", code, err, out.String())
	}
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: run}
	got := p.prepareWorkspace(context.Background(), m)
	f := got.EnvFacts.Prepare
	if f == nil {
		t.Fatal("no prepare facts")
	}
	t.Logf("prepare facts: %+v", f)
	if len(f.Failures) != 0 {
		t.Errorf("failures: %v", f.Failures)
	}
	if !containsString(f.Installed, "npm") || !dirExists(wt, "node_modules") {
		t.Errorf("npm not installed: %v", f.Installed)
	}
	if f.TestCmd != "npm test" {
		t.Errorf("TestCmd = %q", f.TestCmd)
	}
	if len(f.Audit) != 1 || f.Audit[0].Path != "package-lock.json" || f.Audit[0].Packages == 0 || f.Audit[0].Vulnerabilities == 0 {
		t.Errorf("Audit = %+v, want lodash advisories on package-lock.json", f.Audit)
	}
	local, _ := os.ReadFile(filepath.Join(wt, miseLocalFile))
	if !strings.Contains(string(local), "[tasks.test]\nrun = \"npm test\"") {
		t.Errorf("mise.local.toml lacks the test task:\n%s", local)
	}
	if code, err := run(context.Background(), m.ID, "", wt, "mise run test && mise run audit >/dev/null; test $? -le 1", time.Minute, &out); err != nil || code != 0 {
		t.Errorf("mise run test/audit after prepare: %d %v\n%s", code, err, out.String())
	}
}

// artisanStub is a PHP script with the two artisan commands prepare
// touches: key:generate writes APP_KEY into .env; test reports 36
// passed with a key and 36 warnings without one (the be8a2860 shape),
// colored the way Collision prints it.
const artisanStub = `<?php
$cmd = $argv[1] ?? '';
if ($cmd === 'key:generate') {
    $env = file_get_contents('.env');
    file_put_contents('.env', preg_replace('/^APP_KEY=$/m', 'APP_KEY=base64:' . base64_encode(random_bytes(32)), $env));
    exit(0);
}
if ($cmd === 'test') {
    $env = @file_get_contents('.env');
    if ($env !== false && preg_match('/^APP_KEY=base64:/m', $env)) { echo "  \e[90mTests:\e[39m    \e[32;1m36 passed\e[39;22m\e[90m (72 assertions)\e[39m\n"; exit(0); }
    echo "  \e[90mTests:\e[39m    \e[33;1m36 warnings\e[39;22m\e[90m (72 assertions)\e[39m\n"; exit(0);
}
exit(1);
`

// TestPrepareSandboxLaravelShape is the be8a2860 regression in the php
// image: composer and npm lockfiles, an env template and no env file.
// Prepare installs both, creates .env with an app key, and the baseline
// reports 36 passed and 0 warnings.
func TestPrepareSandboxLaravelShape(t *testing.T) {
	image := os.Getenv("PREPARE_SANDBOX_PHP_IMAGE")
	if image == "" {
		t.Skip("PREPARE_SANDBOX_PHP_IMAGE not set")
	}
	m := prepareFixture(t, map[string]string{
		"composer.json": `{"name":"fixture/app","require":{"psr/log":"^3.0"},"scripts":{"test":["@php artisan test"]}}`,
		"package.json":  `{"name":"fixture","version":"1.0.0","private":true,"dependencies":{"lodash":"4.17.15"}}`,
		".env.example":  "APP_NAME=Fixture\nAPP_KEY=\n",
		"artisan":       artisanStub,
	})
	run := dockerRunExec(image)
	wt := m.WorktreePath()
	var out strings.Builder
	if code, err := run(context.Background(), m.ID, "", wt, "composer update --no-install --no-interaction && npm install --package-lock-only --ignore-scripts", 3*time.Minute, &out); err != nil || code != 0 {
		t.Fatalf("lockfile generation: %d %v\n%s", code, err, out.String())
	}
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: run}
	got := p.prepareWorkspace(context.Background(), m)
	f := got.EnvFacts.Prepare
	if f == nil {
		t.Fatal("no prepare facts")
	}
	t.Logf("prepare facts: %+v", f)
	if len(f.Failures) != 0 {
		t.Errorf("failures: %v", f.Failures)
	}
	if fmt.Sprint(f.Installed) != "[composer npm]" || !dirExists(wt, "vendor") || !dirExists(wt, "node_modules") {
		t.Errorf("installs = %v", f.Installed)
	}
	env, _ := os.ReadFile(filepath.Join(wt, ".env"))
	if !strings.Contains(string(env), "APP_KEY=base64:") {
		t.Errorf(".env = %q, want a generated key", env)
	}
	if f.TestCmd != "composer test" || f.Tests == nil || f.Tests.Passed != 36 || f.Tests.Warnings != 0 {
		t.Errorf("baseline = %q %+v, want composer test with 36 passed and 0 warnings", f.TestCmd, f.Tests)
	}
	if len(f.Audit) != 2 {
		t.Errorf("Audit = %+v, want composer.lock and package-lock.json", f.Audit)
	}
}

// TestPrepareSandboxLaravelNoNpmLock is the solid-principles-example-laravel
// shape: composer.lock but a package.json with no lockfile. npm installs
// node_modules without writing a lockfile into the worktree, and the
// lockfile generated under the workspace is audited with lodash's
// advisories.
func TestPrepareSandboxLaravelNoNpmLock(t *testing.T) {
	image := os.Getenv("PREPARE_SANDBOX_PHP_IMAGE")
	if image == "" {
		t.Skip("PREPARE_SANDBOX_PHP_IMAGE not set")
	}
	m := prepareFixture(t, map[string]string{
		"composer.json": `{"name":"fixture/app","require":{"psr/log":"^3.0"},"scripts":{"test":["@php artisan test"]}}`,
		"package.json":  `{"name":"fixture","version":"1.0.0","private":true,"dependencies":{"lodash":"4.17.15"}}`,
		".env.example":  "APP_NAME=Fixture\nAPP_KEY=\n",
		"phpunit.xml":   "<phpunit/>\n",
		"artisan":       artisanStub,
	})
	run := dockerRunExec(image)
	wt := m.WorktreePath()
	var out strings.Builder
	if code, err := run(context.Background(), m.ID, "", wt, "composer update --no-install --no-interaction", 3*time.Minute, &out); err != nil || code != 0 {
		t.Fatalf("lockfile generation: %d %v\n%s", code, err, out.String())
	}
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: run}
	got := p.prepareWorkspace(context.Background(), m)
	f := got.EnvFacts.Prepare
	if f == nil {
		t.Fatal("no prepare facts")
	}
	t.Logf("prepare facts: %+v", f)
	if len(f.Failures) != 0 {
		t.Errorf("failures: %v", f.Failures)
	}
	if fmt.Sprint(f.Installed) != "[composer "+npmNoLockInstalled+"]" || !dirExists(wt, "node_modules/lodash") {
		t.Errorf("installs = %v", f.Installed)
	}
	if fileExists(wt, "package-lock.json") {
		t.Error("package-lock.json written into the worktree")
	}
	if f.TestCmd != "composer test" || f.Tests == nil || f.Tests.Passed != 36 || f.Tests.Warnings != 0 {
		t.Errorf("baseline = %q %+v, want composer test with 36 passed and 0 warnings", f.TestCmd, f.Tests)
	}
	var npmAudit *AuditFact
	for i := range f.Audit {
		if f.Audit[i].Path == npmGeneratedLockLabel {
			npmAudit = &f.Audit[i]
		}
	}
	if len(f.Audit) != 2 || npmAudit == nil || npmAudit.Packages == 0 || npmAudit.Vulnerabilities == 0 {
		t.Errorf("Audit = %+v, want composer.lock and the generated lockfile with lodash advisories", f.Audit)
	}
	for _, e := range store.events[m.ID] {
		if strings.Contains(string(e.Payload), `\u001b`) {
			t.Errorf("event %s keeps escape sequences: %s", e.Kind, e.Payload)
		}
	}
}
