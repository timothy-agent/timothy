# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

Issue #821: automations replace schedules. Creates the automations
tables and `missions.automation_run_id`, migrates every schedule into an
automation with one cron trigger (same id), backfills one run per
schedule-started mission, then drops `missions.schedule_id` and
`schedules`. The new binary has no cron firing until #822 lands, so run
this at deploy time, one transaction, before the new binary boots.

Pre-check first. Every row returned aborts the block below at the
`::uuid` cast or the agents foreign key, so fix or delete those
schedules before running it. Empty result means go.

```sql
SELECT id, name, mission_template->>'agent_id' AS agent_id
FROM schedules
WHERE NULLIF(mission_template->>'agent_id', '') IS NOT NULL
  AND (mission_template->>'agent_id' !~ '^[0-9a-fA-F-]{36}$'
       OR NOT EXISTS (SELECT 1 FROM agents WHERE id::text = lower(mission_template->>'agent_id')));
```

```sql
BEGIN;

CREATE TABLE IF NOT EXISTS automations (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 text NOT NULL,
    description          text NOT NULL DEFAULT '',
    agent_id             uuid NOT NULL REFERENCES agents(id),
    action               jsonb NOT NULL,
    concurrency          text NOT NULL DEFAULT 'skip' CHECK (concurrency IN ('skip','queue','parallel')),
    max_concurrent       int NOT NULL DEFAULT 1 CHECK (max_concurrent BETWEEN 1 AND 3),
    max_runs_per_hour    int NOT NULL DEFAULT 6 CHECK (max_runs_per_hour BETWEEN 1 AND 60),
    continuity           boolean NOT NULL DEFAULT true,
    notes_enabled        boolean NOT NULL DEFAULT true,
    consecutive_failures int NOT NULL DEFAULT 0,
    disabled_reason      text,
    enabled              boolean NOT NULL DEFAULT true,
    expires_at           timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS automations_name_ci ON automations (lower(btrim(name)));

CREATE TABLE IF NOT EXISTS automation_triggers (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    automation_id  uuid NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN ('cron','manual','webhook','connector_event','channel')),
    config         jsonb NOT NULL DEFAULT '{}',
    credential_ref text,
    tool_allowlist jsonb,
    state          jsonb NOT NULL DEFAULT '{}',
    enabled        boolean NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS automation_triggers_automation_idx ON automation_triggers (automation_id);

CREATE TABLE IF NOT EXISTS automation_runs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    automation_id   uuid NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
    trigger_id      uuid REFERENCES automation_triggers(id) ON DELETE SET NULL,
    event_id        bigint REFERENCES events(id) ON DELETE SET NULL,
    dedup_key       text NOT NULL,
    status          text NOT NULL,
    skip_reason     text,
    event           jsonb NOT NULL DEFAULT '{}',
    mission_id      uuid,
    workflow_run_id uuid REFERENCES workflow_runs(id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz,
    UNIQUE (automation_id, dedup_key)
);
CREATE INDEX IF NOT EXISTS automation_runs_automation_created_idx ON automation_runs (automation_id, created_at DESC);

CREATE TABLE IF NOT EXISTS automation_notes (
    automation_id     uuid NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
    name              text NOT NULL CHECK (name ~ '^[a-z0-9_-]{1,64}$'),
    content           text NOT NULL CHECK (octet_length(content) <= 4096),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    updated_by_run_id uuid REFERENCES automation_runs(id) ON DELETE SET NULL,
    PRIMARY KEY (automation_id, name)
);

ALTER TABLE automation_runs DROP CONSTRAINT IF EXISTS automation_runs_mission_id_fkey;
ALTER TABLE automation_runs ADD CONSTRAINT automation_runs_mission_id_fkey
    FOREIGN KEY (mission_id) REFERENCES missions(id) ON DELETE SET NULL;
ALTER TABLE missions ADD COLUMN IF NOT EXISTS automation_run_id uuid REFERENCES automation_runs(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS missions_automation_run_idx ON missions (automation_run_id) WHERE automation_run_id IS NOT NULL;

INSERT INTO automations (id, name, agent_id, action, enabled, expires_at, created_at, updated_at)
SELECT s.id, s.name,
    COALESCE(NULLIF(s.mission_template->>'agent_id', '')::uuid, (SELECT id FROM agents WHERE is_default LIMIT 1)),
    jsonb_build_object('kind', 'mission', 'mission', s.mission_template - 'agent_id'),
    s.enabled, s.expires_at, s.created_at, s.updated_at
FROM schedules s
ON CONFLICT (id) DO NOTHING;

INSERT INTO automation_triggers (automation_id, kind, config, state, created_at, updated_at)
SELECT s.id, 'cron', jsonb_build_object('expr', s.cron),
    jsonb_strip_nulls(jsonb_build_object('last_fired_at', s.last_run, 'pending_fire', s.pending_fire,
        'last_skipped_at', s.last_skipped_at, 'skip_reason', NULLIF(s.skip_reason, ''))),
    s.created_at, s.updated_at
FROM schedules s
WHERE NOT EXISTS (SELECT 1 FROM automation_triggers t WHERE t.automation_id = s.id);

INSERT INTO automation_runs (automation_id, trigger_id, dedup_key, status, mission_id, created_at, started_at, finished_at)
SELECT m.schedule_id,
    (SELECT t.id FROM automation_triggers t WHERE t.automation_id = m.schedule_id AND t.kind = 'cron' ORDER BY t.created_at LIMIT 1),
    'mission:' || m.id,
    CASE m.phase WHEN 'done' THEN 'done' WHEN 'failed' THEN 'failed' ELSE 'running' END,
    m.id, m.created_at, m.created_at,
    CASE WHEN m.phase IN ('done', 'failed') THEN m.updated_at END
FROM missions m
WHERE m.schedule_id IS NOT NULL
ON CONFLICT (automation_id, dedup_key) DO NOTHING;

UPDATE missions SET automation_run_id = r.id
FROM automation_runs r
WHERE r.mission_id = missions.id AND missions.automation_run_id IS NULL;

ALTER TABLE missions DROP COLUMN IF EXISTS schedule_id;
DROP TABLE IF EXISTS schedules;

UPDATE settings SET key = 'automations_enabled'
WHERE key = 'scheduler_enabled'
  AND NOT EXISTS (SELECT 1 FROM settings WHERE key = 'automations_enabled');

SELECT (SELECT count(*) FROM automations) AS automations,
       (SELECT count(*) FROM automation_triggers) AS triggers,
       (SELECT count(*) FROM automation_runs) AS runs;
-- Expect 0.
SELECT count(*) AS unlinked_automation_missions
FROM missions WHERE automation_run_id IS NULL AND origin_kind = 'automation';

COMMIT;
```
