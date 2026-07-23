-- Missions reuse the agents table (missions.agent_id -> agents.id)
-- rather than a parallel agent_profiles table: a mission-serving agent
-- is still "who serves this," same as chat. These three columns are
-- meaningless to a chat-only agent and stay at their defaults for one;
-- a mission-capable agent sets all three.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS review_route text NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS budget_usd numeric(12,2);
ALTER TABLE agents ADD COLUMN IF NOT EXISTS approval_allowlist jsonb NOT NULL DEFAULT '[]';
