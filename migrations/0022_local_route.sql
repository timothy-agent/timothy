-- Seeds a 5th fixed route name: 'local' — the sensitive-tool-routing
-- floor (gmail_read / gmail_read_attachment force the rest of their
-- turn onto this route, keyed by SENSITIVE_TOOL_ROUTE) needs a real
-- route name to target; the fixed set from 0002_gateway.sql has none
-- meant for it. No chain, no opinions — the user chains it to a
-- trusted/local provider in Settings.
INSERT INTO routes (name)
VALUES ('local')
ON CONFLICT (name) DO NOTHING;
