# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

## Review findings ledger and rework counter (D-092, issue #512)

Required on live DBs before/with the next deploy. Additive, safe to
run before deploy.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS review_findings jsonb NOT NULL DEFAULT '[]';
ALTER TABLE missions ADD COLUMN IF NOT EXISTS rework_rounds integer NOT NULL DEFAULT 0;
```

## generate_pdf and share_file on the general agent (issue #546)

Live rows seeded before this change never received the two builtins
(agent tool lists are opt-in, #229). Idempotent, safe to run any time.

```sql
UPDATE agents SET tools = tools || '["share_file"]'::jsonb
  WHERE name IN ('general', 'researcher') AND NOT tools ? 'share_file';
UPDATE agents SET tools = tools || '["generate_pdf"]'::jsonb
  WHERE name IN ('general', 'researcher') AND NOT tools ? 'generate_pdf';
```
