CREATE TABLE IF NOT EXISTS providers (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text UNIQUE NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('api', 'cli')),
    driver         text NOT NULL,
    base_url       text NOT NULL DEFAULT '',
    default_model  text NOT NULL DEFAULT '',
    models         jsonb NOT NULL DEFAULT '[]',
    credential_ref text NOT NULL DEFAULT '',
    headers        jsonb NOT NULL DEFAULT '{}',
    options        jsonb NOT NULL DEFAULT '{}',
    enabled        boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Routes are named provider/model fallback chains (D-033) — the
-- routing primitive. Agents reference a route by name; system callers
-- (embedding, summarize) use fixed names. strategy picks the chain
-- order: 'ordered' keeps the written priority, 'auto' scores entries
-- from recent ledger stats and declared prices, 'price'/'latency' are
-- auto with that factor dominant.
CREATE TABLE IF NOT EXISTS routes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text UNIQUE NOT NULL,
    chain      jsonb NOT NULL DEFAULT '[]',
    strategy   text NOT NULL DEFAULT 'ordered'
        CHECK (strategy IN ('ordered', 'auto', 'price', 'latency')),
    enabled    boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS cost_ledger (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    ts                 timestamptz NOT NULL DEFAULT now(),
    provider           text NOT NULL,
    model              text NOT NULL,
    route              text NOT NULL,
    agent              text NOT NULL DEFAULT '',
    session_id         text,
    lane_id            text,
    input_tokens       integer,
    output_tokens      integer,
    cache_read_tokens  integer,
    cache_write_tokens integer,
    latency_ms         integer NOT NULL,
    status             text NOT NULL,
    error_code         text,
    cost_usd           numeric(12, 6)
);

CREATE INDEX IF NOT EXISTS cost_ledger_ts_idx ON cost_ledger (ts);
CREATE INDEX IF NOT EXISTS cost_ledger_session_idx ON cost_ledger (session_id) WHERE session_id IS NOT NULL;

-- Seed providers, disabled until credentials exist and the user flips
-- them on. Model lists and prices are configuration the panel edits
-- later; these are starting points, not authority. Prices are USD per
-- million tokens, verified from provider docs 2026-07-10:
--   anthropic: sonnet-5 carries INTRO pricing (2/10) until 2026-08-31,
--   then 3/15 — update the row when it flips.
--   xai: cached-input rates unpublished; cache reads cost as 0 until
--   xAI documents them.
INSERT INTO providers (name, kind, driver, base_url, default_model, models, credential_ref)
VALUES
  ('anthropic', 'api', 'anthropic', '', 'claude-sonnet-5', '[
     {"id": "claude-opus-4-8", "capabilities": ["chat", "streaming", "tools"],
      "prices": {"input_per_mtok": 5, "output_per_mtok": 25, "cache_read_per_mtok": 0.5, "cache_write_per_mtok": 6.25}},
     {"id": "claude-sonnet-5", "capabilities": ["chat", "streaming", "tools"],
      "prices": {"input_per_mtok": 2, "output_per_mtok": 10, "cache_read_per_mtok": 0.2, "cache_write_per_mtok": 2.5}},
     {"id": "claude-haiku-4-5-20251001", "capabilities": ["chat", "streaming", "tools"],
      "prices": {"input_per_mtok": 1, "output_per_mtok": 5, "cache_read_per_mtok": 0.1, "cache_write_per_mtok": 1.25}}
   ]', 'ANTHROPIC_API_KEY'),
  ('zai-glm', 'api', 'openaicompat', 'https://api.z.ai/api/paas/v4', 'glm-4.7', '[
     {"id": "glm-4.7", "capabilities": ["chat", "streaming", "tools"],
      "prices": {"input_per_mtok": 0.6, "output_per_mtok": 2.2, "cache_read_per_mtok": 0.11}}
   ]', 'ZAI_API_KEY'),
  ('xai-grok', 'api', 'openaicompat', 'https://api.x.ai/v1', 'grok-4.3', '[
     {"id": "grok-4.3", "capabilities": ["chat", "streaming", "tools"],
      "prices": {"input_per_mtok": 1.25, "output_per_mtok": 2.5}},
     {"id": "grok-4.5", "capabilities": ["chat", "streaming", "tools"],
      "prices": {"input_per_mtok": 2, "output_per_mtok": 6}}
   ]', 'XAI_API_KEY')
ON CONFLICT (name) DO NOTHING;

-- Seed the baseline routes, chained cheap-capable-first where it
-- makes sense; disabled by default. 'default' serves plain chat, the
-- rest are chains agents and system callers reference. The embedding
-- chain stays empty until an embedding-capable provider is configured.
-- NOTE: the chain subselects resolve provider UUIDs by name and rely
-- on the provider INSERT above having run in this same migration; if
-- a seed provider is ever renamed, these lookups return NULL and the
-- chain entry becomes an "unknown provider id" skip at routing time —
-- edit both places together.
INSERT INTO routes (name, chain)
SELECT name, chain FROM (
  VALUES
    ('reasoning', (SELECT jsonb_build_array(
        jsonb_build_object('provider_id', (SELECT id FROM providers WHERE name = 'anthropic'), 'model', 'claude-opus-4-8'),
        jsonb_build_object('provider_id', (SELECT id FROM providers WHERE name = 'xai-grok'), 'model', 'grok-4.5')))),
    ('default', (SELECT jsonb_build_array(
        jsonb_build_object('provider_id', (SELECT id FROM providers WHERE name = 'anthropic'), 'model', 'claude-sonnet-5'),
        jsonb_build_object('provider_id', (SELECT id FROM providers WHERE name = 'zai-glm'), 'model', 'glm-4.7')))),
    ('mini', (SELECT jsonb_build_array(
        jsonb_build_object('provider_id', (SELECT id FROM providers WHERE name = 'anthropic'), 'model', 'claude-haiku-4-5-20251001'),
        jsonb_build_object('provider_id', (SELECT id FROM providers WHERE name = 'zai-glm'), 'model', 'glm-4.7')))),
    ('summarize', (SELECT jsonb_build_array(
        jsonb_build_object('provider_id', (SELECT id FROM providers WHERE name = 'anthropic'), 'model', 'claude-haiku-4-5-20251001'),
        jsonb_build_object('provider_id', (SELECT id FROM providers WHERE name = 'zai-glm'), 'model', 'glm-4.7')))),
    ('realtime', (SELECT jsonb_build_array(
        jsonb_build_object('provider_id', (SELECT id FROM providers WHERE name = 'anthropic'), 'model', 'claude-haiku-4-5-20251001')))),
    ('embedding', '[]'::jsonb)
) AS seeds(name, chain)
ON CONFLICT (name) DO NOTHING;
