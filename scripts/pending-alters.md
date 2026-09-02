# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

## Review findings ledger and rework counter (D-092, issue #512)

Required on live DBs before/with the next deploy. Additive, safe to
run before deploy.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS review_findings jsonb NOT NULL DEFAULT '[]';
ALTER TABLE missions ADD COLUMN IF NOT EXISTS rework_rounds integer NOT NULL DEFAULT 0;
```
