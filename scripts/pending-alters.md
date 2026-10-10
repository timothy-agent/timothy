# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

## memories decay stamp (#874)

```sql
ALTER TABLE memories ADD COLUMN IF NOT EXISTS decayed_at timestamptz;
```

Paces semantic decay to one step per row per window and gates the
retrieval floor (D-146, D-147). Matches `0001_init.sql`. Nullable with
no default: existing rows read as never decayed, which is what the old
code assumed. Additive: safe to run before deploy. Instances: homelab
`timothy`, `demo1`, `demo2` and `demo3` on timothy-oci.

## memories decay index (#880)

```sql
CREATE INDEX IF NOT EXISTS memories_status_confirmed_idx ON memories (status, last_confirmed_at);
```

Serves the semantic decay pass's `last_confirmed_at` cutoff. Matches
`0001_init.sql`. Additive: safe to run before deploy. Instances:
homelab `timothy`, `demo1`, `demo2` and `demo3` on timothy-oci.

## outbound mail ledger (#1155)

```sql
CREATE TABLE IF NOT EXISTS mail_sends (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    connector       text NOT NULL,
    outcome         text NOT NULL CHECK (outcome IN ('admitted', 'rejected')),
    reason          text NOT NULL DEFAULT '' CHECK (reason IN ('', 'recipients', 'daily')),
    recipient_count integer NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mail_sends_connector_created_idx ON mail_sends (connector, created_at);
```

Backs the outbound mail ceilings (D-151): admitted rows in the last 24
hours are an account's daily send count, rejected rows are the
operator's record. Matches `0001_init.sql`. Must run before deploy:
without the table every connector mail send fails closed. Instances:
homelab `timothy`, `demo1`, `demo2` and `demo3` on timothy-oci.
