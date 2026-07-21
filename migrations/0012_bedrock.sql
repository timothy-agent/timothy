-- Amazon Bedrock provider: first-party Amazon models only — Nova for
-- chat/tools (via us.* cross-region inference profiles; the bare model
-- IDs reject on-demand invocation), Titan for embeddings — so usage
-- bills against AWS credits. Disabled until credentials exist:
--   local dev: set credential_ref to an AWS profile name, run
--     `aws sso login --profile <name>`, and mount ~/.aws read-only
--     into the gateway container (see deploy/docker-compose.yml);
--   on AWS: attach an IAM role with bedrock:InvokeModel* / Converse*
--     and leave credential_ref EMPTY (a named profile with no shared
--     config fails client construction).
-- Prices are USD per million tokens, us-east-1, from AWS Bedrock
-- pricing 2026-07-11. Nova cache reads bill at 25% of input; cache
-- writes are free, so no cache_write_per_mtok key.
-- amazon.titan-embed-text-v1 outputs 1536 dimensions — the exact
-- match for memoryd's vector(1536) embedding column (v2 tops out at
-- 1024 and would need a schema migration).
INSERT INTO providers (name, kind, driver, base_url, default_model, models, credential_ref)
VALUES ('bedrock', 'api', 'bedrock', 'us-east-1', 'us.amazon.nova-pro-v1:0', '[
   {"id": "us.amazon.nova-pro-v1:0", "capabilities": ["chat", "streaming", "tools"], "context_window": 300000,
    "prices": {"input_per_mtok": 0.8, "output_per_mtok": 3.2, "cache_read_per_mtok": 0.2}},
   {"id": "us.amazon.nova-lite-v1:0", "capabilities": ["chat", "streaming", "tools"], "context_window": 300000,
    "prices": {"input_per_mtok": 0.06, "output_per_mtok": 0.24, "cache_read_per_mtok": 0.015}},
   {"id": "us.amazon.nova-micro-v1:0", "capabilities": ["chat", "streaming", "tools"], "context_window": 128000,
    "prices": {"input_per_mtok": 0.035, "output_per_mtok": 0.14, "cache_read_per_mtok": 0.00875}},
   {"id": "amazon.titan-embed-text-v1", "capabilities": ["embeddings"],
    "prices": {"input_per_mtok": 0.1}}
 ]', '')
ON CONFLICT (name) DO NOTHING;

-- Append Bedrock as the LAST entry of every chat-capable chain: inert
-- while the provider row stays disabled, and existing provider order
-- is untouched. Nova Pro backs the heavyweight categories, Nova Lite
-- the cheap ones. The containment guard keeps this idempotent and
-- skips chains that already carry a bedrock entry.
-- To serve everything from AWS:
--   UPDATE providers SET enabled = true  WHERE name = 'bedrock';
--   UPDATE providers SET enabled = false WHERE name <> 'bedrock';
UPDATE routes
SET chain = chain || jsonb_build_array(jsonb_build_object(
      'provider_id', (SELECT id FROM providers WHERE name = 'bedrock'),
      'model', CASE WHEN name IN ('mini', 'summarize', 'realtime')
                    THEN 'us.amazon.nova-lite-v1:0'
                    ELSE 'us.amazon.nova-pro-v1:0' END))
WHERE name IN ('reasoning', 'default', 'research', 'mini', 'summarize', 'realtime')
  AND NOT chain @> jsonb_build_array(jsonb_build_object(
      'provider_id', (SELECT id FROM providers WHERE name = 'bedrock')));

-- Give the embedding route its first chain — it was seeded empty in
-- 0002 pending an embedding-capable provider. Only fills a still-empty
-- chain; a user-configured chain is never overwritten. Enable with:
--   UPDATE routes SET enabled = true WHERE name = 'embedding';
UPDATE routes
SET chain = jsonb_build_array(jsonb_build_object(
      'provider_id', (SELECT id FROM providers WHERE name = 'bedrock'),
      'model', 'amazon.titan-embed-text-v1'))
WHERE name = 'embedding' AND chain = '[]'::jsonb;
