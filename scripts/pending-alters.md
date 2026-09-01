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

## Schema restructure slice 2 (issue #480)

Required on live DBs before/with the next deploy. destinations (the
new jsonb column) is added additive first; the data migration below
folds destination_ids/promote_kb_collection_id/on_complete/
branch_pattern/commit_style into it (guarded so a re-run is a no-op:
it only touches rows still at destinations = '[]' and only runs while
the old columns still exist); the five old columns are then dropped.
Run all three steps together: the data migration reads columns the
last step removes.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS destinations jsonb NOT NULL DEFAULT '[]';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'missions' AND column_name = 'destination_ids'
    ) THEN
        UPDATE missions m SET destinations = (
            COALESCE((
                SELECT jsonb_agg(jsonb_build_object('destination', d.kind, 'destination_id', d.id::text))
                FROM unnest(m.destination_ids) AS did(id)
                JOIN destinations d ON d.id = did.id
            ), '[]'::jsonb)
            || CASE WHEN m.promote_kb_collection_id IS NOT NULL
                THEN jsonb_build_array(jsonb_build_object('destination', 'kb', 'collection_id', m.promote_kb_collection_id::text))
                ELSE '[]'::jsonb END
            || CASE WHEN m.on_complete <> '' OR m.branch_pattern <> '' OR m.commit_style <> ''
                THEN jsonb_build_array(jsonb_strip_nulls(jsonb_build_object(
                    'destination', 'github', 'mode', NULLIF(m.on_complete, ''),
                    'branch_pattern', NULLIF(m.branch_pattern, ''), 'commit_style', NULLIF(m.commit_style, '')
                )))
                ELSE '[]'::jsonb END
        )
        WHERE m.destinations = '[]'::jsonb
          AND (array_length(m.destination_ids, 1) > 0 OR m.promote_kb_collection_id IS NOT NULL
               OR m.on_complete <> '' OR m.branch_pattern <> '' OR m.commit_style <> '');
    END IF;
END $$;

ALTER TABLE missions DROP COLUMN IF EXISTS destination_ids;
ALTER TABLE missions DROP COLUMN IF EXISTS on_complete;
ALTER TABLE missions DROP COLUMN IF EXISTS branch_pattern;
ALTER TABLE missions DROP COLUMN IF EXISTS commit_style;
ALTER TABLE missions DROP COLUMN IF EXISTS promote_kb_collection_id;
```

## Schema restructure slice 3 (issue #481)

Required on live DBs before/with the next deploy. sources (the new
jsonb column) is added additive first; the data migration below folds
repo_url/connector_id/attachments/parent_context/referenced_context
into it (guarded so a re-run is a no-op: it only touches rows still at
sources = '[]' and only runs while the old columns still exist); the
five old columns are then dropped. Run all four steps together: the
data migration reads columns the last step removes.

referenced_context was a single concatenated string (issue #481
switched #-mention picks to one Sources entry per pick); a legacy row
folds its whole referenced_context blob into one "kb" entry rather
than trying to re-split it back into individual picks -- the harness
only ever reads the rendered concatenation (Mission.ReferencedContext),
never an individual entry's origin, so this preserves prompt content
exactly while dropping per-pick provenance for pre-migration rows only.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS sources jsonb NOT NULL DEFAULT '[]';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'missions' AND column_name = 'repo_url'
    ) THEN
        UPDATE missions m SET sources = (
            CASE WHEN m.repo_url <> ''
                THEN jsonb_build_array(jsonb_strip_nulls(jsonb_build_object(
                    'source', 'github', 'repo_url', m.repo_url, 'connector_id', NULLIF(m.connector_id, '')
                )))
                ELSE '[]'::jsonb END
            || CASE WHEN m.parent_context <> ''
                THEN jsonb_build_array(jsonb_build_object(
                    'source', 'mission', 'id', 'parent', 'mission_id', m.parent_mission_id::text, 'digest', m.parent_context
                ))
                ELSE '[]'::jsonb END
            || CASE WHEN m.referenced_context <> ''
                THEN jsonb_build_array(jsonb_build_object('source', 'kb', 'digest', m.referenced_context))
                ELSE '[]'::jsonb END
            || COALESCE((
                SELECT jsonb_agg(jsonb_build_object(
                    'source', 'pdf', 'id', a->>'id', 'mime', a->>'mime', 'name', a->>'name', 'markdown', a->>'markdown'
                ))
                FROM jsonb_array_elements(m.attachments) AS a
            ), '[]'::jsonb)
        )
        WHERE m.sources = '[]'::jsonb
          AND (m.repo_url <> '' OR m.parent_context <> '' OR m.referenced_context <> ''
               OR jsonb_array_length(m.attachments) > 0);
    END IF;
END $$;

ALTER TABLE missions DROP COLUMN IF EXISTS repo_url;
ALTER TABLE missions DROP COLUMN IF EXISTS connector_id;
ALTER TABLE missions DROP COLUMN IF EXISTS parent_context;
ALTER TABLE missions DROP COLUMN IF EXISTS referenced_context;
ALTER TABLE missions DROP COLUMN IF EXISTS attachments;
```

## Schema restructure slice 4 (issue #482)

Required on live DBs before/with the next deploy. knowledge is a plain
drop: search_kb has been whole-KB for every mission since D-078 (issue
#368), the column only ever reordered results and was never a gate, so
there is no data to preserve. spec/auto_approve_safe are renamed, no
data migration needed. The two RENAMEs have no IF EXISTS, so each is
guarded with an information_schema check instead (same pattern as the
explore_notes -> discover_notes rename above). Column comments are
always safe to re-run.

```sql
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'missions' AND column_name = 'spec'
    ) THEN
        ALTER TABLE missions RENAME COLUMN spec TO plan;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'missions' AND column_name = 'auto_approve_safe'
    ) THEN
        ALTER TABLE missions RENAME COLUMN auto_approve_safe TO auto_approve_tools;
    END IF;
END $$;

ALTER TABLE missions DROP COLUMN IF EXISTS knowledge;

COMMENT ON COLUMN missions.goal IS 'The mission''s objective, given at create time, never model-mutable.';
COMMENT ON COLUMN missions.agent_id IS 'The creating agent, if any -- resolved to defaults (route, prompt_overlay, ...) at create time, never re-read live afterward.';
COMMENT ON COLUMN missions.pause_reason IS 'Why status=''paused'': "approval", "infra", etc (statemachine.go).';
COMMENT ON COLUMN missions.pause_message IS 'Human-readable detail alongside pause_reason, shown in the UI.';
COMMENT ON COLUMN missions.workspace IS 'Filesystem path of this mission''s workspace directory.';
COMMENT ON COLUMN missions.branch IS 'Git branch a coding mission''s worktree checks out.';
COMMENT ON COLUMN missions.base_commit IS 'Git commit the worktree was based on, the baseline diff''s start point.';
COMMENT ON COLUMN missions.plan IS 'The mission''s submitted plan (missions.Plan): an ordered list of units, each verified independently before the mission can advance past it.';
COMMENT ON COLUMN missions.progress IS 'Append-only log of ProgressNote entries, the durable record a fresh stateless worker turn reads instead of prior transcript.';
COMMENT ON COLUMN missions.iteration IS 'Worker turns spent so far, capped at max_iterations.';
COMMENT ON COLUMN missions.consecutive_failures IS 'Consecutive worker/review failures, the stall-detection input.';
COMMENT ON COLUMN missions.last_gap_fingerprint IS 'Fingerprint of the last stall''s cause, deduping repeat replans.';
COMMENT ON COLUMN missions.budget_amount IS 'Spend ceiling; NULL means unlimited.';
COMMENT ON COLUMN missions.budget_currency IS 'Currency budget_amount is denominated in.';
COMMENT ON COLUMN missions.route IS 'Model route worker/generate turns run on.';
COMMENT ON COLUMN missions.review_route IS 'Model route the prove phase''s reviewer runs on; '''' falls back through plan_route to route (runner.go''s reviewRoute).';
COMMENT ON COLUMN missions.schedule_id IS 'Schedule that fired this mission, if any.';
COMMENT ON COLUMN missions.created_at IS 'Row creation timestamp.';
COMMENT ON COLUMN missions.updated_at IS 'Row last-update timestamp.';
COMMENT ON COLUMN missions.auto_approve_tools IS 'Missions run for hours unattended; per-command-shape approval (built for a human watching a chat session) would otherwise park a mission on every novel-but-harmless shell call. Default true: new missions auto-approve DangerSafe shell calls via a standing session grant set at creation (Driver.Create) -- destructive-classified commands still always ask, unaffected by this column or any grant.';
COMMENT ON COLUMN missions.workflow_step IS 'Step name within that workflow run.';
```
