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

## Mission phase rename (issue #455, slice 1 of the phase redesign)

Not required before deploy: parsePhase (statemachine.go) tolerates
both the old and new phase names, so a new binary reads old rows
correctly with no downtime. Safe to run any time AFTER the new binary
is live, on every instance. Historical mission_events payloads keep
their old phase names forever; the web timeline renderer already
tolerates both.

```sql
UPDATE missions SET phase = CASE phase
    WHEN 'explore' THEN 'discover'
    WHEN 'execute' THEN 'generate'
    WHEN 'review' THEN 'prove'
    ELSE phase
END
WHERE phase IN ('explore', 'execute', 'review');
```

## Plan approval gate (issue #456, slice 2 of the phase redesign)

Required on live DBs before/with the next deploy. Additive, safe to
run before deploy.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS auto_approve_plan boolean NOT NULL DEFAULT true;
```

## ask_user tool and waiting-input park (issue #457, slice 3 of the phase redesign)

Required on live DBs before/with the next deploy. Additive, safe to
run before deploy.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS pending_input jsonb;
ALTER TABLE missions ADD COLUMN IF NOT EXISTS asks_used integer NOT NULL DEFAULT 0;
```
