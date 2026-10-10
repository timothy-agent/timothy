# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

## missions and notifications keyset paging indexes (#1112)

```sql
CREATE INDEX IF NOT EXISTS missions_created_idx ON missions (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS notifications_created_idx ON notifications (created_at DESC, id DESC);
```

Backs the paged `GET /v1/missions` and `GET /v1/notifications` order.
Matches `0001_init.sql`. Additive: safe to run before deploy.
Instances: homelab `timothy`, `demo1`, `demo2` and `demo3` on
timothy-oci.

## keyset paging indexes for run histories and the sessions list (#1114)

```sql
CREATE INDEX IF NOT EXISTS automation_runs_automation_created_id_idx ON automation_runs (automation_id, created_at DESC, id DESC);
DROP INDEX IF EXISTS automation_runs_automation_created_idx;
CREATE INDEX IF NOT EXISTS workflow_runs_workflow_created_idx ON workflow_runs (workflow_id, created_at DESC, id DESC);
DROP INDEX IF EXISTS workflow_runs_workflow_idx;
CREATE INDEX IF NOT EXISTS sessions_updated_id_idx ON sessions (updated_at DESC, id DESC);
DROP INDEX IF EXISTS sessions_updated_idx;
CREATE INDEX IF NOT EXISTS missions_session_idx ON missions (session_id) WHERE session_id IS NOT NULL;
```

Matches `0001_init.sql`. Each new index covers what the index it
replaces served, so the drops are safe once the creates land. Additive:
safe to run before deploy. Instances: homelab `timothy`, `demo1`,
`demo2` and `demo3` on timothy-oci.
