-- Distinguish WHY a call was made from WHICH route served it: distill,
-- auto-title, and compaction summaries all ride cheap routes, so the
-- route alone cannot separate internal calls from user chat.
ALTER TABLE cost_ledger ADD COLUMN IF NOT EXISTS purpose text;
