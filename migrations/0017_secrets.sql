-- Secret values referenced by providers.credential_ref (and future
-- connectors). A ref name resolves only here — there is no env var
-- fallback; an unresolved ref leaves its provider keyless/unhealthy.
-- backend='db' secrets are envelope-encrypted with TIMOTHY_MASTER_KEY;
-- backend='vault'/'asm' rows carry no ciphertext, only backend_ref (the
-- external path/id), the value is fetched from that system at read time.
CREATE TABLE IF NOT EXISTS secrets (
    ref_name    text PRIMARY KEY,
    backend     text NOT NULL DEFAULT 'db',
    ciphertext  bytea,
    nonce       bytea,
    backend_ref text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT secrets_backend_check CHECK (backend IN ('db', 'vault', 'asm')),
    CONSTRAINT secrets_db_has_ciphertext
        CHECK (backend <> 'db' OR (ciphertext IS NOT NULL AND nonce IS NOT NULL)),
    CONSTRAINT secrets_external_has_ref
        CHECK (backend = 'db' OR backend_ref <> '')
);
