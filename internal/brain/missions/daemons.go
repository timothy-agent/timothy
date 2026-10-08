package missions

// D-142 (issue #1017): test databases through mise daemons. When the
// repo's CI services, compose files or test config need PostgreSQL or
// Redis, the harness mise.local.toml declares the mise preset (unless
// the repo config does) and prepare starts it before the baseline test.
// The daemons run as processes in the mission container under the
// existing limits (measured idle: about 110 MiB and 30 tasks for both).
// Both presets listen on 127.0.0.1 only and have no auth; prepare
// records the observed listen addresses and the facts warn when a
// repo-declared daemon binds anything else.
// data_dir sits on the /tmp tmpfs, so a removed or restarted container
// takes the data with it; mise's default data dir is under
// MISE_STATE_DIR on the shared cache volume, which outlives missions.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// daemonDataRoot holds each daemon's data dir inside the /tmp tmpfs.
const daemonDataRoot = "/tmp/timothy-daemons"

// pitchforkTool supervises mise daemons. Declared so `mise daemons
// stop` resolves it: mise installs it on start but sets no version.
const (
	pitchforkTool    = "pitchfork"
	pitchforkVersion = "2.29.0"
)

// daemonPreset is one supported mise daemon preset.
type daemonPreset struct {
	name string
	// defaultMajor is used unless a source pins a newer major.
	defaultMajor int
	// envKeys are the connection variables the preset exports, plus the
	// <NAME>_PORT a repo-declared custom daemon with a port exports.
	envKeys []string
	// noService is the facts line when the preset cannot start.
	noService string
}

// daemonPresets in render order.
var daemonPresets = []daemonPreset{
	{"postgres", 17, []string{"DATABASE_URL", "PGHOST", "PGPORT", "PGUSER", "PGDATABASE", "POSTGRES_PORT"}, "no database service; use sqlite where the project supports it"},
	{"redis", 8, []string{"REDIS_URL", "REDIS_PORT"}, "no redis service; use the project's non-redis cache, queue and session drivers where it supports them"},
}

// presetByName returns the preset named name.
func presetByName(name string) (daemonPreset, bool) {
	for _, p := range daemonPresets {
		if p.name == name {
			return p, true
		}
	}
	return daemonPreset{}, false
}

// serviceNeed is a preset the repo needs, the major to run and the
// first file that asked for it.
type serviceNeed struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"`
}

// ServiceFact is one test service prepare started or failed to start.
type ServiceFact struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Source  string            `json:"source"`
	OK      bool              `json:"ok"`
	Env     map[string]string `json:"env,omitempty"`
	// Listen is the observed listen addresses on the service port.
	// The presets bind 127.0.0.1 with no auth; a repo-declared daemon
	// may not, so a non-loopback address is warned about in the facts.
	Listen []string `json:"listen,omitempty"`
}

// imageRef matches a postgres, postgis or redis image reference ending a
// line: `image: postgres:16`, `- redis`, `name: "postgis/postgis:16-3.4"`.
var imageRef = regexp.MustCompile(`(?m)(?:image:|name:|^\s*-)\s*["']?(?:[\w.-]+/)*(postgres|postgis|redis)(?::([0-9]+)[\w.-]*)?["']?\s*$`)

// railsPostgres matches a PostgreSQL adapter in config/database.yml.
var railsPostgres = regexp.MustCompile(`(?m)^\s*adapter:\s*["']?(postgresql|postgis)\b`)

// djangoPostgres and djangoRedis match Django settings backends.
var (
	djangoPostgres = regexp.MustCompile(`django\.db\.backends\.postgresql|django\.contrib\.gis\.db\.backends\.postgis|["']postgres(ql)?://`)
	djangoRedis    = regexp.MustCompile(`django_redis|django\.core\.cache\.backends\.redis|["']redis://`)
)

// phpunitEnv matches `<env name="X" value="Y"/>` (or <server>) in phpunit.xml.
var phpunitEnv = regexp.MustCompile(`<(?:env|server)\s+name="([A-Z_]+)"\s+value="([^"]*)"`)

// dotenvLine matches `KEY=value` in an env template.
var dotenvLine = regexp.MustCompile(`(?m)^[ \t]*([A-Z_]+)[ \t]*=[ \t]*["']?([^"'\s#]*)`)

// laravelRedisKeys select redis as a Laravel driver.
var laravelRedisKeys = []string{"CACHE_STORE", "CACHE_DRIVER", "QUEUE_CONNECTION", "SESSION_DRIVER"}

// detectServices lists the presets the worktree needs, sorted by
// preset order. Sources: CI workflows, compose files, Rails
// database.yml, Django settings, Laravel phpunit.xml and env template.
func detectServices(worktree string) []serviceNeed {
	if worktree == "" {
		return nil
	}
	found := map[string]serviceNeed{}
	add := func(name, major, source string) {
		p, ok := presetByName(name)
		if !ok {
			return
		}
		v := p.defaultMajor
		if n, err := strconv.Atoi(major); err == nil && n > v {
			v = n
		}
		cur, seen := found[name]
		if !seen {
			found[name] = serviceNeed{Name: name, Version: strconv.Itoa(v), Source: source}
			return
		}
		if n, _ := strconv.Atoi(cur.Version); v > n {
			cur.Version = strconv.Itoa(v)
			found[name] = cur
		}
	}
	for _, rel := range serviceImageFiles(worktree) {
		for _, m := range imageRef.FindAllStringSubmatch(readHead(worktree, rel), -1) {
			name := m[1]
			if name == "postgis" {
				name = "postgres"
			}
			add(name, m[2], rel)
		}
	}
	if railsPostgres.MatchString(readHead(worktree, "config/database.yml")) {
		add("postgres", "", "config/database.yml")
	}
	for _, rel := range djangoSettingsFiles(worktree) {
		body := readHead(worktree, rel)
		if djangoPostgres.MatchString(body) {
			add("postgres", "", rel)
		}
		if djangoRedis.MatchString(body) {
			add("redis", "", rel)
		}
	}
	laravelServices(worktree, add)
	var out []serviceNeed
	for _, p := range daemonPresets {
		if s, ok := found[p.name]; ok {
			out = append(out, s)
		}
	}
	return out
}

// serviceImageFiles lists the CI and compose files that may name a
// service image, worktree-relative and sorted.
func serviceImageFiles(worktree string) []string {
	var out []string
	for _, pattern := range []string{".github/workflows/*.yml", ".github/workflows/*.yaml", "docker-compose*.yml", "docker-compose*.yaml", "compose*.yml", "compose*.yaml"} {
		matches, _ := filepath.Glob(filepath.Join(worktree, filepath.FromSlash(pattern)))
		for _, m := range matches {
			if rel, err := filepath.Rel(worktree, m); err == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
	}
	if fileExists(worktree, ".gitlab-ci.yml") {
		out = append(out, ".gitlab-ci.yml")
	}
	sort.Strings(out)
	return out
}

// djangoSettingsFiles lists settings.py files and modules in a settings
// package, to depth 3 under the worktree.
func djangoSettingsFiles(worktree string) []string {
	var out []string
	for _, pattern := range []string{"settings.py", "*/settings.py", "*/*/settings.py", "*/settings/*.py", "*/*/settings/*.py"} {
		matches, _ := filepath.Glob(filepath.Join(worktree, filepath.FromSlash(pattern)))
		for _, m := range matches {
			if rel, err := filepath.Rel(worktree, m); err == nil && !strings.HasPrefix(rel, ".") {
				out = append(out, filepath.ToSlash(rel))
			}
		}
	}
	sort.Strings(out)
	return out
}

// laravelServices reads DB_CONNECTION and the redis driver keys from
// phpunit.xml (or .dist), then the env templates for keys phpunit.xml
// does not set: the test run sees phpunit's values first.
func laravelServices(worktree string, add func(name, major, source string)) {
	if !fileExists(worktree, "artisan") {
		return
	}
	vals := map[string]string{}
	srcs := map[string]string{}
	for _, f := range []string{"phpunit.xml", "phpunit.xml.dist"} {
		for _, m := range phpunitEnv.FindAllStringSubmatch(readHead(worktree, f), -1) {
			if _, ok := vals[m[1]]; !ok {
				vals[m[1]], srcs[m[1]] = m[2], f
			}
		}
	}
	for _, f := range envTemplateNames {
		for _, m := range dotenvLine.FindAllStringSubmatch(readHead(worktree, f), -1) {
			if _, ok := vals[m[1]]; !ok {
				vals[m[1]], srcs[m[1]] = m[2], f
			}
		}
	}
	if vals["DB_CONNECTION"] == "pgsql" {
		add("postgres", "", srcs["DB_CONNECTION"])
	}
	for _, k := range laravelRedisKeys {
		if vals[k] == "redis" {
			add("redis", "", srcs[k])
		}
	}
}

// readHead returns the first manifestScanBytes of worktree/rel, "" when
// it is not a regular file.
func readHead(worktree, rel string) string {
	if !fileExists(worktree, filepath.FromSlash(rel)) {
		return ""
	}
	f, err := os.Open(filepath.Join(worktree, filepath.FromSlash(rel))) //nolint:gosec // fixed names under the mission worktree
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, manifestScanBytes)
	n, _ := f.Read(buf)
	return string(buf[:n])
}

// renderDaemons renders the [daemons.<name>] tables for the services
// the repo config does not declare.
func renderDaemons(services []serviceNeed, repoKeys map[string]bool) string {
	var b strings.Builder
	for _, s := range services {
		if repoKeys["daemons."+s.Name] {
			continue
		}
		fmt.Fprintf(&b, "\n[daemons.%s]\npreset = %q\nversion = %q\nport = \"auto\"\ndata_dir = %q\n",
			s.Name, s.Name, s.Version, daemonDataRoot+"/"+s.Name)
	}
	return b.String()
}

// serviceNames lists the services' daemon names.
func serviceNames(services []serviceNeed) []string {
	names := make([]string, 0, len(services))
	for _, s := range services {
		names = append(names, s.Name)
	}
	return names
}

// buildDaemonStartCmd starts one daemon and waits for its readiness check.
func buildDaemonStartCmd(name string) string {
	return "mise daemons start " + shQuote(name)
}

// buildDaemonStopCmd stops one daemon.
func buildDaemonStopCmd(name string) string {
	return "mise daemons stop " + shQuote(name)
}

// buildDaemonEnvCmd prints the mise environment as JSON, daemon
// connection variables included.
func buildDaemonEnvCmd() string {
	return "mise env --json"
}

// buildListenProbeCmd prints the kernel's TCP socket tables; tcp6 is
// absent when IPv6 is disabled.
func buildListenProbeCmd() string {
	return "cat /proc/net/tcp && { cat /proc/net/tcp6 2>/dev/null || true; }"
}

// servicePort is the TCP port a started service's variables name, 0
// when unknown.
func servicePort(s ServiceFact) int {
	raw := s.Env["PGPORT"]
	if u := s.Env["REDIS_URL"]; raw == "" && u != "" {
		if parsed, err := url.Parse(u); err == nil {
			raw = parsed.Port()
		}
	}
	if raw == "" {
		raw = s.Env[strings.ToUpper(s.Name)+"_PORT"]
	}
	n, _ := strconv.Atoi(raw)
	return n
}

// tcpListen is the /proc/net/tcp* state of a listening socket.
const tcpListen = "0A"

// parseListenAddrs returns the addresses with a listening socket on port
// in /proc/net/tcp and tcp6 output, sorted and deduplicated.
func parseListenAddrs(out string, port int) []string {
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[3] != tcpListen {
			continue
		}
		hexAddr, hexPort, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		if p, err := strconv.ParseUint(hexPort, 16, 16); err != nil || int(p) != port {
			continue
		}
		if ip := procNetIP(hexAddr); ip != nil {
			seen[ip.String()] = true
		}
	}
	addrs := make([]string, 0, len(seen))
	for a := range seen {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	return addrs
}

// procNetIP decodes a /proc/net address: 32-bit words in host (little
// endian) byte order, one word for IPv4, four for IPv6.
func procNetIP(h string) net.IP {
	b, err := hex.DecodeString(h)
	if err != nil || (len(b) != net.IPv4len && len(b) != net.IPv6len) {
		return nil
	}
	ip := make(net.IP, len(b))
	for i := 0; i < len(b); i += 4 {
		ip[i], ip[i+1], ip[i+2], ip[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return ip
}

// nonLoopback returns the addresses that are not loopback.
func nonLoopback(addrs []string) []string {
	var out []string
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip == nil || !ip.IsLoopback() {
			out = append(out, a)
		}
	}
	return out
}

// parseDaemonEnv picks the preset's connection variables out of
// `mise env --json` output, ignoring any text around the JSON object.
func parseDaemonEnv(out string, p daemonPreset) map[string]string {
	start, end := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return nil
	}
	var all map[string]string
	if err := json.Unmarshal([]byte(out[start:end+1]), &all); err != nil {
		return nil
	}
	env := map[string]string{}
	for _, k := range p.envKeys {
		if v, ok := all[k]; ok {
			env[k] = v
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// renderServiceFacts renders one line per test service for the facts block.
func renderServiceFacts(services []ServiceFact) string {
	var b strings.Builder
	for _, s := range services {
		p, _ := presetByName(s.Name)
		if !s.OK {
			fmt.Fprintf(&b, "- Test service %s %s (needed by %s) could not start: %s.\n", s.Name, NeutralizeSlot(s.Version), NeutralizeSlot(s.Source), p.noService)
			continue
		}
		keys := make([]string, 0, len(s.Env))
		for k := range s.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		vars := make([]string, 0, len(keys))
		for _, k := range keys {
			vars = append(vars, k+"="+NeutralizeSlot(s.Env[k]))
		}
		if len(vars) == 0 {
			vars = append(vars, "connection variables unknown, see `mise env`")
		}
		fmt.Fprintf(&b, "- Test service %s %s (needed by %s) runs in the sandbox as a mise daemon, no password: %s. `mise exec` and `mise run` export these; point the project's own test settings at them. `mise daemons start %s` restarts it after a sandbox restart; its data is temporary.\n",
			s.Name, NeutralizeSlot(s.Version), NeutralizeSlot(s.Source), strings.Join(vars, ", "), s.Name)
		if exposed := nonLoopback(s.Listen); len(exposed) > 0 {
			fmt.Fprintf(&b, "- Warning: test service %s listens on %s, not only loopback, with no password; other hosts on the sandbox network can reach it. Bind it to 127.0.0.1 before running anything that stores real data.\n",
				s.Name, NeutralizeSlot(strings.Join(exposed, ", ")))
		}
	}
	return b.String()
}
