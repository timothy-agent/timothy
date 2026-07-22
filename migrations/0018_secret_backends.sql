-- Connection settings for secret backends, editable from the Settings
-- UI. Non-secret parts only: the Vault token itself lives in the
-- secrets table (backend 'db') under the ref named by the config's
-- token_ref, so this table never holds a credential.
--
-- Exactly one backend is the store-wide default (D-031): every
-- credential entered in the UI is stored through it. 'db' (built-in
-- storage) needs no config but sits in the table so it can carry the
-- flag; it is seeded as the default.
CREATE TABLE IF NOT EXISTS secret_backend_config (
    backend    text PRIMARY KEY,
    config     jsonb NOT NULL DEFAULT '{}'::jsonb,
    is_default boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT secret_backend_config_check CHECK (backend IN ('db', 'vault', 'asm', 'file'))
);

-- At most one default, enforced by the database not the application.
CREATE UNIQUE INDEX IF NOT EXISTS secret_backend_config_one_default
    ON secret_backend_config ((true)) WHERE is_default;

INSERT INTO secret_backend_config (backend, config, is_default)
SELECT 'db', '{}'::jsonb, NOT EXISTS (SELECT 1 FROM secret_backend_config WHERE is_default)
ON CONFLICT (backend) DO NOTHING;
