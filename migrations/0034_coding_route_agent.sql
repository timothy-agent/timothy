-- Seeds a 6th fixed route name: 'coding' — GLM primary (glm-5.2, the
-- stronger of the two Z.ai models already configured) with Nova Pro
-- as fallback (a reasoning-capable model, unlike the nova-lite already
-- used elsewhere). No chain here: like 'local' (0022), the chain is
-- provider-instance-specific and gets set via PATCH once the
-- providers exist in this deployment, not hardcoded into a migration.
INSERT INTO routes (name)
VALUES ('coding')
ON CONFLICT (name) DO NOTHING;

-- Seeds the 'coder' agent: coding missions pick this from the agent
-- dropdown at creation time (missions.go's create handler resolves an
-- agent's route the same way chat sessions do) to get the
-- GLM-then-Nova-reasoning chain instead of the general default.
INSERT INTO agents (name, description, prompt_overlay, route, review_route)
VALUES
  ('coder', 'Coding missions and tasks: GLM primary, Nova reasoning fallback.', '', 'coding', 'coding')
ON CONFLICT (name) DO NOTHING;
