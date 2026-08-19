-- Destinations: operator-created outbound sinks missions deliver
-- results to (internal/brain/destinations). config is per-kind and
-- never a secret value; credential_ref names a secret store ref
-- (unused for email/webhook in slice 1, present for forward
-- compatibility with telegram).
CREATE TABLE destinations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL UNIQUE,
    kind        text NOT NULL CHECK (kind IN ('email', 'webhook', 'telegram')),
    config      jsonb NOT NULL DEFAULT '{}',
    credential_ref text NOT NULL DEFAULT '',
    enabled     boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
