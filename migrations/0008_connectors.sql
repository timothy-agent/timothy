-- Connectors are third-party integrations the agent can call as tools
-- (MCP servers, Google Workspace, ...). Like providers they are data:
-- admin CRUD writes rows, brain reloads and surfaces each connector's
-- tools into the registry namespaced by connector name. credential_ref
-- names a secret in the secrets table — never a value.
CREATE TABLE IF NOT EXISTS connectors (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text UNIQUE NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('mcp', 'google', 'github', 'microsoft')),
    -- kind-specific settings: mcp → {transport, endpoint, headers},
    -- google/microsoft → {client_id, client_secret_ref, scopes}. OAuth
    -- tokens NEVER live here; they go to the secrets table under
    -- credential_ref.
    config         jsonb NOT NULL DEFAULT '{}',
    credential_ref text NOT NULL DEFAULT '',
    enabled        boolean NOT NULL DEFAULT false,
    -- Marks a whole connector as sensitive: every tool it serves
    -- shares the connector's name as a suffix (session.SensitiveTools),
    -- pinning them all onto the privacy-floor route without listing
    -- tool names in Go.
    sensitive      boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
