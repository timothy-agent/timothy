-- Agents are configuration, not code (D-030, D-034): a chat session
-- starts by choosing WHO serves it. An agent names a prompt overlay,
-- a route (its model chain), skill and tool allowlists (empty = all),
-- and whether long-term memory participates. Exactly one agent is the
-- default — the zero-click choice a new session gets.
CREATE TABLE IF NOT EXISTS agents (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text UNIQUE NOT NULL,
    description    text NOT NULL DEFAULT '',
    prompt_overlay text NOT NULL DEFAULT '',
    route          text NOT NULL DEFAULT '',
    skills         jsonb NOT NULL DEFAULT '[]',
    tools          jsonb NOT NULL DEFAULT '[]',
    memory         boolean NOT NULL DEFAULT true,
    is_default     boolean NOT NULL DEFAULT false,
    enabled        boolean NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS agents_one_default
    ON agents ((true)) WHERE is_default;

-- Sessions record which agent serves them; message events carry the
-- per-turn agent so mid-session switches attribute correctly.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS agent text NOT NULL DEFAULT '';

-- Seed only the agents the code depends on: 'general' because exactly
-- one default agent must exist, 'researcher' because the /research
-- page locks sessions to that name. Empty skills/tools = everything
-- allowed; route '' on general = the default route.
INSERT INTO agents (name, description, prompt_overlay, route, is_default)
VALUES
  ('general', 'Everyday questions and tasks on a strong all-round chain.', '', '', true),
  ('researcher',
   'Consults tools and sources before answering, never from memory alone.',
   'You are in research mode: consult tools and cite what you find before answering. Never answer purely from memory when a tool could verify.',
   'research', false)
ON CONFLICT (name) DO NOTHING;
