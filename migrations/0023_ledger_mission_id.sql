-- Rename cost_ledger.lane_id -> mission_id: the column exists to tag a
-- ledger row with the long-running unit of work that produced it, and
-- "lane" never shipped a lane concept — missions (internal/brain/missions)
-- are that concept. RENAME preserves historical values instead of a
-- drop+add. Guarded so a database that already has mission_id (re-run,
-- or created fresh from a future consolidated schema) is a no-op.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'cost_ledger' AND column_name = 'lane_id'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'cost_ledger' AND column_name = 'mission_id'
    ) THEN
        ALTER TABLE cost_ledger RENAME COLUMN lane_id TO mission_id;
    END IF;
END $$;

-- Defensive heal, same rationale as 0019: a database that never had
-- lane_id at all (init'd from a pre-0019 draft) still ends up with
-- mission_id.
ALTER TABLE cost_ledger ADD COLUMN IF NOT EXISTS mission_id text;
