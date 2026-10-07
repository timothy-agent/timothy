# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

## missions.env_facts (#1008)

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS env_facts jsonb;
```

Environment facts collected at provisioning (base branch, repo
destinations, manifests, probed tool versions, gaps) and rendered into
every mission phase prompt. Edited in place in `0001_init.sql`, so
existing databases (homelab and the demos) need this ALTER before the
first release that includes #1008 starts; mission reads fail without it.
