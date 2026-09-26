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
