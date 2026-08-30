-- Idempotent delta for issue #423 (schema consolidation): converts a
-- live pre-consolidation database to the schema in migrations/0001_init.sql.
-- Safe to run more than once. Run with: psql "$DATABASE_URL" -f
-- scripts/schema-consolidation-delta.sql

BEGIN;

DROP TABLE IF EXISTS relations;

ALTER TABLE kb_documents DROP COLUMN IF EXISTS content_hash;
ALTER TABLE kb_chunks DROP COLUMN IF EXISTS metadata;
ALTER TABLE sessions DROP COLUMN IF EXISTS agent;
ALTER TABLE tool_outputs DROP COLUMN IF EXISTS bytes;

-- workflow_run_events: drop the bigserial surrogate PK and its
-- sequence, replace with PRIMARY KEY (run_id, seq) to match
-- mission_events/session_events. The old table also carried a
-- separate UNIQUE (run_id, seq) alongside the id PK; that constraint
-- becomes redundant once (run_id, seq) is the PK itself, so it is
-- dropped too. Guarded: only runs when the old shape (an id column)
-- is still present.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'workflow_run_events' AND column_name = 'id'
    ) THEN
        ALTER TABLE workflow_run_events DROP CONSTRAINT IF EXISTS workflow_run_events_pkey;
        ALTER TABLE workflow_run_events DROP CONSTRAINT IF EXISTS workflow_run_events_run_id_seq_key;
        ALTER TABLE workflow_run_events DROP COLUMN id;
        ALTER TABLE workflow_run_events ADD PRIMARY KEY (run_id, seq);
        DROP SEQUENCE IF EXISTS workflow_run_events_id_seq;
    END IF;
END $$;

-- missions: drop the four pending_permission_* text columns and
-- convert pending_permission from text to jsonb. The five columns are
-- live-only transient state (a mission's in-flight permission park),
-- so converting drops any in-flight park rather than migrating it.
-- Guarded: only alters when the column is still text.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'missions' AND column_name = 'pending_permission' AND data_type = 'text'
    ) THEN
        ALTER TABLE missions ALTER COLUMN pending_permission TYPE jsonb USING NULL;
    END IF;
END $$;

ALTER TABLE missions DROP COLUMN IF EXISTS pending_permission_tool;
ALTER TABLE missions DROP COLUMN IF EXISTS pending_permission_args;
ALTER TABLE missions DROP COLUMN IF EXISTS pending_permission_danger;
ALTER TABLE missions DROP COLUMN IF EXISTS pending_permission_rationale;

CREATE INDEX IF NOT EXISTS cost_ledger_mission_idx ON cost_ledger (mission_id) WHERE mission_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS notifications_mission_idx ON notifications (mission_id);

-- Validate the two FKs that were created NOT VALID pre-consolidation
-- (fresh installs get them validated inline in 0001_init.sql).
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'missions_schedule_id_fkey' AND NOT convalidated
    ) THEN
        ALTER TABLE missions VALIDATE CONSTRAINT missions_schedule_id_fkey;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'missions_workflow_run_id_fkey' AND NOT convalidated
    ) THEN
        ALTER TABLE missions VALIDATE CONSTRAINT missions_workflow_run_id_fkey;
    END IF;
END $$;

-- Collapse migration history: fresh installs and upgraded databases
-- both end up recording only '0001_init.sql', so migrate.go's
-- filename-based skip logic treats an upgraded database exactly like
-- one that already applied the consolidated file.
DELETE FROM schema_migrations WHERE version <> '0001_init.sql';
INSERT INTO schema_migrations (version) VALUES ('0001_init.sql')
ON CONFLICT (version) DO NOTHING;

COMMIT;
