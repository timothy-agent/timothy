package missions

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

const composerLockOld = `{
  "packages": [
    {"name": "laravel/framework", "version": "v10.1.0"},
    {"name": "guzzlehttp/guzzle", "version": "7.5.0"},
    {"name": "old/removed", "version": "1.0.0"}
  ],
  "packages-dev": [{"name": "phpunit/phpunit", "version": "10.0.0"}]
}`

const composerLockNew = `{
  "packages": [
    {"name": "laravel/framework", "version": "v10.2.0"},
    {"name": "guzzlehttp/guzzle", "version": "7.5.0"},
    {"name": "new/added", "version": "2.1.0"}
  ],
  "packages-dev": [{"name": "phpunit/phpunit", "version": "10.5.1"}]
}`

const npmLockOld = `{
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "app", "version": "1.0.0"},
    "node_modules/vite": {"version": "4.5.0"},
    "node_modules/axios": {"version": "1.6.0"},
    "node_modules/axios/node_modules/form-data": {"version": "4.0.0"}
  }
}`

const npmLockNew = `{
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "app", "version": "1.0.0"},
    "node_modules/vite": {"version": "5.4.1"},
    "node_modules/axios": {"version": "1.6.0"},
    "node_modules/axios/node_modules/form-data": {"version": "4.0.1"}
  }
}`

func TestSummarizeLockfile(t *testing.T) {
	tests := []struct {
		name          string
		file          string
		before, after string
		lines         int
		want          string
	}{
		{"composer.lock", "composer.lock", composerLockOld, composerLockNew, 12,
			"composer.lock: 4 packages changed\n" +
				"- laravel/framework: v10.1.0 -> v10.2.0\n" +
				"- new/added: added 2.1.0\n" +
				"- old/removed: removed 1.0.0\n" +
				"- phpunit/phpunit: 10.0.0 -> 10.5.1\n"},
		{"package-lock.json v3", "web/package-lock.json", npmLockOld, npmLockNew, 8,
			"web/package-lock.json: 2 packages changed\n" +
				"- axios/node_modules/form-data: 4.0.0 -> 4.0.1\n" +
				"- vite: 4.5.0 -> 5.4.1\n"},
		{"package-lock.json v1", "package-lock.json",
			`{"lockfileVersion":1,"dependencies":{"lodash":{"version":"4.17.20"}}}`,
			`{"lockfileVersion":1,"dependencies":{"lodash":{"version":"4.17.21"}}}`, 2,
			"package-lock.json: 1 packages changed\n- lodash: 4.17.20 -> 4.17.21\n"},
		{"new lockfile", "composer.lock", "", composerLockNew, 20,
			"composer.lock: 4 packages changed\n- guzzlehttp/guzzle: added 7.5.0\n- laravel/framework: added v10.2.0\n- new/added: added 2.1.0\n- phpunit/phpunit: added 10.5.1\n"},
		{"other lockfile falls back to a line count", "yarn.lock", "a", "b", 42, "yarn.lock: lockfile changed, 42 lines\n"},
		{"unparseable composer.lock falls back", "composer.lock", "{", composerLockNew, 7, "composer.lock: lockfile changed, 7 lines\n"},
		{"binary lockfile", "bun.lockb", "a", "b", -1, "bun.lockb: lockfile changed\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeLockfile(tt.file, []byte(tt.before), []byte(tt.after), tt.lines); got != tt.want {
				t.Fatalf("summarizeLockfile =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestSummarizeLockfileCapsPackages(t *testing.T) {
	var pk []string
	for i := range lockSummaryCap + 5 {
		pk = append(pk, fmt.Sprintf(`{"name":"p/%03d","version":"1.0.0"}`, i))
	}
	after := `{"packages":[` + strings.Join(pk, ",") + `]}`
	got := summarizeLockfile("composer.lock", nil, []byte(after), 100)
	if !strings.Contains(got, "- ... 5 more\n") || strings.Count(got, ": added ") != lockSummaryCap {
		t.Fatalf("summary not capped at %d:\n%s", lockSummaryCap, got)
	}
}

// TestLockfileSummariesFromGit reads the old side through git show and
// the new side from the worktree.
func TestLockfileSummariesFromGit(t *testing.T) {
	m := lockfileFixture(t,
		map[string]string{"composer.lock": composerLockOld, "yarn.lock": "a\n"},
		map[string]string{"composer.lock": composerLockNew, "yarn.lock": "a\nb\nc\n"},
		nil, 0)
	got := lockfileSummaries(context.Background(), m.WorktreePath(), m.BaseCommit, []string{"composer.lock", "yarn.lock"})
	for _, want := range []string{"composer.lock: 4 packages changed\n", "- laravel/framework: v10.1.0 -> v10.2.0\n", "yarn.lock: lockfile changed, 2 lines\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("summaries lack %q:\n%s", want, got)
		}
	}
}

// TestFullReviewPacketLockfiles: a full round's packet carries the
// package summary and the stored evidence; the diff keeps lockfiles out.
func TestFullReviewPacketLockfiles(t *testing.T) {
	m := lockfileFixture(t,
		map[string]string{"composer.lock": composerLockOld},
		map[string]string{"composer.lock": composerLockNew},
		nil, 0)
	m.EnvFacts.Lockfile = &LockfileEvidence{Lockfiles: []string{"composer.lock"}, Criteria: []LockfileCriterion{{Text: "tests", Status: lockfileMet}}}
	d := testDriver(newFakeStore(), nil)
	packet, _, err := d.fullReviewPacket(context.Background(), m, []int{0}, m.Plan.Units)
	if err != nil {
		t.Fatalf("fullReviewPacket: %v", err)
	}
	if !strings.Contains(packet.Lockfiles, "- laravel/framework: v10.1.0 -> v10.2.0\n") || !strings.Contains(packet.LockfileEvidence, "1. tests: met") {
		t.Fatalf("packet lockfiles = %q, evidence = %q", packet.Lockfiles, packet.LockfileEvidence)
	}
	if strings.Contains(packet.Diff, "laravel/framework") {
		t.Fatalf("diff carries raw lockfile hunks:\n%s", packet.Diff)
	}
}

// TestRenderReviewContentLockfiles: the packet carries package lines
// and the harness criteria in place of raw lockfile hunks.
func TestRenderReviewContentLockfiles(t *testing.T) {
	e := &LockfileEvidence{Lockfiles: []string{"composer.lock"}, TestCmd: "php artisan test", TestsBefore: &TestSummary{Passed: 36, Parsed: true},
		TestsAfter: &TestSummary{Passed: 36, Warnings: 36, Parsed: true}, VulnsBefore: intp(1), VulnsAfter: intp(0)}
	e.Criteria = lockfileCriteria(e, false)
	got := renderReviewContent(ReviewPacket{
		Goal: "g", Lockfiles: "composer.lock: 1 packages changed\n- laravel/framework: v10.1.0 -> v10.2.0\n",
		LockfileEvidence: renderLockfileEvidence(e),
	})
	for _, want := range []string{
		"Lockfile changes by package (raw lockfile hunks are left out of the diff):\ncomposer.lock: 1 packages changed\n- laravel/framework: v10.1.0 -> v10.2.0\n",
		"Harness lockfile evidence:\nLockfiles changed: composer.lock.",
		"1. The baseline test command still exits 0 with no fewer passing tests and no new failures or warnings: not_met (warnings rose from 0 to 36",
		"2. The osv-scanner advisory count is not higher than the baseline: met (before: 1; after: 0)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("review content lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(renderReviewContent(ReviewPacket{Goal: "g"}), "Lockfile changes") {
		t.Error("a packet without lockfiles renders the lockfile section")
	}
}
