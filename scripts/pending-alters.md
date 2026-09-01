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

## Mission flow selection (D-090, issue #459)

Required on live DBs before/with the next deploy. Additive, safe to
run before deploy.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS flow text NOT NULL DEFAULT 'full';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'missions_flow_check'
    ) THEN
        ALTER TABLE missions ADD CONSTRAINT missions_flow_check
            CHECK (flow IN ('full', 'discover_generate', 'no_prove', 'light'));
    END IF;
END $$;

UPDATE missions SET flow = 'light' WHERE light AND flow <> 'light';
```

## Phase rename leftovers (issue #472)

Not required before deploy: the new binary reads/writes discover_notes
only, so this is safe to run any time AFTER the new binary is live, on
every instance. The RENAME has no IF EXISTS, so it is guarded with an
information_schema check instead. approval_allowlist is a jsonb array
of tool names; any row still naming the pre-rename sentinel tool gets
it swapped for the current name.

```sql
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'missions' AND column_name = 'explore_notes'
    ) THEN
        ALTER TABLE missions RENAME COLUMN explore_notes TO discover_notes;
    END IF;
END $$;

UPDATE agents
SET approval_allowlist = (
    SELECT jsonb_agg(CASE WHEN elem = '"explore_notes"' THEN '"discover_notes"'::jsonb ELSE elem END)
    FROM jsonb_array_elements(approval_allowlist) AS elem
)
WHERE approval_allowlist @> '["explore_notes"]';
```

## Schema restructure slice 1 (issue #479)

Required on live DBs before/with the next deploy. Data is already
coherent before either drop: flow='light' iff light (normalized at
create since D-090), and worktree is always workspace + "/wt" for a
mission whose kind/flow policy needs one (Mission.WorktreePath derives
it going forward, never reads the old column again).

```sql
ALTER TABLE missions DROP COLUMN IF EXISTS light;
ALTER TABLE missions DROP COLUMN IF EXISTS worktree;
```
