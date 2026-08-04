-- Secret values referenced by providers.credential_ref (and future
-- connectors). A ref name resolves only here — there is no env var
-- fallback; an unresolved ref leaves its provider keyless/unhealthy.
-- backend='db' secrets are envelope-encrypted with TIMOTHY_MASTER_KEY;
-- backend='vault'/'asm'/'file' rows carry no ciphertext, only
-- backend_ref (the external path/id, or filename under the file
-- backend's mount dir), the value is fetched from that system at read
-- time.
CREATE TABLE IF NOT EXISTS secrets (
    ref_name    text PRIMARY KEY,
    backend     text NOT NULL DEFAULT 'db',
    ciphertext  bytea,
    nonce       bytea,
    backend_ref text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT secrets_backend_check CHECK (backend IN ('db', 'vault', 'asm', 'file')),
    CONSTRAINT secrets_db_has_ciphertext
        CHECK (backend <> 'db' OR (ciphertext IS NOT NULL AND nonce IS NOT NULL)),
    CONSTRAINT secrets_external_has_ref
        CHECK (backend = 'db' OR backend_ref <> '')
);

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
