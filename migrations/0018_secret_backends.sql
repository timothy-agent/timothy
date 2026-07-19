-- Connection settings for external secret backends (vault, asm),
-- editable from the Settings UI. Non-secret parts only: the Vault
-- token itself lives in the secrets table (backend 'db') under the
-- ref named by the config's token_ref, so this table never holds a
-- credential.
CREATE TABLE IF NOT EXISTS secret_backend_config (
    backend    text PRIMARY KEY,
    config     jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT secret_backend_config_check CHECK (backend IN ('vault', 'asm'))
);
