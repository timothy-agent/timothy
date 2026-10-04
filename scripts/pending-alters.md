# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

## missions.environment_marker (#983, merged 2026-10-03)

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS environment_marker text NOT NULL DEFAULT '';
```

Records what set `missions.environment`: `''` for an operator-explicit
value, the repo marker file, or `discover`. Edited in place in
`0001_init.sql`, so existing databases need this ALTER before the first
release that includes #983 starts.
