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
