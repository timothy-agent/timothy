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
    kind                  text NOT NULL CHECK (kind IN ('coding', 'general')),
    agent_id              uuid REFERENCES agents(id),
    phase                 text NOT NULL DEFAULT 'explore',
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
    budget_amount         numeric(12,2),
    budget_currency       char(3) NOT NULL DEFAULT 'USD',
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
    prompt_overlay        text NOT NULL DEFAULT '',
    -- Mission worker turns run through loop.Agent same as chat, but
    -- tool-call bookkeeping (session_events, tools audit) hard-requires
    -- a real session_id uuid FK -- a mission has no chat session of its
    -- own. Give every mission a hidden session row purely so that
    -- bookkeeping has something real to attach to; nothing about it is
    -- chat-facing (no title shown, sessions list can filter it out by
    -- join).
    session_id            uuid REFERENCES sessions(id),
    -- pending_permission already holds the broker-issued id a mission
    -- is parked on; these columns carry what the UI needs to render a
    -- real decision prompt (tool name, args, danger level, rationale)
    -- instead of a bare "waiting" banner -- mirrors chat's
    -- PermissionRequestEvent.
    pending_permission_tool      text NOT NULL DEFAULT '',
    pending_permission_args      text NOT NULL DEFAULT '',
    pending_permission_danger    text NOT NULL DEFAULT '',
    pending_permission_rationale text NOT NULL DEFAULT '',
    -- Missions run for hours unattended; per-command-shape approval
    -- (built for a human watching a chat session) would otherwise park
    -- a mission on every novel-but-harmless shell call. Default true:
    -- new missions auto-approve DangerSafe shell calls via a standing
    -- session grant set at creation (Driver.Create) -- destructive-
    -- classified commands still always ask, unaffected by this column
    -- or any grant.
    auto_approve_safe     boolean NOT NULL DEFAULT true,
    -- The reviewer judges the baseline git diff, but a general
    -- mission never touches tracked files -- its diff is
    -- always empty, so the reviewer previously had zero evidence to
    -- judge and rejected every round. This carries the worker's own
    -- mission_status evidence text forward from execute to review,
    -- alongside (not instead of) the diff for coding missions.
    last_evidence         text NOT NULL DEFAULT '',
    -- The explore phase's findings, carried into the plan phase's
    -- prompt (internal/brain/missions/driver.go's runPlan) so the
    -- planner sees what exploration turned up, not just the bare goal.
    explore_notes         text NOT NULL DEFAULT '',
    -- A mission gets exactly one automatic replan attempt on stall
    -- (statemachine.go's stepWorkerRetry/stepReviewRework) before a
    -- second identical stall pauses for a human, same as always.
    replan_used           boolean NOT NULL DEFAULT false
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

-- Schedules fire mission templates on a cron cadence
-- (internal/brain/missions/scheduler.go). mission_template is applied
-- verbatim as the new mission's initial columns each firing.
CREATE TABLE IF NOT EXISTS schedules (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name              text UNIQUE NOT NULL,
    cron              text NOT NULL,
    mission_template  jsonb NOT NULL DEFAULT '{}',
    enabled           boolean NOT NULL DEFAULT true,
    expires_at        timestamptz,
    last_run          timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- Deferred FK: a mission may name the schedule that spawned it. NOT
-- VALID skips a table scan on possibly-large existing data at
-- migration time; VALIDATE CONSTRAINT can run later out-of-band if
-- ever needed. New rows are checked immediately either way. Guarded
-- for idempotency: ADD CONSTRAINT has no IF NOT EXISTS form, so check
-- pg_constraint directly. NOT VALID must be preserved so fresh
-- installs match upgraded databases in pg_catalog.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'missions_schedule_id_fkey'
    ) THEN
        ALTER TABLE missions ADD CONSTRAINT missions_schedule_id_fkey
            FOREIGN KEY (schedule_id) REFERENCES schedules(id) NOT VALID;
    END IF;
END $$;

-- Durable notification inbox (internal/brain/missions/notify.go):
-- always written for actionable transitions regardless of whether the
-- best-effort webhook fan-out succeeds.
CREATE TABLE IF NOT EXISTS notifications (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mission_id  uuid NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    kind        text NOT NULL,
    message     text NOT NULL DEFAULT '',
    read        boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS notifications_unread_idx ON notifications (mission_id) WHERE NOT read;
