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
    -- agent is still "who serves this," same as chat. These four
    -- columns are meaningless to a chat-only agent and stay at their
    -- defaults for one; a mission-capable agent sets what it needs.
    -- harness is the coding executor a mission delegates to (mission.
    -- harness -> agent.harness -> settings.coding_executor -> native);
    -- empty means inherit.
    review_route       text NOT NULL DEFAULT '',
    budget_usd         numeric(12,2),
    approval_allowlist jsonb NOT NULL DEFAULT '[]',
    harness            text NOT NULL DEFAULT '',
    -- Knowledge collections (kb_collections.name) this agent may search
    -- with search_kb (D-060) — empty means none, same opt-in semantics
    -- as skills/tools: an agent must name a collection explicitly.
    knowledge      jsonb NOT NULL DEFAULT '[]',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS agents_one_default
    ON agents ((true)) WHERE is_default;

-- Seed exactly one agent: 'general', because exactly one default
-- agent must exist. Every other agent is created by the operator in
-- the UI. Route is 'default' (seeded in 0002_gateway.sql, guaranteed
-- to exist by migration order). Skills/tools are
-- opt-in only (empty means none): the seed allowlists every shipped
-- skill pack — an allowlist entry costs one index line per turn, the
-- body loads only on demand — and a minimal tool surface, since every
-- listed tool schema rides every turn's prompt.
--
-- Tool names are the compiled-in builtins' exact registered names
-- (internal/brain/tools/builtin/*.go) plus the unified connector
-- capability names (connectors.Manager.Tools aggregates every
-- connected account behind one name per capability, e.g. "search_mail"
-- covers every connected google/microsoft mail account); an allowlist
-- entry here is agent-authored before any connector even exists, and
-- covers every current and future account serving that capability.
-- Guarded both ways for is_default: if any default agent already
-- exists, this seed must not attempt to set is_default=true.
INSERT INTO agents (name, description, prompt_overlay, route, skills, tools, is_default)
SELECT 'general', 'Everyday questions and tasks on a strong all-round chain.', '', 'default',
    '["research-brief", "deep-research", "coding", "email-research"]',
    '["get_current_time", "convert_time", "calculate", "convert_currency", "search_web", "fetch_url", "remember", "list_missions", "get_mission", "push_mission_branch", "followup_mission", "search_mail", "read_mail", "read_mail_attachment", "send_mail", "list_calendar_events", "create_calendar_event"]',
    NOT EXISTS (SELECT 1 FROM agents WHERE is_default)
WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'general');
