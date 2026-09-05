# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

- issue #561: convert each legacy self-describing "github" destinations
  entry into a saved destinations row (kind 'github') plus an
  id-referencing entry. No schema change, a data step only. Idempotent:
  ```sh
  psql "$DATABASE_URL" -f scripts/migrate-github-entries.sql
  ```
