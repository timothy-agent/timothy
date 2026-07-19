-- Heal schema drift: databases initialized from a pre-merge draft of
-- 0002 have cost_ledger without lane_id (CREATE TABLE IF NOT EXISTS
-- skipped the final shape), which makes every ledger INSERT fail
-- silently — spend tracking and budgets record nothing. No-op for
-- databases created from the merged 0002.
ALTER TABLE cost_ledger ADD COLUMN IF NOT EXISTS lane_id text;
