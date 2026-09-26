# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

Issue #846: missions.agent_id gets ON DELETE SET NULL, so deleting an
agent keeps past mission history instead of raising a raw FK error.
Additive, safe any time before deploy.

```sql
BEGIN;
ALTER TABLE missions DROP CONSTRAINT IF EXISTS missions_agent_id_fkey;
ALTER TABLE missions ADD CONSTRAINT missions_agent_id_fkey
    FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE SET NULL;
COMMIT;
```

Issue #931: index missions.workflow_run_id, which the workflow engine's
step-mission adopt lookup (`WorkflowChild`) and the boot recovery of
runs without an entry mission filter on. Additive, safe any time before
deploy.

```sql
CREATE INDEX IF NOT EXISTS missions_workflow_run_idx ON missions (workflow_run_id) WHERE workflow_run_id IS NOT NULL;
```
