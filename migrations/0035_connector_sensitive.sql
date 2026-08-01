-- Marks a whole connector as sensitive: every tool it serves shares the
-- connector's name as a suffix (session.SensitiveTools), pinning them
-- all onto the privacy-floor route without listing tool names in Go.
ALTER TABLE connectors ADD COLUMN sensitive boolean NOT NULL DEFAULT false;
