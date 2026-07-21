-- One settings table for both boolean switches and typed values
-- (D-032): value becomes jsonb, runtime_settings rows fold in, the
-- extra table drops. Booleans store as jsonb true/false, typed knobs
-- as jsonb strings; empty string still means "built-in default".
ALTER TABLE settings ALTER COLUMN value TYPE jsonb USING to_jsonb(value);
INSERT INTO settings (key, value, updated_at)
SELECT key, to_jsonb(value), updated_at FROM runtime_settings
ON CONFLICT (key) DO NOTHING;
DROP TABLE runtime_settings;
