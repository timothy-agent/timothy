# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

## D-085 per-collection retrieval weight (issue #443)

Required on live DBs before/with the next deploy. Additive, safe to
run before deploy.

```sql
ALTER TABLE kb_collections ADD COLUMN IF NOT EXISTS retrieval_weight double precision NOT NULL DEFAULT 1.0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'kb_collections_retrieval_weight_check'
    ) THEN
        ALTER TABLE kb_collections ADD CONSTRAINT kb_collections_retrieval_weight_check
            CHECK (retrieval_weight > 0 AND retrieval_weight <= 2);
    END IF;
END $$;
```

## Parked permission timeout (issue #445)

Required on live DBs before/with the next deploy. Additive, safe to
run before deploy.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS permission_timeout_seconds integer;
```
