# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

Issue #817: adds missions.origin_kind and missions.unattended, backfilling existing rows from schedule_id, workflow_run_id and parent_mission_id.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS origin_kind text;
ALTER TABLE missions ADD COLUMN IF NOT EXISTS unattended boolean NOT NULL DEFAULT false;
UPDATE missions SET origin_kind = CASE
    WHEN schedule_id IS NOT NULL THEN 'automation'
    WHEN workflow_run_id IS NOT NULL THEN 'workflow'
    WHEN parent_mission_id IS NOT NULL THEN 'followup'
    ELSE 'api' END,
  unattended = (schedule_id IS NOT NULL OR workflow_run_id IS NOT NULL)
  WHERE origin_kind IS NULL;
ALTER TABLE missions ALTER COLUMN origin_kind SET NOT NULL;
ALTER TABLE missions DROP CONSTRAINT IF EXISTS missions_origin_kind_check,
  ADD CONSTRAINT missions_origin_kind_check
  CHECK (origin_kind IN ('api', 'automation', 'workflow', 'chat', 'followup'));
```

Issue #818: events inbox.

```sql
CREATE TABLE IF NOT EXISTS events (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source        text NOT NULL,
    kind          text NOT NULL,
    dedup_key     text NOT NULL,
    payload       jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    processed_at  timestamptz,
    attempts      int NOT NULL DEFAULT 0,
    last_error    text,
    UNIQUE (source, dedup_key)
);
CREATE INDEX IF NOT EXISTS events_unprocessed_idx ON events (id) WHERE processed_at IS NULL;
```

Issue #820: pending_permissions.

```sql
CREATE TABLE IF NOT EXISTS pending_permissions (
    id          text PRIMARY KEY,
    session_id  uuid REFERENCES sessions(id) ON DELETE CASCADE,
    mission_id  uuid REFERENCES missions(id) ON DELETE CASCADE,
    tool        text NOT NULL,
    args        jsonb NOT NULL DEFAULT '{}',
    danger      text NOT NULL DEFAULT '',
    rationale   text NOT NULL DEFAULT '',
    origin_kind text NOT NULL DEFAULT 'chat',
    created_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    decision    text,
    carry_over  boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS pending_permissions_pending_idx
    ON pending_permissions (created_at) WHERE resolved_at IS NULL;
CREATE INDEX IF NOT EXISTS pending_permissions_carry_idx
    ON pending_permissions (session_id, mission_id, tool) WHERE carry_over;
```
