# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

## Generate -> Build phase rename (issue #611)

The worker phase is now `build`. Code reads both spellings
(`parsePhase`, `parseFlow`), so this is safe to run before or after the
deploy; it exists so stored rows match the new vocabulary rather than
relying on the read-time alias forever.

Order matters: `missions.flow`'s CHECK constraint is dropped first, the
rows are rewritten, and only then is the new constraint added. Adding
it earlier fails, since Postgres validates a new CHECK against every
existing row immediately and the old `discover_generate` values are not
in the new list.

```sql
BEGIN;

ALTER TABLE missions DROP CONSTRAINT missions_flow_check;

UPDATE missions SET phase = 'build' WHERE phase = 'generate';
UPDATE missions SET flow = 'discover_build' WHERE flow = 'discover_generate';

ALTER TABLE missions ADD CONSTRAINT missions_flow_check
    CHECK (flow IN ('full', 'discover_build', 'no_prove', 'light'));

COMMIT;
```

`mission_events` is append-only and is deliberately left alone: its
historical rows keep whatever phase string they were written with, and
`parsePhase` translates them on read.
