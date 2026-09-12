# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

```sql
-- issue #718: plan units renamed verify_cmd -> check_cmd. The brain
-- reads both keys (PlanUnit.LegacyVerifyCmd) until this has run; after
-- it, the alias field can be dropped.
UPDATE missions
SET plan = jsonb_set(
  plan,
  '{units}',
  (SELECT jsonb_agg(
     CASE WHEN u ? 'verify_cmd' AND NOT u ? 'check_cmd'
          THEN (u - 'verify_cmd') || jsonb_build_object('check_cmd', u->'verify_cmd')
          ELSE u - 'verify_cmd' END)
   FROM jsonb_array_elements(plan->'units') AS u))
WHERE plan ? 'units' AND jsonb_typeof(plan->'units') = 'array'
  AND EXISTS (SELECT 1 FROM jsonb_array_elements(plan->'units') AS u WHERE u ? 'verify_cmd');
```

```sql
-- issue #720: per-mission executor session policy ('' / 'resume' keep
-- today's resume behaviour, 'fresh' starts every unit cold).
ALTER TABLE missions ADD COLUMN IF NOT EXISTS executor_session_policy text NOT NULL DEFAULT '';
```
