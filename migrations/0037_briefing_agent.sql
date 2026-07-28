-- Briefing agent seed (roadmap item 3): the daily-briefing skill's
-- prompt overlay and approval allowlist, seeded once so recurring
-- "briefing" missions have an agent to run against. Goal-driven: the
-- topics to cover come from the mission goal text itself, resolved at
-- mission-run time — no separate topics table. Pre-release: fold into
-- 0021_agents.sql at next DB reset — kept as its own numbered file for
-- now per the iterative-migration rule while the schema is still
-- moving.
--
-- approval_allowlist names tools by their bare, connector-unprefixed
-- name ("calendar_list_events", not "google-calendar_calendar_list_
-- events") — matchGrant's suffix rule (D-036, internal/brain/tools/
-- permissions.go) matches these against the runtime-namespaced tool
-- name a connector actually registers, so this works regardless of
-- which connector name serves gmail/calendar for a given user.
INSERT INTO agents (name, description, prompt_overlay, skills, tools, approval_allowlist, memory, route)
VALUES (
    'briefing',
    'Goal-driven briefing: covers every topic named in the mission goal, adds calendar/email highlights when connected, writes briefing.md.',
    'Load the daily-briefing skill first and follow its rules for the rest of this mission. Cover every topic named in the mission goal, using web_search to check each one for anything from roughly the last 24 hours. When calendar and email tools are available, add short highlight sections for today''s events and unread mail; when they are not available, say so plainly instead of inventing content. Write the full briefing as briefing.md at the workspace root. Only send it by email via gmail_send if the goal explicitly names a recipient.',
    '["daily-briefing"]',
    '["web_search", "calendar_list_events", "gmail_search", "gmail_read", "gmail_send"]',
    '["gmail_search", "gmail_read", "calendar_list_events", "gmail_send"]',
    false,
    'research'
)
ON CONFLICT (name) DO NOTHING;
