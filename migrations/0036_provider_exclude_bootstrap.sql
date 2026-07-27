-- exclude_from_bootstrap opts a provider out of BootstrapChain's
-- auto-fallback fill (D-033 follow-up): a local/dev provider (e.g.
-- Ollama) otherwise gets silently appended as a fallback on the
-- shared default/summarize/embedding routes the moment it's
-- connected, which let a below-floor local model serve production
-- mission traffic during a cloud-provider outage.
ALTER TABLE providers ADD COLUMN IF NOT EXISTS exclude_from_bootstrap boolean NOT NULL DEFAULT false;
