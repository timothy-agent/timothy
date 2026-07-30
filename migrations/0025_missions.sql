-- Missions are long-running, agent-driven units of work distinct from
-- chat sessions: a mission survives across many model turns, tracks a
-- plan and progress log, and drives itself through a fixed phase
-- pipeline under a state machine (internal/brain/missions).
--
-- phase and status are deliberately NOT CHECK-constrained. A future
-- code rollback that doesn't recognize a newer phase/status value must
-- degrade the row to a safe paused/infra state in Go (parsePhase/
-- parseStatus), not have Postgres reject it outright — corruption-
-- safety over strictness at the schema layer.
CREATE TABLE IF NOT EXISTS missions (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal                  text NOT NULL,
    kind                  text NOT NULL CHECK (kind IN ('coding', 'research', 'scheduled')),
    agent_id              uuid REFERENCES agents(id),
    phase                 text NOT NULL DEFAULT 'research',
    status                text NOT NULL DEFAULT 'idle',
    pause_reason          text NOT NULL DEFAULT '',
    pause_message         text NOT NULL DEFAULT '',
    workspace             text NOT NULL DEFAULT '',
    worktree              text NOT NULL DEFAULT '',
    branch                text NOT NULL DEFAULT '',
    base_commit           text NOT NULL DEFAULT '',
    spec                  jsonb NOT NULL DEFAULT '{}',
    progress              jsonb NOT NULL DEFAULT '[]',
    iteration             integer NOT NULL DEFAULT 0,
    max_iterations        integer NOT NULL DEFAULT 8,
    consecutive_failures  integer NOT NULL DEFAULT 0,
    last_gap_fingerprint  text NOT NULL DEFAULT '',
    stall_count           integer NOT NULL DEFAULT 0,
    budget_usd            numeric(12,2),
    route                 text NOT NULL DEFAULT '',
    review_route          text NOT NULL DEFAULT '',
    pending_permission    text,
    schedule_id           uuid,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    -- Opt-in escalation ladder: when set, worker turns switch to this
    -- route after a worker failure or review rework instead of burning
    -- more iterations on a model that already proved too weak for the
    -- unit. Empty (the default) disables escalation entirely -- route
    -- changes must never be a surprise cost jump.
    escalation_route      text NOT NULL DEFAULT '',
    -- PromptOverlay snapshots the creating agent's prompt_overlay at
    -- create time (like route/review_route already do) — a mission
    -- outlives the request that made it, so it can't re-resolve a live
    -- agent lookup later without risking a surprise prompt change
    -- mid-mission if the agent row is edited while the mission runs.
    prompt_overlay        text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS missions_status_idx ON missions (status);
-- The work-slot cap (ClaimWorkSlot) and the boot sweep both need
-- "which missions are actively occupying a slot," cheaply.
CREATE INDEX IF NOT EXISTS missions_active_idx ON missions (phase) WHERE phase NOT IN ('done', 'failed');

-- Append-only event log, the mission's audit trail and the Timeline
-- UI's data source. seq is assigned under a SELECT ... FOR UPDATE on
-- the parent mission row (serializes appends per-mission only, not
-- globally) — see internal/brain/missions/store.go AppendEvent.
CREATE TABLE IF NOT EXISTS mission_events (
    mission_id  uuid NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    kind        text NOT NULL,
    payload     jsonb NOT NULL DEFAULT '{}',
    -- provenance distinguishes real driver output from test/replay
    -- fixtures written into the same table for scenario tests.
    provenance  text NOT NULL DEFAULT 'live' CHECK (provenance IN ('live', 'test', 'replay')),
    fingerprint text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mission_id, seq)
);
