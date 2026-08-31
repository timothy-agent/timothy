-- One-time scrub for issue #444: admin_audit rows written before the
-- go-forward redaction (internal/gateway/admin/admin.go,
-- internal/brain/connectors/connectors.go) stored provider Headers and
-- connector Config verbatim, including secret values.
--
-- Idempotent: every update is guarded by a "not already scrubbed"
-- check, safe to re-run.
--
-- Coarser than the go-forward redaction on purpose: the Go code
-- redacts individual string leaves and keeps keys/structure so a
-- fresh audit still shows what changed shape-wise; here we replace
-- the whole headers/config sub-object with a single marker. Faithful
-- SQL replication of the recursive per-string-leaf rule (arbitrary
-- config nesting) isn't worth the complexity for a one-time pass over
-- an append-only table nothing reads.

-- provider rows: blank the whole headers object, before and after.
UPDATE admin_audit
SET before = jsonb_set(before, '{headers}', '{"redacted": true}'::jsonb)
WHERE entity = 'provider'
  AND before ? 'headers'
  AND before->'headers' <> '{"redacted": true}'::jsonb;

UPDATE admin_audit
SET after = jsonb_set(after, '{headers}', '{"redacted": true}'::jsonb)
WHERE entity = 'provider'
  AND after ? 'headers'
  AND after->'headers' <> '{"redacted": true}'::jsonb;

-- connector rows: blank the whole config object, before and after.
UPDATE admin_audit
SET before = jsonb_set(before, '{config}', '{"redacted": true}'::jsonb)
WHERE entity = 'connector'
  AND before ? 'config'
  AND before->'config' <> '{"redacted": true}'::jsonb;

UPDATE admin_audit
SET after = jsonb_set(after, '{config}', '{"redacted": true}'::jsonb)
WHERE entity = 'connector'
  AND after ? 'config'
  AND after->'config' <> '{"redacted": true}'::jsonb;
