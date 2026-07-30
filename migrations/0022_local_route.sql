-- Seeds a 5th fixed route name: 'local' — the sensitive-tool-routing
-- floor (gmail_read / gmail_read_attachment force the rest of their
-- turn onto this route, keyed by the sensitive_tool_route setting)
-- needs a real
-- route name to target; the fixed set from 0002_gateway.sql has none
-- meant for it. No chain, no opinions — the user chains it to a
-- trusted/local provider in Settings.
INSERT INTO routes (name)
VALUES ('local')
ON CONFLICT (name) DO NOTHING;

-- Seeds a 6th fixed route name: 'coding' — GLM primary (glm-5.2, the
-- stronger of the two Z.ai models already configured) with Nova Pro
-- as fallback (a reasoning-capable model, unlike the nova-lite already
-- used elsewhere). No chain here: like 'local' above, the chain is
-- provider-instance-specific and gets set via PATCH once the
-- providers exist in this deployment, not hardcoded into a migration.
INSERT INTO routes (name)
VALUES ('coding')
ON CONFLICT (name) DO NOTHING;
