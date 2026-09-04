# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

- issue #560: destinations.kind gains 'github'.
  ```sql
  ALTER TABLE destinations DROP CONSTRAINT destinations_kind_check;
  ALTER TABLE destinations ADD CONSTRAINT destinations_kind_check
      CHECK (kind IN ('email', 'webhook', 'telegram', 'github'));
  ```
