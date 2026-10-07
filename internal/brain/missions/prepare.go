package missions

// D-130 (issue #1010): the harness prepares a coding mission's workspace
// with mise before the discover turn, outside any model turn: tool
// install (osv-scanner), `mise deps` providers for the lockfiles at the
// root, an env template copy with a Laravel app key, one baseline test
// command that must succeed once, and an osv-scanner audit of every
// lockfile. Every step runs through the sandbox exec path under a total
// ceiling and per-step timeouts; failures are recorded as facts and the
// mission continues. Resume derives completion from mission_events.

import (
	"context"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// prepareCeiling bounds the whole prepare step.
	prepareCeiling = 25 * time.Minute
	// prepareMinStep is the remaining budget below which no further
	// step starts.
	prepareMinStep = 15 * time.Second
	// Per-step timeouts, each clamped to the remaining ceiling.
	prepareToolsTimeout = 5 * time.Minute
	prepareDepsTimeout  = 10 * time.Minute
	prepareEnvTimeout   = 2 * time.Minute
	prepareTestTimeout  = 10 * time.Minute
	prepareAuditTimeout = 3 * time.Minute
	// prepareTailCap bounds the output tail kept per step event.
	prepareTailCap = 2000
	// prepareFactTailCap bounds the tail kept on a failed step's fact.
	prepareFactTailCap = 600
	// prepareTestTries bounds how many ladder candidates run.
	prepareTestTries = 3
)

// prepareDir under the mission workspace (outside the worktree) holds
// the audit report the harness reads back.
const prepareDir = "prepare"

// osv-scanner exit codes that are results, not failures: advisories
// found, and no packages in the given files.
const (
	osvExitAdvisories = 1
	osvExitNoPackages = 128
)

// PrepareFacts is the prepare step's outcome, stored on EnvFacts and
// rendered into every phase prompt.
type PrepareFacts struct {
	Steps []PrepareStepFact `json:"steps,omitempty"`
	// Installed lists the deps providers whose install succeeded.
	Installed []string `json:"installed,omitempty"`
	// EnvFile is the env file the harness created, with its template.
	EnvFile    string       `json:"env_file,omitempty"`
	TestCmd    string       `json:"test_cmd,omitempty"`
	TestSource string       `json:"test_source,omitempty"`
	Tests      *TestSummary `json:"tests,omitempty"`
	Audit      []AuditFact  `json:"audit,omitempty"`
	Failures   []string     `json:"failures,omitempty"`
	CeilingHit bool         `json:"ceiling_hit,omitempty"`
}

// PrepareStepFact is one executed step. Tail is kept on failure only.
type PrepareStepFact struct {
	Name       string `json:"name"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	OK         bool   `json:"ok"`
	Tail       string `json:"tail,omitempty"`
}

// prepareDone reports whether a mission.prepare_complete event exists.
func (p *provisioner) prepareDone(ctx context.Context, id string) bool {
	events, err := p.store.Events(ctx, id)
	if err != nil {
		p.log.Warn("driver: read prepare state failed; preparing again", "mission_id", id, "error", err)
		return false
	}
	for _, e := range events {
		if e.Kind == "mission.prepare_complete" {
			return true
		}
	}
	return false
}

// prepareWorkspace runs the prepare step for a coding mission with a
// worktree, once per mission. Returns m with its EnvFacts updated.
func (p *provisioner) prepareWorkspace(ctx context.Context, m Mission) Mission {
	if m.Kind != KindCoding || p.sandboxExec == nil {
		return m
	}
	wt := m.WorktreePath()
	if wt == "" || p.prepareDone(ctx, m.ID) {
		return m
	}
	var facts EnvFacts
	if m.EnvFacts != nil {
		facts = *m.EnvFacts
	}
	manifests := facts.Manifests
	if m.EnvFacts == nil {
		manifests = walkManifests(wt)
	}
	spec := planPrepare(wt, manifests)
	if spec.empty() {
		p.appendPrepareEvent(ctx, m.ID, "mission.prepare_complete", map[string]any{"skipped": "no manifests, env template or lockfiles at the worktree root"})
		return m
	}
	start := time.Now()
	r := &prepareRun{p: p, m: m, wt: wt, deadline: start.Add(prepareCeiling), facts: &PrepareFacts{}}
	p.appendPrepareEvent(ctx, m.ID, "mission.prepare_started", map[string]any{
		"providers": spec.Providers, "env_template": spec.EnvTemplate, "test_candidates": spec.TestCandidates, "lockfiles": spec.Lockfiles,
	})
	r.run(ctx, spec)
	payload := map[string]any{
		"duration_ms": time.Since(start).Milliseconds(), "installed": r.facts.Installed, "env_file": r.facts.EnvFile,
		"test_cmd": r.facts.TestCmd, "audit": r.facts.Audit, "failures": r.facts.Failures, "ceiling_hit": r.facts.CeilingHit,
	}
	p.appendPrepareEvent(ctx, m.ID, "mission.prepare_complete", payload)
	facts.Prepare = r.facts
	if err := p.store.SetEnvFacts(ctx, m.ID, facts); err != nil {
		p.log.Warn("driver: store prepare facts failed", "mission_id", m.ID, "error", err)
	}
	m.EnvFacts = &facts
	return m
}

// appendPrepareEvent records one prepare event; a store failure is logged.
func (p *provisioner) appendPrepareEvent(ctx context.Context, id, kind string, payload map[string]any) {
	if err := p.store.AppendEvent(ctx, id, kind, payload); err != nil {
		p.log.Warn("driver: record prepare event failed", "mission_id", id, "kind", kind, "error", err)
	}
}

// prepareRun carries one prepare execution.
type prepareRun struct {
	p        *provisioner
	m        Mission
	wt       string
	deadline time.Time
	facts    *PrepareFacts
}

// run executes the spec in order: config, tools, deps, env template,
// baseline test, audit.
func (r *prepareRun) run(ctx context.Context, spec prepareSpec) {
	local := miseLocalInput{Providers: spec.Providers, EnvTemplate: spec.EnvTemplate, Artisan: spec.Artisan, Lockfiles: spec.Lockfiles}
	if err := r.writeMiseLocal(local, spec.RepoKeys); err != nil {
		r.fail("writing " + miseLocalFile + ": " + err.Error())
		return
	}
	if _, _, ok := r.step(ctx, "tools", miseLocked("mise install"), prepareToolsTimeout); !ok {
		r.fail("mise install (tools from the repo config and osv-scanner) failed; the audit may be unavailable")
	}
	for _, name := range spec.Providers {
		_, _, ok := r.step(ctx, "deps:"+name, buildDepsCmd(name), prepareDepsTimeout)
		if ok {
			if out := providerOutput(name); out != "" && !dirExists(r.wt, out) {
				ok = false
				r.fail("deps " + name + ": exit 0 but " + out + "/ is missing")
			}
		} else {
			r.fail("deps " + name + " failed; dependencies may need installing by hand")
		}
		if ok {
			r.facts.Installed = append(r.facts.Installed, name)
		}
	}
	if spec.EnvTemplate != "" {
		if _, _, ok := r.step(ctx, envTemplateProvider, buildDepsCmd(envTemplateProvider), prepareEnvTimeout); ok && fileExists(r.wt, envFileName) {
			r.facts.EnvFile = envFileName + " created from " + spec.EnvTemplate
			if spec.Artisan {
				r.facts.EnvFile += " with a generated app key"
			}
		} else {
			r.fail("env template " + spec.EnvTemplate + " was not applied")
		}
	}
	r.baselineTest(ctx, spec.TestCandidates)
	if r.facts.TestCmd != "" && !strings.HasPrefix(r.facts.TestCmd, "mise run ") {
		local.TestCmd = r.facts.TestCmd
		if err := r.writeMiseLocal(local, spec.RepoKeys); err != nil {
			r.fail("rewriting " + miseLocalFile + " with the test task: " + err.Error())
		}
	}
	r.audit(ctx, spec.Lockfiles)
}

// baselineTest tries the ladder candidates in order until one exits 0;
// that one is the baseline and its summary is parsed.
func (r *prepareRun) baselineTest(ctx context.Context, candidates []testCandidate) {
	if len(candidates) == 0 {
		return
	}
	if len(candidates) > prepareTestTries {
		candidates = candidates[:prepareTestTries]
	}
	var tried []string
	for _, c := range candidates {
		_, out, ok := r.step(ctx, "test", buildTestCmd(c), prepareTestTimeout)
		tried = append(tried, "`"+c.Cmd+"`")
		if ok {
			s := parseTestSummary(out)
			r.facts.TestCmd, r.facts.TestSource, r.facts.Tests = c.Cmd, c.Source, &s
			return
		}
		if r.facts.CeilingHit {
			return
		}
	}
	r.fail("no baseline test command succeeded; tried " + strings.Join(tried, ", "))
}

// audit runs osv-scanner over the lockfiles into a report file under the
// workspace and reads the counts back. Exit 1 means advisories found,
// 128 means no packages; both are results, anything else a failure.
func (r *prepareRun) audit(ctx context.Context, lockfiles []string) {
	if len(lockfiles) == 0 {
		return
	}
	dir := filepath.Join(r.m.Workspace, prepareDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		r.fail("audit: creating " + dir + ": " + err.Error())
		return
	}
	outFile := filepath.Join(dir, "osv.json")
	_ = os.Remove(outFile)
	code, _, ok := r.step(ctx, "audit", buildAuditCmd(lockfiles, outFile), prepareAuditTimeout, osvExitAdvisories, osvExitNoPackages)
	switch {
	case !ok:
		r.fail("osv-scanner audit failed; vulnerability counts are unknown")
		return
	case code == osvExitNoPackages:
		return
	}
	raw, err := os.ReadFile(outFile) //nolint:gosec // harness-owned path under the mission workspace
	if err != nil {
		r.fail("audit: reading the osv-scanner report: " + err.Error())
		return
	}
	audit, err := parseOSVReport(raw, r.wt)
	if err != nil {
		r.fail("audit: parsing the osv-scanner report: " + err.Error())
		return
	}
	r.facts.Audit = audit
}

// step runs one command in the worktree through the sandbox, bounded by
// timeout and the remaining ceiling, and records a mission.prepare_step
// event plus a step fact. ok is exit 0 (or one of okCodes) with no exec
// error.
func (r *prepareRun) step(ctx context.Context, name, command string, timeout time.Duration, okCodes ...int) (code int, out string, ok bool) {
	remaining := time.Until(r.deadline)
	if remaining < prepareMinStep {
		if !r.facts.CeilingHit {
			r.facts.CeilingHit = true
			r.fail("prepare ceiling of " + prepareCeiling.String() + " reached before " + name)
		}
		r.p.appendPrepareEvent(ctx, r.m.ID, "mission.prepare_step", map[string]any{"name": name, "command": command, "skipped": "ceiling"})
		return -1, "", false
	}
	if timeout > remaining {
		timeout = remaining
	}
	var buf bytes.Buffer
	start := time.Now()
	code, err := r.p.sandboxExec(ctx, r.m.ID, r.m.Environment, r.wt, command, timeout, &buf)
	dur := time.Since(start)
	out = buf.String()
	ok = err == nil && (code == 0 || containsInt(okCodes, code))
	tail := tailString(strings.TrimSpace(out), prepareTailCap)
	payload := map[string]any{"name": name, "command": command, "exit_code": code, "duration_ms": dur.Milliseconds(), "tail": tail}
	if err != nil {
		payload["error"] = err.Error()
		tail = strings.TrimSpace(err.Error() + "\n" + tail)
	}
	r.p.appendPrepareEvent(ctx, r.m.ID, "mission.prepare_step", payload)
	fact := PrepareStepFact{Name: name, ExitCode: code, DurationMs: dur.Milliseconds(), OK: ok}
	if !ok {
		fact.Tail = tailString(tail, prepareFactTailCap)
	}
	r.facts.Steps = append(r.facts.Steps, fact)
	if !ok {
		r.p.log.Warn("driver: prepare step failed; mission continues", "mission_id", r.m.ID, "step", name, "exit_code", code, "error", err)
	}
	return code, out, ok
}

// fail records one failure line for the facts block.
func (r *prepareRun) fail(msg string) {
	r.facts.Failures = append(r.facts.Failures, msg)
}

// writeMiseLocal writes the harness config at the worktree root.
func (r *prepareRun) writeMiseLocal(in miseLocalInput, repoKeys map[string]bool) error {
	return os.WriteFile(filepath.Join(r.wt, miseLocalFile), []byte(renderMiseLocal(in, repoKeys)), 0o600)
}

// providerOutput is the directory a built-in provider must leave behind.
func providerOutput(name string) string {
	for _, p := range depsProviders {
		if p.name == name {
			return p.output
		}
	}
	return ""
}

// containsInt reports whether list holds n.
func containsInt(list []int, n int) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

// tailString keeps the last n bytes of s, marked with a leading ellipsis.
func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// renderPrepareFacts renders the prepare outcome for the facts block.
func renderPrepareFacts(f *PrepareFacts) string {
	if f == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("- Workspace prepared by the harness with mise before discover; " + miseLocalFile + " at the worktree root is harness-owned, never edit or commit it.\n")
	if len(f.Installed) > 0 {
		fmt.Fprintf(&b, "- Dependencies installed (mise deps): %s. Do not reinstall them; a check_cmd must never run an install.\n", strings.Join(f.Installed, ", "))
	}
	if f.EnvFile != "" {
		fmt.Fprintf(&b, "- Env file: %s by the harness.\n", NeutralizeSlot(f.EnvFile))
	}
	if f.TestCmd != "" {
		fmt.Fprintf(&b, "- Baseline tests: `%s` (from %s) exits 0", NeutralizeSlot(f.TestCmd), NeutralizeSlot(f.TestSource))
		if f.Tests != nil && f.Tests.Parsed {
			fmt.Fprintf(&b, ": %d passed, %d failed, %d warnings", f.Tests.Passed, f.Tests.Failed, f.Tests.Warnings)
		}
		b.WriteString(". Run it as `mise run test`; it is the regression baseline.\n")
	}
	if len(f.Audit) > 0 {
		var parts []string
		for _, a := range f.Audit {
			parts = append(parts, fmt.Sprintf("%s %d packages, %d known advisories", NeutralizeSlot(a.Path), a.Packages, a.Vulnerabilities))
		}
		fmt.Fprintf(&b, "- Vulnerability audit (osv-scanner): %s. Re-run with `mise run audit` after a lockfile changes.\n", strings.Join(parts, "; "))
	}
	for _, msg := range f.Failures {
		fmt.Fprintf(&b, "- Prepare failure: %s\n", NeutralizeSlot(msg))
	}
	for _, s := range f.Steps {
		if !s.OK && s.Tail != "" {
			fmt.Fprintf(&b, "  - %s (exit %d, %ds): %s\n", NeutralizeSlot(s.Name), s.ExitCode, s.DurationMs/1000, NeutralizeSlot(strings.ReplaceAll(s.Tail, "\n", " ")))
		}
	}
	return b.String()
}
