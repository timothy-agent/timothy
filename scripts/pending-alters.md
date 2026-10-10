# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

## missions and notifications keyset paging indexes (#1112)

```sql
CREATE INDEX IF NOT EXISTS missions_created_idx ON missions (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS notifications_created_idx ON notifications (created_at DESC, id DESC);
```

Backs the paged `GET /v1/missions` and `GET /v1/notifications` order.
Matches `0001_init.sql`. Additive: safe to run before deploy.
Instances: homelab `timothy`, `demo1`, `demo2` and `demo3` on
timothy-oci.
