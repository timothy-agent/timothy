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
