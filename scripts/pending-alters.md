# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

Issue #826: GitHub event poller cursors. Additive, safe any time
before the new binary boots.

```sql
CREATE TABLE IF NOT EXISTS github_poll_cursors (
    connector_id  uuid NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    repo          text NOT NULL,
    etag          text NOT NULL DEFAULT '',
    last_event_id text NOT NULL DEFAULT '',
    runs_since    timestamptz NOT NULL DEFAULT now(),
    next_poll_at  timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (connector_id, repo)
);
```

Issue #857: automation triggers restrict a run's tools. Adds the
per-mission allowlist column (NULL = unrestricted). Run before the new
binary boots; it reads the column on every mission load.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS tool_allowlist text[];
```

Issue #827: operator notifications without a mission (webhook trigger
guard, automation breaker with no mission). Run before the new binary
boots; it inserts rows with a NULL mission_id.

```sql
ALTER TABLE notifications ALTER COLUMN mission_id DROP NOT NULL;
```
