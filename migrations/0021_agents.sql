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
    -- Missions reuse the agents table (missions.agent_id -> agents.id)
    -- rather than a parallel agent_profiles table: a mission-serving
    -- agent is still "who serves this," same as chat. These three
    -- columns are meaningless to a chat-only agent and stay at their
    -- defaults for one; a mission-capable agent sets all three.
    review_route       text NOT NULL DEFAULT '',
    budget_usd         numeric(12,2),
    approval_allowlist jsonb NOT NULL DEFAULT '[]',
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
-- allowed; general routes 'research' rather than '' so the default
-- agent also consults tools/sources instead of answering from memory
-- alone.
INSERT INTO agents (name, description, prompt_overlay, route, is_default)
VALUES
  ('general', 'Everyday questions and tasks on a strong all-round chain.', '', 'research', true),
  ('researcher',
   'Consults tools and sources before answering, never from memory alone.',
   'You are in research mode: consult tools and cite what you find before answering. Never answer purely from memory when a tool could verify.',
   'research', false)
ON CONFLICT (name) DO NOTHING;

-- Briefing agent seed (roadmap item 3): the daily-briefing skill's
-- prompt overlay and approval allowlist, seeded once so recurring
-- "briefing" missions have an agent to run against. Goal-driven: the
-- topics to cover come from the mission goal text itself, resolved at
-- mission-run time — no separate topics table.
--
-- approval_allowlist names tools by their bare, connector-unprefixed
-- name ("calendar_list_events", not "google-calendar_calendar_list_
-- events") — matchGrant's suffix rule (D-036, internal/brain/tools/
-- permissions.go) matches these against the runtime-namespaced tool
-- name a connector actually registers, so this works regardless of
-- which connector name serves gmail/calendar for a given user.
INSERT INTO agents (name, description, prompt_overlay, skills, tools, approval_allowlist, memory, route)
VALUES (
    'briefing',
    'Goal-driven briefing: covers every topic named in the mission goal, adds calendar/email highlights when connected, writes briefing.md.',
    'Load the daily-briefing skill first and follow its rules for the rest of this mission. Cover every topic named in the mission goal, using web_search to check each one for anything from roughly the last 24 hours. When calendar and email tools are available, add short highlight sections for today''s events and unread mail; when they are not available, say so plainly instead of inventing content. Write the full briefing as briefing.md at the workspace root. Only send it by email via gmail_send if the goal explicitly names a recipient.',
    '["daily-briefing"]',
    '["web_search", "calendar_list_events", "gmail_search", "gmail_read", "gmail_send"]',
    '["gmail_search", "gmail_read", "calendar_list_events", "gmail_send"]',
    false,
    'research'
)
ON CONFLICT (name) DO NOTHING;
