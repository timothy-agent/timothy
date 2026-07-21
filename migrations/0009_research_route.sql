-- The agent loop coerces a "research" turn that answers without
-- consulting a tool (D-009 min-retrieval floor). That floor keys on
-- the route name, so "research" must be a real, routable name. Route
-- it like reasoning: a strong model first, a cheaper fallback.

INSERT INTO routes (name, chain)
SELECT 'research', jsonb_build_array(
    jsonb_build_object('provider_id', (SELECT id FROM providers WHERE name = 'anthropic'), 'model', 'claude-opus-4-8'),
    jsonb_build_object('provider_id', (SELECT id FROM providers WHERE name = 'zai-glm'), 'model', 'glm-4.7'))
ON CONFLICT (name) DO NOTHING;
