# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

```sql
ALTER TABLE cost_ledger ADD COLUMN IF NOT EXISTS tool_def_tokens_estimate integer;
```
