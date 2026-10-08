package missions

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestDetectServices: CI services, compose files, Rails, Django and
// Laravel configs map to the postgres and redis presets; a newer pinned
// major wins over the default, an older one does not; phpunit.xml
// overrides the env template.
func TestDetectServices(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []serviceNeed
	}{
		{
			"github workflow services",
			map[string]string{".github/workflows/ci.yml": "jobs:\n  test:\n    services:\n      postgres:\n        image: postgres:16\n        env:\n          POSTGRES_PASSWORD: x\n      redis:\n        image: redis\n    steps:\n      - name: redis cache\n"},
			[]serviceNeed{{"postgres", "17", ".github/workflows/ci.yml"}, {"redis", "8", ".github/workflows/ci.yml"}},
		},
		{
			"compose with a newer quoted postgis tag",
			map[string]string{"docker-compose.yml": "services:\n  db:\n    image: 'postgis/postgis:18-3.5'\n"},
			[]serviceNeed{{"postgres", "18", "docker-compose.yml"}},
		},
		{
			"gitlab list services",
			map[string]string{".gitlab-ci.yml": "test:\n  services:\n    - postgres:15\n    - redis:7-alpine\n"},
			[]serviceNeed{{"postgres", "17", ".gitlab-ci.yml"}, {"redis", "8", ".gitlab-ci.yml"}},
		},
		{
			"rails database.yml",
			map[string]string{"config/database.yml": "default: &default\n  adapter: postgresql\n"},
			[]serviceNeed{{"postgres", "17", "config/database.yml"}},
		},
		{
			"rails sqlite",
			map[string]string{"config/database.yml": "default: &default\n  adapter: sqlite3\n"},
			nil,
		},
		{
			"django settings package",
			map[string]string{"proj/settings/base.py": "DATABASES = {'default': {'ENGINE': 'django.db.backends.postgresql'}}\nCACHES = {'default': {'BACKEND': 'django_redis.cache.RedisCache'}}\n"},
			[]serviceNeed{{"postgres", "17", "proj/settings/base.py"}, {"redis", "8", "proj/settings/base.py"}},
		},
		{
			"laravel env template pgsql and redis queue",
			map[string]string{"artisan": "<?php\n", ".env.example": "APP_KEY=\nDB_CONNECTION=pgsql\nQUEUE_CONNECTION=redis\nREDIS_HOST=127.0.0.1\n"},
			[]serviceNeed{{"postgres", "17", ".env.example"}, {"redis", "8", ".env.example"}},
		},
		{
			"laravel phpunit sqlite overrides the template",
			map[string]string{"artisan": "<?php\n", ".env.example": "DB_CONNECTION=pgsql\n", "phpunit.xml": `<php><env name="DB_CONNECTION" value="sqlite"/><env name="QUEUE_CONNECTION" value="sync"/></php>`},
			nil,
		},
		{
			"laravel phpunit pgsql",
			map[string]string{"artisan": "<?php\n", "phpunit.xml.dist": `<php><env name="DB_CONNECTION" value="pgsql"/></php>`},
			[]serviceNeed{{"postgres", "17", "phpunit.xml.dist"}},
		},
		{
			"env template without artisan is not laravel",
			map[string]string{".env.example": "DB_CONNECTION=pgsql\n"},
			nil,
		},
		{"nothing", map[string]string{"README.md": "postgres"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, tc.files)
			if got := detectServices(dir); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("detectServices = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestRenderMiseLocalDaemons: services add pitchfork, a daemon table
// per preset with its data on the /tmp tmpfs, and the test task's
// daemons; a repo-declared daemon or pitchfork is left to the repo.
func TestRenderMiseLocalDaemons(t *testing.T) {
	in := miseLocalInput{TestCmd: "php artisan test", Services: []serviceNeed{{"postgres", "17", "ci"}, {"redis", "8", "ci"}}}
	got := renderMiseLocal(in, nil)
	for _, want := range []string{
		`pitchfork = "` + pitchforkVersion + `"`,
		"[daemons.postgres]\npreset = \"postgres\"\nversion = \"17\"\nport = \"auto\"\ndata_dir = \"/tmp/timothy-daemons/postgres\"\n",
		"[daemons.redis]\npreset = \"redis\"\nversion = \"8\"\nport = \"auto\"\ndata_dir = \"/tmp/timothy-daemons/redis\"\n",
		"[tasks.test]\ndaemons = [\"postgres\", \"redis\"]\nrun = \"php artisan test\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	got = renderMiseLocal(in, map[string]bool{"daemons.postgres": true, "tools.pitchfork": true})
	if strings.Contains(got, "[daemons.postgres]") || strings.Contains(got, "pitchfork =") || !strings.Contains(got, "[daemons.redis]") {
		t.Errorf("repo-declared daemon or pitchfork was written:\n%s", got)
	}
	if got := renderMiseLocal(miseLocalInput{TestCmd: "x"}, nil); strings.Contains(got, "pitchfork") || strings.Contains(got, "daemons") {
		t.Errorf("no services but daemons rendered:\n%s", got)
	}
}

// TestParseDaemonEnv keeps the preset's keys and ignores text around the JSON.
func TestParseDaemonEnv(t *testing.T) {
	out := "mise WARN x\n{\"DATABASE_URL\":\"postgresql://postgres@127.0.0.1:5433/postgres\",\"PGPORT\":\"5433\",\"PATH\":\"/bin\",\"REDIS_URL\":\"redis://127.0.0.1:6379\"}\n"
	pg, _ := presetByName("postgres")
	if got := parseDaemonEnv(out, pg); !reflect.DeepEqual(got, map[string]string{"DATABASE_URL": "postgresql://postgres@127.0.0.1:5433/postgres", "PGPORT": "5433"}) {
		t.Errorf("postgres env = %v", got)
	}
	if got := parseDaemonEnv("not json", pg); got != nil {
		t.Errorf("garbage parsed: %v", got)
	}
}

// laravelPostgresFixture is laravelFixture with a pgsql template and a
// redis queue.
func laravelPostgresFixture(t *testing.T) (Mission, string) {
	t.Helper()
	m, wt := laravelFixture(t)
	if err := os.WriteFile(filepath.Join(wt, ".env.example"), []byte("APP_KEY=\nDB_CONNECTION=pgsql\nQUEUE_CONNECTION=redis\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return m, wt
}

// TestPrepareWorkspaceServices: detected services start before the
// baseline test, their connection variables reach the facts block, and
// the test task starts them again.
func TestPrepareWorkspaceServices(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	stubPrepareTools(t, logFile)
	m, wt := laravelPostgresFixture(t)
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: fakeSandboxExec}
	got := p.prepareWorkspace(context.Background(), m)
	f := got.EnvFacts.Prepare
	if f == nil {
		t.Fatal("no prepare facts")
	}
	var steps []string
	for _, s := range f.Steps {
		steps = append(steps, s.Name)
	}
	if want := []string{"tools", "deps:composer", "deps:npm", "env-template", "daemon:postgres", "daemon:redis", "daemons:env", "test", "audit"}; !reflect.DeepEqual(steps, want) {
		t.Errorf("steps = %v, want %v", steps, want)
	}
	if len(f.Services) != 2 || !f.Services[0].OK || !f.Services[1].OK || f.Services[0].Env["PGHOST"] != "127.0.0.1" || f.Services[1].Env["REDIS_URL"] != "redis://127.0.0.1:6379" || f.Services[0].Env["PATH"] != "" {
		t.Errorf("Services = %+v", f.Services)
	}
	if len(f.Failures) != 0 {
		t.Errorf("Failures = %v", f.Failures)
	}
	local, _ := os.ReadFile(filepath.Join(wt, miseLocalFile)) //nolint:gosec // test-owned fixture path
	for _, want := range []string{"[daemons.postgres]", "[daemons.redis]", "daemons = [\"postgres\", \"redis\"]\nrun = \"composer test\""} {
		if !strings.Contains(string(local), want) {
			t.Errorf("mise.local.toml missing %q:\n%s", want, local)
		}
	}
	block := renderEnvFacts(got)
	for _, want := range []string{"Test service postgres 17 (needed by .env.example) runs in the sandbox", "DATABASE_URL=postgresql://postgres@127.0.0.1:5432/postgres", "PGUSER=postgres", "REDIS_URL=redis://127.0.0.1:6379"} {
		if !strings.Contains(block, want) {
			t.Errorf("facts block missing %q:\n%s", want, block)
		}
	}
}

// TestPrepareWorkspaceServiceFails: a daemon that does not start is
// stopped, dropped from mise.local.toml and recorded; the facts say to
// use sqlite; the other service and the baseline still run.
func TestPrepareWorkspaceServiceFails(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	stubPrepareTools(t, logFile)
	t.Setenv("STUB_DAEMON_FAIL", "postgres")
	m, wt := laravelPostgresFixture(t)
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: fakeSandboxExec}
	got := p.prepareWorkspace(context.Background(), m)
	f := got.EnvFacts.Prepare
	if len(f.Services) != 2 || f.Services[0].OK || f.Services[0].Env != nil || !f.Services[1].OK {
		t.Errorf("Services = %+v", f.Services)
	}
	if f.TestCmd != "composer test" {
		t.Errorf("baseline did not run: %q", f.TestCmd)
	}
	calls, _ := os.ReadFile(logFile) //nolint:gosec // test-owned log path
	if !strings.Contains(string(calls), "mise daemons stop postgres\n") {
		t.Errorf("failed daemon not stopped:\n%s", calls)
	}
	local, _ := os.ReadFile(filepath.Join(wt, miseLocalFile)) //nolint:gosec // test-owned fixture path
	if strings.Contains(string(local), "[daemons.postgres]") || !strings.Contains(string(local), "daemons = [\"redis\"]") {
		t.Errorf("mise.local.toml still carries the failed daemon:\n%s", local)
	}
	block := renderEnvFacts(got)
	for _, want := range []string{"Prepare failure: test service postgres did not start", "no database service; use sqlite where the project supports it", "pitchfork error: postgres not ready"} {
		if !strings.Contains(block, want) {
			t.Errorf("facts block missing %q:\n%s", want, block)
		}
	}
}

// TestBuildDaemonCmdsRoundTrip runs the composed daemon commands in a
// real shell against an argument-logging mise.
func TestBuildDaemonCmdsRoundTrip(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls.log")
	stubPrepareTools(t, logFile)
	for _, c := range []string{buildDaemonStartCmd("post gres'x"), buildDaemonStopCmd("redis"), buildDaemonEnvCmd()} {
		if out, err := exec.Command("/bin/sh", "-c", c).CombinedOutput(); err != nil { //nolint:gosec // test runs the harness-composed command
			t.Fatalf("%s: %v: %s", c, err, out)
		}
	}
	calls, _ := os.ReadFile(logFile) //nolint:gosec // test-owned log path
	if want := "mise daemons start post gres'x\nmise daemons stop redis\nmise env --json\n"; string(calls) != want {
		t.Fatalf("calls = %q, want %q", calls, want)
	}
}
