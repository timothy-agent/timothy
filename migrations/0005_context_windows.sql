-- Stamp context windows onto the seeded provider models so callers
-- (the brain's compactor) can derive token budgets from the model
-- actually in use instead of a static guess. Idempotent: re-running
-- overwrites the same values.
UPDATE providers
SET models = (
    SELECT jsonb_agg(
        CASE m->>'id'
            WHEN 'claude-opus-4-8'           THEN m || '{"context_window": 200000}'
            WHEN 'claude-sonnet-5'           THEN m || '{"context_window": 200000}'
            WHEN 'claude-haiku-4-5-20251001' THEN m || '{"context_window": 200000}'
            WHEN 'glm-4.7'                   THEN m || '{"context_window": 200000}'
            WHEN 'grok-4.3'                  THEN m || '{"context_window": 256000}'
            WHEN 'grok-4.5'                  THEN m || '{"context_window": 256000}'
            ELSE m
        END ORDER BY ord)
    FROM jsonb_array_elements(models) WITH ORDINALITY AS t(m, ord)
)
WHERE jsonb_array_length(models) > 0;
