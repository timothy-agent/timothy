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
