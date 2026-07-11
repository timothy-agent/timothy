-- Retrieval bookkeeping. last_retrieved_at is lifecycle metadata for
-- the consolidation job (archive episodic memories unretrieved for
-- 180d) — it is NOT last_confirmed_at, which only explicit
-- confirmation or re-extraction may bump (D-011).

ALTER TABLE memories ADD COLUMN IF NOT EXISTS last_retrieved_at timestamptz;
