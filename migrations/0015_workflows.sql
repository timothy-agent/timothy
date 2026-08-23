-- Agentic workflows: an orchestration layer above missions
-- (internal/brain/workflows). A workflow is composable data (steps +
-- edges); missions stay atoms, spawned one at a time by the engine
-- reacting to mission terminal events. See
-- docs/2026-08-14-agentic-workflows-plan.md.
CREATE TABLE IF NOT EXISTS workflows (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text UNIQUE NOT NULL,
    definition  jsonb NOT NULL DEFAULT '{}',
    enabled     boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- status is deliberately NOT CHECK-constrained, same reasoning as
-- missions.phase/status (0010_missions.sql): a future code rollback
-- that doesn't recognize a newer status must degrade safely in Go, not
-- have Postgres reject the row.
CREATE TABLE IF NOT EXISTS workflow_runs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id   uuid NOT NULL REFERENCES workflows(id),
    status        text NOT NULL DEFAULT 'running', -- running | paused | done | failed | cancelled
    current_step  text NOT NULL DEFAULT '',
    context       jsonb NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS workflow_runs_workflow_idx ON workflow_runs (workflow_id);

-- Append-only event log, same invariant as mission_events: seq is
-- assigned under a SELECT ... FOR UPDATE on the parent run row, never
-- updated or deleted.
CREATE TABLE IF NOT EXISTS workflow_run_events (
    id          bigserial PRIMARY KEY,
    run_id      uuid NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    kind        text NOT NULL,
    payload     jsonb NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, seq)
);

-- Deferred FK: a mission may name the workflow run that spawned it.
-- NOT VALID skips a table scan on possibly-large existing data at
-- migration time, same pattern as missions_schedule_id_fkey
-- (0010_missions.sql).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'missions_workflow_run_id_fkey'
    ) THEN
        ALTER TABLE missions ADD CONSTRAINT missions_workflow_run_id_fkey
            FOREIGN KEY (workflow_run_id) REFERENCES workflow_runs(id) NOT VALID;
    END IF;
END $$;
