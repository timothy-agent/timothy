-- Agents are configuration, not code (D-030, D-034): a chat session
-- starts by choosing WHO serves it. An agent names a prompt overlay,
-- a route (its model chain), skill and tool allowlists (empty = none:
-- opt-in only, keeps an agent's tool schemas off the wire until named),
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

-- Seed only the agents the code depends on: 'general' because exactly
-- one default agent must exist, 'researcher' because the /research
-- page locks sessions to that name. Skills/tools are minimal-by-
-- default: empty means none (opt-in only), so every allowlist below
-- names exactly the surface that agent needs — a shorter list is
-- fewer tool schemas on every turn's system prompt, cheaper in tokens
-- and less for the model to choose wrong among. general routes
-- 'research' rather than '' so the default agent also consults
-- tools/sources instead of answering from memory alone. Guarded both
-- ways for is_default: if any default agent already exists, this seed
-- must not attempt to set is_default=true.
--
-- Tool names are the compiled-in builtins' exact registered names
-- (internal/brain/tools/builtin/*.go) plus connector tools by their
-- bare, connector-unprefixed name ("gmail_search", not "gmail_
-- gmail_search") — matchGrant's/filterDefs' suffix rule (D-036,
-- internal/brain/tools/permissions.go) matches these against the
-- runtime-namespaced tool name a connector actually registers, so
-- this works regardless of which connector serves gmail/calendar.
-- gmail_send is deliberately left off every seed: sending mail is a
-- deliberate per-agent opt-in, not a default.
INSERT INTO agents (name, description, prompt_overlay, route, skills, tools, is_default)
SELECT 'general', 'Everyday questions and tasks on a strong all-round chain.', '', 'research',
    '["email-research"]',
    '["current_time", "convert_time", "calculate", "currency_convert", "web_search", "web_fetch", "remember", "missions", "mission_push", "gmail_search", "gmail_read", "calendar_list_events"]',
    NOT EXISTS (SELECT 1 FROM agents WHERE is_default)
WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'general');

INSERT INTO agents (name, description, prompt_overlay, route, skills, tools, is_default)
VALUES
  ('researcher',
   'Consults tools and sources before answering, never from memory alone.',
   'You are in research mode: consult tools and cite what you find before answering. Never answer purely from memory when a tool could verify.',
   'research',
   '["research-brief", "deep-research"]',
   '["web_search", "web_fetch", "current_time", "convert_time", "missions", "mission_push"]',
   false)
ON CONFLICT (name) DO NOTHING;

-- Seeds the 'coder' agent: coding missions pick this from the agent
-- dropdown at creation time (missions.go's create handler resolves an
-- agent's route the same way chat sessions do) to get the
-- GLM-then-Nova-reasoning chain instead of the general default.
INSERT INTO agents (name, description, prompt_overlay, route, review_route, skills, tools)
VALUES
  ('coder', 'Coding missions and tasks: GLM primary, Nova reasoning fallback.', '', 'coding', 'coding',
   '["coding-task"]',
   '["missions", "mission_push", "web_search", "web_fetch", "current_time", "convert_time"]')
ON CONFLICT (name) DO NOTHING;
