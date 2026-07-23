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

-- Deferred FK from 0025 (schedules didn't exist yet): a mission may
-- name the schedule that spawned it. NOT VALID skips a table scan on
-- possibly-large existing data at migration time; VALIDATE CONSTRAINT
-- can run later out-of-band if ever needed. New rows are checked
-- immediately either way. Guarded for idempotency: ADD CONSTRAINT has
-- no IF NOT EXISTS form, so check pg_constraint directly.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'missions_schedule_id_fkey'
    ) THEN
        ALTER TABLE missions ADD CONSTRAINT missions_schedule_id_fkey
            FOREIGN KEY (schedule_id) REFERENCES schedules(id) NOT VALID;
    END IF;
END $$;
