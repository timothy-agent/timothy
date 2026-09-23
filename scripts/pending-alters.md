# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

None pending: the #817, #818 and #820 blocks were applied on homelab
with the v0.1.0-alpha.97 deploy on 2026-09-23, including the
post-deploy DROP DEFAULT on missions.origin_kind.
