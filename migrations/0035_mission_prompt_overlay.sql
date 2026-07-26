-- PromptOverlay snapshots the creating agent's prompt_overlay at
-- create time (like route/review_route already do) — a mission
-- outlives the request that made it, so it can't re-resolve a live
-- agent lookup later without risking a surprise prompt change
-- mid-mission if the agent row is edited while the mission runs.
ALTER TABLE missions ADD COLUMN IF NOT EXISTS prompt_overlay text NOT NULL DEFAULT '';
