# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

```sql
-- issue #582: opt-in delegated reviewer harness on a mission.
ALTER TABLE missions ADD COLUMN IF NOT EXISTS review_harness text NOT NULL DEFAULT '';
```
