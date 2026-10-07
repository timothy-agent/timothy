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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func writeMarkers(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectToolchainVersions(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		files map[string]string
		want  map[string]string
	}{
		{"no markers", "", nil, map[string]string{}},
		{".python-version", "python", map[string]string{".python-version": "3.10\n"}, map[string]string{"python": "3.10"}},
		{".python-version comment and pypy skipped", "python", map[string]string{".python-version": "# pin\npypy3.9\n"}, map[string]string{}},
		{"runtime.txt", "python", map[string]string{"runtime.txt": "python-3.10.4\n"}, map[string]string{"python": "3.10.4"}},
		{"pyproject range", "python", map[string]string{"pyproject.toml": "[project]\nrequires-python = \">=3.10,<3.11\"\n"}, map[string]string{"python": "3.10"}},
		{"pyproject compatible release", "python", map[string]string{"pyproject.toml": "[project]\nrequires-python = '~=3.10'\n"}, map[string]string{"python": "3.10"}},
		{"pyproject lower bound with patch", "python", map[string]string{"pyproject.toml": "requires-python = \">=3.9.2\"\n"}, map[string]string{"python": "3.9"}},
		{"pyproject wildcard", "python", map[string]string{"pyproject.toml": "requires-python = \"==3.10.*\"\n"}, map[string]string{"python": "3.10"}},
		{"pyproject exclusion skipped", "python", map[string]string{"pyproject.toml": "requires-python = \"!=3.9.*\"\n"}, map[string]string{}},
		{"python precedence", "python", map[string]string{".python-version": "3.11", "runtime.txt": "python-3.9.1", "pyproject.toml": "requires-python = \">=3.8\""}, map[string]string{"python": "3.11"}},
		{"runtime.txt beats pyproject", "python", map[string]string{"runtime.txt": "python-3.9.1", "pyproject.toml": "requires-python = \">=3.8\""}, map[string]string{"python": "3.9.1"}},
		{"nvmrc with v", "node", map[string]string{".nvmrc": "v18.20.4\n"}, map[string]string{"node": "18.20.4"}},
		{"nvmrc lts skipped", "node", map[string]string{".nvmrc": "lts/*\n"}, map[string]string{}},
		{".node-version", "node", map[string]string{".node-version": "20\n"}, map[string]string{"node": "20"}},
		{"engines range", "node", map[string]string{"package.json": `{"engines":{"node":">=18 <19"}}`}, map[string]string{"node": "18"}},
		{"engines caret", "node", map[string]string{"package.json": `{"engines":{"node":"^18"}}`}, map[string]string{"node": "18"}},
		{"engines x-range", "node", map[string]string{"package.json": `{"engines":{"node":"18.x"}}`}, map[string]string{"node": "18"}},
		{"engines alternatives skipped", "node", map[string]string{"package.json": `{"engines":{"node":"^16 || ^18"}}`}, map[string]string{}},
		{"engines upper bound only skipped", "node", map[string]string{"package.json": `{"engines":{"node":"<19"}}`}, map[string]string{}},
		{"bad package.json", "node", map[string]string{"package.json": `{`}, map[string]string{}},
		{"nvmrc beats engines", "node", map[string]string{".nvmrc": "20", "package.json": `{"engines":{"node":">=18"}}`}, map[string]string{"node": "20"}},
		{"go.mod", "go", map[string]string{"go.mod": "module x\n\ngo 1.22.3\n\ntoolchain go1.23.0\n"}, map[string]string{"go": "1.22.3"}},
		{"go.mod minor only", "go", map[string]string{"go.mod": "module x\ngo 1.22\n"}, map[string]string{"go": "1.22"}},
		{".tool-versions", "", map[string]string{".tool-versions": "python 3.10.4 3.9.1\nnodejs 18.20.4 # lts\ngolang 1.22\nruby system\n"}, map[string]string{"python": "3.10.4", "node": "18.20.4", "go": "1.22"}},
		{".mise.toml string, list and table", "java", map[string]string{".mise.toml": "[env]\nX = \"1\"\n\n[tools]\npython = \"3.12\"\nnode = [\"20\", \"18\"]\ngo = { version = \"1.22\" }\n"}, map[string]string{"python": "3.12", "node": "20", "go": "1.22"}},
		{"language marker beats tool-versions", "python", map[string]string{".python-version": "3.11", ".tool-versions": "python 3.9"}, map[string]string{"python": "3.11"}},
		{"tool-versions beats mise.toml", "", map[string]string{".tool-versions": "python 3.9", ".mise.toml": "[tools]\npython = \"3.12\"\nnode = \"20\"\n"}, map[string]string{"python": "3.9", "node": "20"}},
		{"python env ignores node markers", "python", map[string]string{".nvmrc": "18", ".python-version": "3.10"}, map[string]string{"python": "3.10"}},
		{"go env ignores python markers", "go", map[string]string{".python-version": "3.10", "go.mod": "go 1.22"}, map[string]string{"go": "1.22"}},
		{"base env reads every language marker", "base", map[string]string{".python-version": "3.10", ".nvmrc": "18"}, map[string]string{"python": "3.10", "node": "18"}},
		{"hostile version rejected", "", map[string]string{".tool-versions": "python 3.10;rm\nnode $(x)\n"}, map[string]string{"python": "3.10"}},
		{"hostile tool name rejected", "", map[string]string{".tool-versions": "py$thon 3.10\n"}, map[string]string{}},
		{"unsupported tools ignored", "", map[string]string{".tool-versions": "ruby 3.3.0\nphp 8.2\n", ".mise.toml": "[tools]\njava = \"21\"\n"}, map[string]string{}},
		{"composer require caret", "php", map[string]string{"composer.json": `{"require":{"php":"^8.1"}}`}, map[string]string{"php": "8.1"}},
		{"composer platform beats require", "php", map[string]string{"composer.json": `{"require":{"php":"^8.1"},"config":{"platform":{"php":"8.3.12"}}}`}, map[string]string{"php": "8.3"}},
		{"composer laravel 9 lower bound clamps to baked", "php", map[string]string{"composer.json": `{"require":{"php":"^8.0.2"}}`}, map[string]string{"php": "8.1"}},
		{"composer unbaked exact kept for the install to report", "php", map[string]string{"composer.json": `{"require":{"php":"7.4.33"}}`}, map[string]string{"php": "7.4"}},
		{"composer alternatives skipped", "php", map[string]string{"composer.json": `{"require":{"php":"^7.4|^8.0"}}`}, map[string]string{}},
		{"composer without php constraint", "php", map[string]string{"composer.json": `{"require":{"laravel/framework":"^12.0"}}`}, map[string]string{}},
		{"bad composer.json", "php", map[string]string{"composer.json": `{`}, map[string]string{}},
		{"php tool-versions in php env", "php", map[string]string{".tool-versions": "php 8.2.10\n"}, map[string]string{"php": "8.2"}},
		{"composer beats tool-versions", "php", map[string]string{"composer.json": `{"require":{"php":"^8.3"}}`, ".tool-versions": "php 8.1\n"}, map[string]string{"php": "8.3"}},
		{"php ignored outside php env", "base", map[string]string{"composer.json": `{"require":{"php":"^8.1"}}`, ".mise.toml": "[tools]\nphp = \"8.2\"\n"}, map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectToolchainVersions(writeMarkers(t, tc.files), tc.env)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("detectToolchainVersions = %v, want %v", got, tc.want)
			}
		})
	}
	if got := detectToolchainVersions("", ""); len(got) != 0 {
		t.Fatalf("empty worktree = %v, want none", got)
	}
}

func TestGoalToolchainVersions(t *testing.T) {
	cases := []struct {
		goal string
		want map[string]string
	}{
		{"Build a Django app on Python 3.10", map[string]string{"python": "3.10"}},
		{"use python3.11.4 and Node.js 20", map[string]string{"python": "3.11.4", "node": "20"}},
		{"NodeJS v18.20 frontend, Golang 1.22 backend", map[string]string{"node": "18.20", "go": "1.22"}},
		{"Go 1.23.1 service", map[string]string{"go": "1.23.1"}},
		{"python 3.10 first, then python 3.12", map[string]string{"python": "3.10"}},
		{"go 2 steps back, python 3 rewrite, node 8", map[string]string{}},
		{"Django 4.2 upgrade", map[string]string{}},
		{"node 180 workers", map[string]string{}},
		{"Laravel app on PHP 8.2", map[string]string{"php": "8.2"}},
		{"php8.1.27 legacy fix", map[string]string{"php": "8.1"}},
		{"php 8 rewrite", map[string]string{}},
		{"", map[string]string{}},
	}
	for _, tc := range cases {
		if got := goalToolchainVersions(tc.goal); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("goalToolchainVersions(%q) = %v, want %v", tc.goal, got, tc.want)
		}
	}
}

func TestDetectMissionToolchainsGoalFallback(t *testing.T) {
	dir := writeMarkers(t, map[string]string{".python-version": "3.11"})
	got := detectMissionToolchains(dir, "", "Python 3.10 API with a Node 18 frontend")
	want := map[string]string{"python": "3.11", "node": "18"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("marker must win, goal fills the rest: got %v, want %v", got, want)
	}
	if got := detectMissionToolchains("", "python", "a CLI tool"); len(got) != 0 {
		t.Fatalf("no marker, no goal version = %v, want none", got)
	}
	if got := detectMissionToolchains("", "php", "Laravel on PHP 8.2"); !reflect.DeepEqual(got, map[string]string{"php": "8.2"}) {
		t.Fatalf("php env goal = %v, want php 8.2", got)
	}
	if got := detectMissionToolchains("", "node", "port the PHP 8.2 app to Node 20"); !reflect.DeepEqual(got, map[string]string{"node": "20"}) {
		t.Fatalf("non-php env must drop the goal's php: got %v", got)
	}
}

func TestNormalizePHPVersion(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"^8.1", "8.1", true},
		{">=8.2", "8.2", true},
		{"~8.1", "8.1", true},
		{"~8.1.3", "8.1", true},
		{"^8.0.2", "8.1", true},
		{"^8", "8.1", true},
		{">=7.4", "7.4", true},
		{"8.2.*", "8.2", true},
		{"8.0.30", "8.0", true},
		{"^8.5", "8.5", true},
		{"^7.4|^8.0", "", false},
		{"^7.4 || ^8.0", "", false},
		{"<9", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := normalizePHPVersion(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("normalizePHPVersion(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestBuildPHPSelectCmdRoundTrip runs the php selection through /bin/sh
// against a fake bin dir: the requested minor is linked, a missing
// minor fails naming it, and a hostile version stays one value.
func TestBuildPHPSelectCmdRoundTrip(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"php8.1", "phar8.1", "php8.4", "phar8.4", "phar.phar8.4"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho "+name+"\n"), 0o700); err != nil { //nolint:gosec // test stub must be executable
			t.Fatal(err)
		}
	}
	run := func(t *testing.T, version, link string) (string, error) {
		dir := t.TempDir()
		cmd := exec.Command("/bin/sh", "-c", buildPHPSelectCmd(version, bin, link)) //nolint:gosec // test-authored command
		cmd.Dir = dir
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		if _, serr := os.Stat(filepath.Join(dir, "pwned")); serr == nil {
			t.Fatal("injected command ran")
		}
		return string(out), err
	}
	t.Run("links the minor's binaries", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "nested", "bin")
		if out, err := run(t, "8.4", link); err != nil {
			t.Fatalf("run: %v: %s", err, out)
		}
		for name, want := range map[string]string{"php": "php8.4", "phar": "phar8.4", "phar.phar": "phar.phar8.4"} {
			got, err := os.Readlink(filepath.Join(link, name))
			if err != nil || got != filepath.Join(bin, want) {
				t.Errorf("%s -> %q (%v), want %q", name, got, err, filepath.Join(bin, want))
			}
		}
	})
	t.Run("relinks and skips absent binaries", func(t *testing.T) {
		link := t.TempDir()
		if out, err := run(t, "8.4", link); err != nil {
			t.Fatalf("run 8.4: %v: %s", err, out)
		}
		if out, err := run(t, "8.1", link); err != nil {
			t.Fatalf("run 8.1: %v: %s", err, out)
		}
		if got, _ := os.Readlink(filepath.Join(link, "php")); got != filepath.Join(bin, "php8.1") {
			t.Errorf("php -> %q, want php8.1", got)
		}
	})
	t.Run("missing minor fails naming it", func(t *testing.T) {
		link := t.TempDir()
		out, err := run(t, "7.4", link)
		if err == nil || !strings.Contains(out, "php 7.4 is not installed") {
			t.Fatalf("err = %v, out = %q; want failure naming 7.4", err, out)
		}
		if _, serr := os.Lstat(filepath.Join(link, "php")); serr == nil {
			t.Fatal("php linked despite missing minor")
		}
	})
	t.Run("hostile version stays one value", func(t *testing.T) {
		if _, err := run(t, "8.1'; touch pwned; '", t.TempDir()); err == nil {
			t.Fatal("hostile version succeeded")
		}
	})
}

func TestBuildToolchainInstallCmdPHP(t *testing.T) {
	phpOnly := buildToolchainInstallCmd(map[string]string{"php": "8.1"})
	if phpOnly != buildPHPSelectCmd("8.1", phpBinDir, phpLinkDir) {
		t.Fatalf("php only = %q, want the php select alone", phpOnly)
	}
	mixed := buildToolchainInstallCmd(map[string]string{"php": "8.1", "node": "20"})
	want := buildPHPSelectCmd("8.1", phpBinDir, phpLinkDir) + " && " + miseLocked("mise use --global 'node@20'")
	if mixed != want {
		t.Fatalf("mixed = %q, want %q", mixed, want)
	}
}

// TestBuildToolchainInstallCmdRoundTrip runs the composed command
// through /bin/sh with a stub mise that prints its argv, so quoting
// bugs a string-compare fake would forgive show up.
func TestBuildToolchainInstallCmdRoundTrip(t *testing.T) {
	bin := t.TempDir()
	stub := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(bin, "mise"), []byte(stub), 0o700); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   map[string]string
		want []string
	}{
		{"sorted", map[string]string{"python": "3.10", "node": "18"}, []string{"use", "--global", "node@18", "python@3.10"}},
		{"quote in version survives as one arg", map[string]string{"python": "3.10'; touch pwned; '"}, []string{"use", "--global", "python@3.10'; touch pwned; '"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command("/bin/sh", "-c", buildToolchainInstallCmd(tc.in)) //nolint:gosec // test-authored command
			cmd.Dir = dir
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
				t.Fatal("injected command ran")
			}
		})
	}
}

// TestMiseLockedSerializesRoundTrip runs two miseLocked commands at once
// through /bin/sh with a stub mise that records its start and end: the
// lock on the data dir (D-131) must keep them from overlapping, create a
// missing data dir, and pass the exit code through.
func TestMiseLockedSerializesRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock not installed")
	}
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "calls")
	stub := "#!/bin/sh\necho \"start $1\" >> \"$CALL_LOG\"\nsleep 0.3\necho \"end $1\" >> \"$CALL_LOG\"\n[ \"$1\" != fail ]\n"
	if err := os.WriteFile(filepath.Join(bin, "mise"), []byte(stub), 0o700); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}
	dataDir := filepath.Join(t.TempDir(), "not-yet", "mise")
	run := func(arg string) *exec.Cmd {
		cmd := exec.Command("/bin/sh", "-c", miseLocked("mise "+shQuote(arg))) //nolint:gosec // test-authored command
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "MISE_DATA_DIR=" + dataDir, "CALL_LOG=" + logPath}
		return cmd
	}
	a, b := run("a"), run("b")
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	if err := a.Wait(); err != nil {
		t.Fatalf("a: %v", err)
	}
	if err := b.Wait(); err != nil {
		t.Fatalf("b: %v", err)
	}
	raw, err := os.ReadFile(logPath) //nolint:gosec // test-owned log path
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 4 || strings.Fields(lines[0])[1] != strings.Fields(lines[1])[1] || strings.Fields(lines[2])[1] != strings.Fields(lines[3])[1] {
		t.Fatalf("calls overlapped: %q", lines)
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".timothy-install.lock")); err != nil {
		t.Fatalf("lock file not in the data dir: %v", err)
	}
	if err := run("fail").Run(); err == nil {
		t.Fatal("failing mise exited 0 through the lock")
	}
}

type recordedExec struct {
	mu    sync.Mutex
	cmds  []string
	envs  []string
	code  int
	err   error
	write string
	// probes are the environment facts tool probes (issue #1008),
	// kept out of cmds and envs; values are the image each ran on.
	probes []string
}

func (r *recordedExec) exec(_ context.Context, _, environment, _, command string, _ time.Duration, out io.Writer) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if command == buildToolProbeCmd() {
		r.probes = append(r.probes, environment)
		return 0, nil
	}
	r.cmds = append(r.cmds, command)
	r.envs = append(r.envs, environment)
	_, _ = io.WriteString(out, r.write)
	return r.code, r.err
}

func eventKinds(store *fakeStore, id string) []string {
	var out []string
	for _, e := range store.events[id] {
		out = append(out, e.Kind)
	}
	return out
}

func toolchainRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	requireGitForPush(t)
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare", "-b", "main")
	seed := t.TempDir()
	gitRun(t, seed, "init", "-q", "-b", "main")
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, seed, "add", name)
	}
	gitRun(t, seed, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "seed")
	gitRun(t, seed, "remote", "add", "origin", bare)
	gitRun(t, seed, "push", "-q", "origin", "main")
	return bare
}

func provisionToolchainMission(t *testing.T, files map[string]string, ex *recordedExec) (Mission, *fakeStore) {
	t.Helper()
	bare := toolchainRepo(t, files)
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Goal: "go ahead and fix the login page", Kind: "coding",
		Sources: []SourceEntry{{Source: SourceKindGitHub, RepoURL: bare, ConnectorID: "conn1"}},
		Phase:   PhaseBuild, Status: StatusWorking, MaxIterations: 8,
	})
	workspace := NewWorkspace(t.TempDir(), nil, slog.Default())
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, workspace, &fakeSessionCreator{}, &fakeGranter{}, ex.exec, nil, slog.Default())
	d.SetCloneTokenResolver(func(context.Context, string) (string, error) { return "dummy-token", nil })
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	return m, store
}

func TestProvisionInstallsDetectedToolchains(t *testing.T) {
	ex := &recordedExec{}
	m, store := provisionToolchainMission(t, map[string]string{"pyproject.toml": "[project]\nname='x'\n", ".python-version": "3.10\n"}, ex)
	if !reflect.DeepEqual(m.Toolchains, map[string]string{"python": "3.10"}) {
		t.Fatalf("Toolchains = %v, want python 3.10", m.Toolchains)
	}
	if len(ex.cmds) != 1 || ex.cmds[0] != miseLocked("mise use --global 'python@3.10'") || ex.envs[0] != "python" {
		t.Fatalf("exec cmds = %q envs = %q, want one install on the python image", ex.cmds, ex.envs)
	}
	if !reflect.DeepEqual(ex.probes, []string{"python"}) {
		t.Fatalf("tool probes = %q, want one on the python image", ex.probes)
	}
	if m.EnvFacts == nil || m.EnvFacts.BaseBranch != "main" || !reflect.DeepEqual(m.EnvFacts.Manifests, []string{"pyproject.toml"}) {
		t.Fatalf("EnvFacts = %+v, want base branch main and pyproject.toml", m.EnvFacts)
	}
	if !slices.Contains(eventKinds(store, "m1"), "mission.toolchain_installed") {
		t.Fatalf("events = %v, want mission.toolchain_installed", eventKinds(store, "m1"))
	}
	if got := (&Driver{store: store, log: slog.Default()}).toolchainInstallState(context.Background(), m); got != "installed" {
		t.Fatalf("install state = %q, want installed", got)
	}
}

func TestProvisionWithoutMarkersRunsNoInstall(t *testing.T) {
	ex := &recordedExec{}
	m, store := provisionToolchainMission(t, map[string]string{"package.json": "{}"}, ex)
	if len(m.Toolchains) != 0 || len(ex.cmds) != 0 {
		t.Fatalf("Toolchains = %v, exec cmds = %q, want none", m.Toolchains, ex.cmds)
	}
	for _, k := range eventKinds(store, "m1") {
		if strings.HasPrefix(k, "mission.toolchain_") {
			t.Fatalf("unexpected event %s", k)
		}
	}
}

func TestProvisionToolchainInstallFailureContinues(t *testing.T) {
	ex := &recordedExec{code: 1, write: "mise ERROR no matching version python@0.0.1"}
	m, store := provisionToolchainMission(t, map[string]string{"pyproject.toml": "x", ".python-version": "0.0.1\n"}, ex)
	if m.Workspace == "" || m.SessionID == "" {
		t.Fatalf("mission not provisioned after failed install: %+v", m)
	}
	var payload map[string]any
	for _, e := range store.events["m1"] {
		if e.Kind == "mission.toolchain_install_failed" {
			_ = json.Unmarshal(e.Payload, &payload)
		}
	}
	if msg, _ := payload["error"].(string); !strings.Contains(msg, "no matching version") {
		t.Fatalf("failure payload = %v, want mise stderr", payload)
	}
	m.ToolchainInstall = (&Driver{store: store, log: slog.Default()}).toolchainInstallState(context.Background(), m)
	if m.ToolchainInstall != "failed" {
		t.Fatalf("install state = %q, want failed", m.ToolchainInstall)
	}
}

func TestInstallToolchainsTruncatesOutput(t *testing.T) {
	store := newFakeStore()
	ex := &recordedExec{code: 1, write: strings.Repeat("a", toolchainErrCap*3) + "TAIL"}
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: ex.exec}
	got, failed := p.installToolchains(context.Background(), Mission{ID: "m1", Toolchains: map[string]string{"python": "3.10"}}, t.TempDir())
	if !failed || len(got) > toolchainErrCap+3 || !strings.HasSuffix(got, "TAIL") {
		t.Fatalf("failure text len %d, want the tail within the cap", len(got))
	}
}

func TestDiscoverToolchainNudge(t *testing.T) {
	tc := map[string]string{"python": "3.10", "node": "18"}
	cases := []struct {
		name string
		m    Mission
		want string
	}{
		{"installed", Mission{Kind: KindCoding, Toolchains: tc, ToolchainInstall: "installed"}, "Toolchains installed: node 18, python 3.10. Do not reinstall them."},
		{"failed", Mission{Kind: KindCoding, Toolchains: tc, ToolchainInstall: "failed"}, "bootstrap unit (bootstrap: true)"},
		{"none", Mission{Kind: KindCoding}, ""},
		{"no install state", Mission{Kind: KindCoding, Toolchains: tc}, ""},
		{"general", Mission{Kind: "general", Toolchains: tc, ToolchainInstall: "installed"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := discoverToolchainNudge(c.m)
			if c.want == "" && got != "" || !strings.Contains(got, c.want) {
				t.Fatalf("nudge = %q, want to contain %q", got, c.want)
			}
		})
	}
	if got := discoverToolchainNudge(Mission{Kind: KindCoding, Toolchains: tc, ToolchainInstall: "failed"}); !strings.Contains(got, "python 3.10") {
		t.Fatalf("failed nudge = %q, want the attempted toolchains", got)
	}
}

func TestDiscoverSessionCarriesToolchainNudge(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{toolEndEvent(discoverNotesToolName, `{"findings":"ok"}`)}}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Kind: KindCoding, Route: "default", Goal: "x", Environment: "python", Toolchains: map[string]string{"python": "3.10"}, ToolchainInstall: "installed"}
	if _, _, _, err := r.DiscoverSession(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(agent.requests[0].System, "Toolchains installed: python 3.10. Do not reinstall them.") {
		t.Fatalf("system prompt lacks the toolchain nudge:\n%s", agent.requests[0].System)
	}
}

// TestDiscoverOverrideRedetectsToolchains covers the discover report
// replacing a marker-detected environment: the sink receives the
// toolchains detected for the NEW environment from the worktree.
func TestDiscoverOverrideRedetectsToolchains(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, "wt")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{".python-version": "3.10", ".nvmrc": "18"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{toolEndEvent(discoverNotesToolName, `{"findings":"django","environment":"python"}`)}}}
	r := newTestRunner(agent)
	sink := &toolchainSink{}
	r.SetEnvironmentSink(sink)
	m := Mission{ID: "m1", Kind: KindCoding, Route: "default", Goal: "x", Environment: "node", EnvironmentMarker: "package.json", Workspace: ws}
	if _, _, _, err := r.DiscoverSession(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sink.toolchains, map[string]string{"python": "3.10"}) {
		t.Fatalf("toolchains for the new environment = %v, want python 3.10 only", sink.toolchains)
	}
}

type toolchainSink struct{ toolchains map[string]string }

func (s *toolchainSink) SetEnvironment(_ context.Context, _, _, _ string, _ []string, toolchains map[string]string) error {
	s.toolchains = toolchains
	return nil
}

// TestDriverDiscoverReinstallsToolchainsAfterRecreate covers the
// recreate path: new container, toolchains installed again, and a
// failed install lands in the notes the planner reads.
func TestDriverDiscoverReinstallsToolchainsAfterRecreate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     int
		wantNote bool
	}{{"ok", 0, false}, {"failed", 1, true}} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.put("m1", Mission{ID: "m1", Kind: KindCoding, Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true,
				SessionID: "s1", Workspace: "/already/provisioned"})
			remover := &fakeSandboxRemover{}
			ex := &recordedExec{code: tc.code}
			runner := &scriptedRunner{
				discoverNotes: []string{"django app"},
				onDiscover: func(ctx context.Context, m Mission) {
					_ = store.SetEnvironment(ctx, m.ID, "python", "discover", nil, map[string]string{"python": "3.10"})
				},
				plans: []Plan{{Units: []PlanUnit{{Title: "only unit"}}}},
			}
			d := NewDriver(store, runner, nil, &fakeSessionCreator{}, &fakeGranter{}, ex.exec, remover, slog.Default())
			if _, err := d.Advance(context.Background(), "m1"); err != nil {
				t.Fatalf("Advance: %v", err)
			}
			if len(ex.cmds) != 1 || ex.cmds[0] != miseLocked("mise use --global 'python@3.10'") || ex.envs[0] != "python" {
				t.Fatalf("exec cmds = %q envs = %q, want one install on the python image", ex.cmds, ex.envs)
			}
			if !reflect.DeepEqual(ex.probes, []string{"python"}) {
				t.Fatalf("tool probes = %q, want a re-probe on the python image", ex.probes)
			}
			m, _ := store.Get(context.Background(), "m1")
			if got := strings.Contains(m.DiscoverNotes, "bootstrap unit"); got != tc.wantNote {
				t.Fatalf("notes = %q, bootstrap note = %v, want %v", m.DiscoverNotes, got, tc.wantNote)
			}
		})
	}
}
