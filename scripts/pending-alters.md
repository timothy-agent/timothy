# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

## missions.environment, missions.environment_marker dropped (#1015)

```sql
ALTER TABLE missions DROP COLUMN IF EXISTS environment, DROP COLUMN IF EXISTS environment_marker;
```

D-141: one sandbox image, so the per-mission image selector and the
marker that set it are gone. Removed in place from `0001_init.sql`.
Not additive: run it AFTER the release that includes #1015 is live on
an instance (the new binary never reads either column, the old one
does). Instances: homelab `timothy`, `demo1`, `demo2` and `demo3` on
timothy-oci.
