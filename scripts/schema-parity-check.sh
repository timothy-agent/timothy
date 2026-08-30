#!/usr/bin/env bash
# Proves migrations/0001_init.sql (fresh install) and the old 15-file
# migration sequence plus scripts/schema-consolidation-delta.sql
# (upgraded database) produce the identical schema (issue #423).
#
# Usage: scripts/schema-parity-check.sh
# Requires docker and a git checkout of main with the pre-consolidation
# migration files reachable via `git show main:migrations/<name>`.

set -euo pipefail

IMAGE="${PGVECTOR_IMAGE:-pgvector/pgvector:0.8.4-pg18-trixie}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'docker rm -f schema-parity-a schema-parity-b >/dev/null 2>&1 || true; rm -rf "$WORKDIR"' EXIT

echo "== Fetching old migration files from main =="
OLD_FILES=(0001_init.sql 0002_gateway.sql 0003_sessions.sql 0004_tools.sql 0005_memory.sql
    0006_settings.sql 0007_secrets.sql 0008_connectors.sql 0009_agents.sql 0010_missions.sql
    0011_attachments.sql 0012_knowledge.sql 0014_destinations.sql 0015_workflows.sql 0016_pdfgen.sql)
mkdir -p "$WORKDIR/old"
for f in "${OLD_FILES[@]}"; do
    git -C "$REPO_ROOT" show "main:migrations/$f" > "$WORKDIR/old/$f"
done

echo "== Starting container A (fresh: new 0001_init.sql) =="
docker rm -f schema-parity-a >/dev/null 2>&1 || true
docker run -d --name schema-parity-a -e POSTGRES_PASSWORD=parity -e POSTGRES_DB=timothy "$IMAGE" >/dev/null
echo "== Starting container B (upgraded: old files + delta) =="
docker rm -f schema-parity-b >/dev/null 2>&1 || true
docker run -d --name schema-parity-b -e POSTGRES_PASSWORD=parity -e POSTGRES_DB=timothy "$IMAGE" >/dev/null

wait_ready() {
    local name="$1"
    for _ in $(seq 1 60); do
        if docker exec "$name" pg_isready -U postgres >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    echo "container $name never became ready" >&2
    exit 1
}
wait_ready schema-parity-a
wait_ready schema-parity-b

echo "== Applying new consolidated init to A =="
docker cp "$REPO_ROOT/migrations/0001_init.sql" schema-parity-a:/tmp/0001_init.sql
docker exec -e PGPASSWORD=parity schema-parity-a psql -U postgres -d timothy -v ON_ERROR_STOP=1 -f /tmp/0001_init.sql

echo "== Applying old migration sequence + delta to B =="
for f in "${OLD_FILES[@]}"; do
    docker cp "$WORKDIR/old/$f" "schema-parity-b:/tmp/$f"
    docker exec -e PGPASSWORD=parity schema-parity-b psql -U postgres -d timothy -v ON_ERROR_STOP=1 -f "/tmp/$f"
done
docker cp "$REPO_ROOT/scripts/schema-consolidation-delta.sql" schema-parity-b:/tmp/delta.sql
docker exec -e PGPASSWORD=parity schema-parity-b psql -U postgres -d timothy -v ON_ERROR_STOP=1 -f /tmp/delta.sql

echo "== Dumping schemas =="
docker exec -e PGPASSWORD=parity schema-parity-a pg_dump -U postgres -d timothy --schema-only --no-owner --no-privileges > "$WORKDIR/a.sql"
docker exec -e PGPASSWORD=parity schema-parity-b pg_dump -U postgres -d timothy --schema-only --no-owner --no-privileges > "$WORKDIR/b.sql"

normalize() {
    # Strip comments, blank lines, SET/SELECT pg_catalog session lines,
    # and \restrict/\unrestrict tokens pg_dump emits, all of which vary
    # by connection/session/pg_dump version and carry no schema
    # information.
    command grep -vE '^(--|SET |SELECT pg_catalog\.set_config|\\restrict |\\unrestrict )' "$1" \
        | command grep -v '^$' \
        | sed -E 's/[[:space:]]+/ /g; s/[[:space:]]+$//'
}
normalize "$WORKDIR/a.sql" > "$WORKDIR/a.norm.sql"
normalize "$WORKDIR/b.sql" > "$WORKDIR/b.norm.sql"

echo "== Diffing normalized schemas =="
if diff -u "$WORKDIR/a.norm.sql" "$WORKDIR/b.norm.sql"; then
    echo "PARITY OK: fresh 0001_init.sql == upgraded (old files + delta)"
    exit 0
else
    echo "PARITY FAILED: schemas differ, see diff above" >&2
    exit 1
fi
