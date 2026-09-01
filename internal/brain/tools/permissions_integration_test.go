//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func integrationPermissions(t *testing.T) (*Permissions, string) {
	t.Helper()
	o := integrationOutputs(t) // reuses pool + migrations
	sid := newSessionID(t, o)
	return NewPermissions(o.db, "/workspace"), sid
}

func shellCall(command string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"command": command})
	return raw
}

// sweepAllowlist removes the fixture patterns this file seeds.
func sweepAllowlist(ctx context.Context, db execer) {
	_, _ = db.Exec(ctx,
		"DELETE FROM project_allowlist WHERE tool = 'shell' AND pattern IN ('git status*', 'rm*')")
}

// cleanupAllowlist sweeps on an independent connection: t.Context is
// canceled before cleanups run and the pool may already be closed.
func cleanupAllowlist(t *testing.T) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		sweepAllowlist(ctx, conn)
	}
}

func TestResolveNoGrantAsks(t *testing.T) {
	p, sid := integrationPermissions(t)

	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("ls -la"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("res = %+v, want ask", res)
	}
}

// TestResolvePushMissionBranchAsksWithoutGrant proves push_mission_branch
// (unlike list_missions/get_mission, which are exempt) always falls
// through to "no standing grant" and asks — it is deliberately absent
// from Permissions.exempt (see NewPermissions) so a chat session's
// model can never talk its way into an auto-approved push.
func TestResolvePushMissionBranchAsksWithoutGrant(t *testing.T) {
	p, sid := integrationPermissions(t)

	res, err := p.Resolve(t.Context(), sid, "push_mission_branch", json.RawMessage(`{"id":"m1"}`))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("res = %+v, want ask", res)
	}
}

func TestResolveSessionGrantAllows(t *testing.T) {
	p, sid := integrationPermissions(t)

	if err := p.Grant(t.Context(), sid, "shell", "ls*", time.Hour); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("ls -la src/"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Fatalf("res = %+v, want allow", res)
	}

	// The grant is scoped to its session.
	o := integrationOutputs(t)
	other := newSessionID(t, o)
	res, err = p.Resolve(t.Context(), other, "shell", shellCall("ls -la src/"))
	if err != nil {
		t.Fatalf("Resolve other session: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("other-session res = %+v, want ask", res)
	}
}

func TestResolveExpiredGrantAsks(t *testing.T) {
	p, sid := integrationPermissions(t)

	if err := p.Grant(t.Context(), sid, "shell", "wc*", time.Hour); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	db, err := p.db.Get()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if _, err := db.Exec(t.Context(),
		"UPDATE session_grants SET expires = now() - interval '1 minute' WHERE session_id = $1", sid,
	); err != nil {
		t.Fatalf("expire: %v", err)
	}

	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("wc -l notes.md"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("res = %+v, want ask after expiry", res)
	}
}

func TestResolveProjectAllowlistAllows(t *testing.T) {
	p, sid := integrationPermissions(t)

	db, err := p.db.Get()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	// Sweep at setup too — a run that dies before t.Cleanup registers
	// leaks its fixtures.
	sweepAllowlist(t.Context(), db)
	if _, err := db.Exec(t.Context(),
		"INSERT INTO project_allowlist (tool, pattern) VALUES ('shell', 'git status*') ON CONFLICT DO NOTHING",
	); err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}
	t.Cleanup(cleanupAllowlist(t))

	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("git status --short"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Fatalf("res = %+v, want allow", res)
	}

	// Destructive commands ignore the allowlist entirely.
	if _, err := db.Exec(t.Context(),
		"INSERT INTO project_allowlist (tool, pattern) VALUES ('shell', 'rm*') ON CONFLICT DO NOTHING",
	); err != nil {
		t.Fatalf("seed rm allowlist: %v", err)
	}
	res, err = p.Resolve(t.Context(), sid, "shell", shellCall("rm -rf build/"))
	if err != nil {
		t.Fatalf("Resolve rm: %v", err)
	}
	if res.Decision != DecisionAsk || res.Danger != DangerDestructive {
		t.Fatalf("res = %+v, want ask despite allowlist", res)
	}
}

// TestResolveSandboxOpaqueWithGrantAllows is D-050's core case: a
// session with a registered sandbox (a mission's per-mission Docker
// container) AND a standing "shell" grant (AutoApproveTools) runs an
// opaque command — interpreter -c inline code — without a human
// prompt. The reviewer-parks-on-python3--c friction this decision
// exists to remove. This is also the D-039 unattended-mission case
// (requirement 8): loop.Agent's resolveAndRun only consults
// Request.Unattended inside the DecisionAsk branch — a DecisionAllow
// result (this test) reaches execution unconditionally, schedule-fired
// or not, so a schedule-fired mission with this same grant/sandbox
// combination gains the identical capability with no further wiring.
func TestResolveSandboxOpaqueWithGrantAllows(t *testing.T) {
	p, sid := integrationPermissions(t)

	if err := p.Grant(t.Context(), sid, SandboxGrantTool, "/workspace/mission-1", time.Hour); err != nil {
		t.Fatalf("Grant sandbox: %v", err)
	}
	if err := p.Grant(t.Context(), sid, "shell", "*", time.Hour); err != nil {
		t.Fatalf("Grant shell: %v", err)
	}

	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("python3 -c 'print(1)'"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Fatalf("res = %+v, want allow (opaque-in-sandbox relaxes to safe, grant covers it)", res)
	}
	if res.Danger != DangerSafe {
		t.Fatalf("res.Danger = %v, want safe once reclassified", res.Danger)
	}
}

// TestResolveSandboxOpaqueWithoutGrantAsks is requirement 3: the
// relaxation reclassifies the command to safe, but safe still needs a
// standing grant to skip the prompt — a mission created with
// auto_approve_tools=false (no "shell" grant registered) still asks on
// an opaque command even though its session has a registered sandbox.
func TestResolveSandboxOpaqueWithoutGrantAsks(t *testing.T) {
	p, sid := integrationPermissions(t)

	if err := p.Grant(t.Context(), sid, SandboxGrantTool, "/workspace/mission-1", time.Hour); err != nil {
		t.Fatalf("Grant sandbox: %v", err)
	}

	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("python3 -c 'print(1)'"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("res = %+v, want ask — no standing grant despite the sandbox", res)
	}
}

// TestResolveSandboxExplicitDestructiveStillAsks is requirement 2: the
// relaxation is opaque-only. An explicit destructive pattern (rm -rf)
// is not opaque, so a registered sandbox plus a standing "shell" grant
// does NOT let it skip the prompt the way an opaque command now can —
// it still goes through sandboxAllows' narrower, path-scoped downgrade,
// and rm -rf's target here is not path-confined to the sandbox root.
func TestResolveSandboxExplicitDestructiveStillAsks(t *testing.T) {
	p, sid := integrationPermissions(t)

	if err := p.Grant(t.Context(), sid, SandboxGrantTool, "/workspace/mission-1", time.Hour); err != nil {
		t.Fatalf("Grant sandbox: %v", err)
	}
	if err := p.Grant(t.Context(), sid, "shell", "*", time.Hour); err != nil {
		t.Fatalf("Grant shell: %v", err)
	}

	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("git push origin main"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAsk || res.Danger != DangerDestructive {
		t.Fatalf("res = %+v, want ask+destructive — git-push is not opaque and not file-scoped", res)
	}
}

// TestResolveSandboxOpaqueGuardStillDenies is requirement 4: the
// relaxation only ever affects the ask/allow decision downstream of
// the policy guard, which runs first in Resolve regardless of any
// sandbox registration — an opaque command naming a guarded path is
// still a hard deny, sandbox or not.
func TestResolveSandboxOpaqueGuardStillDenies(t *testing.T) {
	p, sid := integrationPermissions(t)

	if err := p.Grant(t.Context(), sid, SandboxGrantTool, "/workspace/mission-1", time.Hour); err != nil {
		t.Fatalf("Grant sandbox: %v", err)
	}
	if err := p.Grant(t.Context(), sid, "shell", "*", time.Hour); err != nil {
		t.Fatalf("Grant shell: %v", err)
	}

	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("python3 -c \"open('/etc/passwd').read()\""))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionDeny || !strings.Contains(res.Rationale, "policy guard") {
		t.Fatalf("res = %+v, want hard deny (system dirs guard)", res)
	}
}
