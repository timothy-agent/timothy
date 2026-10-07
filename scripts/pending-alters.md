# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

## memories.supersedes (#959)

```sql
ALTER TABLE memories ADD COLUMN IF NOT EXISTS supersedes uuid REFERENCES memories (id);
```

Points a pending correction at the active memory it would replace, so
the review queue can show both texts and confirming it supersedes the old
row. Edited in place in `0001_init.sql`, so existing databases need this
ALTER before the first release that includes #959 starts; memory reads
fail without it.

## missions.toolchains (#991)

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS toolchains jsonb NOT NULL DEFAULT '{}';
```

Toolchain versions detected from repo marker files (tool -> mise version
prefix), written with `environment` and installed in the sandbox before
discover. Edited in place in `0001_init.sql`, so existing databases need
this ALTER before the first release that includes #991 starts.

## missions.environment_marker (#983, merged 2026-10-03)

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS environment_marker text NOT NULL DEFAULT '';
```

Records what set `missions.environment`: `''` for an operator-explicit
value, the repo marker file, or `discover`. Edited in place in
`0001_init.sql`, so existing databases need this ALTER before the first
release that includes #983 starts.
