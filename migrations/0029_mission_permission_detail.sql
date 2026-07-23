-- pending_permission already holds the broker-issued id a mission is
-- parked on; these columns carry what the UI needs to render a real
-- decision prompt (tool name, args, danger level, rationale) instead
-- of a bare "waiting" banner -- mirrors chat's PermissionRequestEvent.
ALTER TABLE missions ADD COLUMN IF NOT EXISTS pending_permission_tool text NOT NULL DEFAULT '';
ALTER TABLE missions ADD COLUMN IF NOT EXISTS pending_permission_args text NOT NULL DEFAULT '';
ALTER TABLE missions ADD COLUMN IF NOT EXISTS pending_permission_danger text NOT NULL DEFAULT '';
ALTER TABLE missions ADD COLUMN IF NOT EXISTS pending_permission_rationale text NOT NULL DEFAULT '';
