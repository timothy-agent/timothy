-- One secret backend is the default: every credential entered in the
-- UI (provider keys, connector tokens) is stored through it, instead
-- of a per-key source choice. 'db' (Timothy's own encrypted storage)
-- joins the table so it can carry the flag; its config stays empty.
ALTER TABLE secret_backend_config DROP CONSTRAINT secret_backend_config_check;
ALTER TABLE secret_backend_config
    ADD CONSTRAINT secret_backend_config_check CHECK (backend IN ('db', 'vault', 'asm'));
ALTER TABLE secret_backend_config
    ADD COLUMN IF NOT EXISTS is_default boolean NOT NULL DEFAULT false;

-- At most one default, enforced by the database not the application.
CREATE UNIQUE INDEX IF NOT EXISTS secret_backend_config_one_default
    ON secret_backend_config ((true)) WHERE is_default;

-- Built-in storage starts as the default unless one is already set.
INSERT INTO secret_backend_config (backend, config, is_default)
SELECT 'db', '{}'::jsonb, NOT EXISTS (SELECT 1 FROM secret_backend_config WHERE is_default)
ON CONFLICT (backend) DO NOTHING;
