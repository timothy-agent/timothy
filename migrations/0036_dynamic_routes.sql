-- Routing becomes fully dynamic (no hardcoded route names in code):
-- capability is what a route can serve (chat/embeddings/vision,
-- replacing the name=="embedding" special case in
-- internal/gateway/router/router.go); role marks which route serves
-- one of the 4 functions Timothy requires to work at all
-- (default/embedding/vision/summarize). A route's name is now free
-- text the user can rename at will — code resolves "the route with
-- role = X", never a literal name.
ALTER TABLE routes ADD COLUMN IF NOT EXISTS capability text NOT NULL DEFAULT 'chat'
    CHECK (capability IN ('chat', 'embeddings', 'vision'));
ALTER TABLE routes ADD COLUMN IF NOT EXISTS role text
    CHECK (role IN ('default', 'embedding', 'vision', 'summarize'));
CREATE UNIQUE INDEX IF NOT EXISTS routes_role_unique_idx ON routes (role) WHERE role IS NOT NULL;

-- Backfill existing fixed-name rows so upgraders keep their meaning.
-- 'research'/'local'/'coding' get no role — they become plain
-- user-owned routes going forward.
UPDATE routes SET capability = 'embeddings', role = 'embedding' WHERE name = 'embedding' AND role IS NULL;
UPDATE routes SET capability = 'vision', role = 'vision' WHERE name = 'vision' AND role IS NULL;
UPDATE routes SET role = 'default' WHERE name = 'default' AND role IS NULL;
UPDATE routes SET role = 'summarize' WHERE name = 'summarize' AND role IS NULL;

-- 'research'/'local'/'coding' were seeded by 0002/0022 with an empty
-- chain and disabled, for every install to date (no create/delete UI
-- existed before this migration, so no real user chain can exist
-- under those names yet). Drop the never-configured seed rows so a
-- fresh install starts with only the 4 required routes; an upgrader
-- who already gave one of these a chain keeps that row untouched.
DELETE FROM routes WHERE name IN ('research', 'local', 'coding') AND role IS NULL AND chain = '[]' AND enabled = false;
