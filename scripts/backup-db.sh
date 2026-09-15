#!/usr/bin/env bash
# Dumps the whole Timothy database to a gzipped file outside the pgdata
# volume and prunes old dumps (issue #436). Cron-able: no prompts, no
# secret values on stdout, non-zero exit on any failure.
#
# Usage: scripts/backup-db.sh [output-dir]
#   BACKUP_DIR       output directory (default: <repo>/backups)
#   BACKUP_KEEP      how many dumps to keep (default: 14)
#
# Cron example (daily 03:15, log to syslog):
#   15 3 * * * /path/to/timothy/scripts/backup-db.sh >> /var/log/timothy-backup.log 2>&1
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE=(docker compose -f "${REPO_ROOT}/deploy/docker-compose.yml")
BACKUP_DIR="${1:-${BACKUP_DIR:-${REPO_ROOT}/backups}}"
KEEP="${BACKUP_KEEP:-14}"

log() { echo "$(date -u '+%Y-%m-%dT%H:%M:%SZ') backup-db: $*"; }
fail() { log "ERROR: $*" >&2; exit 1; }

case "${KEEP}" in
  ''|*[!0-9]*) fail "BACKUP_KEEP must be a whole number, got '${KEEP}'" ;;
esac
[[ "${KEEP}" -ge 1 ]] || fail "BACKUP_KEEP must be at least 1"

# Refuse to write inside the volume this backup exists to survive.
case "${BACKUP_DIR}" in
  /var/lib/postgresql*) fail "refusing to write backups into the postgres data directory" ;;
esac

mkdir -p "${BACKUP_DIR}" || fail "cannot create ${BACKUP_DIR}"

"${COMPOSE[@]}" ps --status running --services 2>/dev/null | grep -qx postgres \
  || fail "postgres container is not running; start the stack with 'make up' first"

stamp="$(date -u '+%Y%m%dT%H%M%SZ')"
target="${BACKUP_DIR}/timothy-${stamp}.sql.gz"
tmp="${target}.partial"
trap 'rm -f "${tmp}"' EXIT

log "dumping database to ${target}"
# Whole-database dump on purpose. Never narrow this to `pg_dump -t
# <table>`: a table-scoped dump silently drops the `secrets` table, and
# the restore then comes up with every provider and connector
# credential missing.
# -T: no TTY, so the gzip stream stays byte-exact. The password comes
# from the container's own POSTGRES_PASSWORD env, never this shell.
# shellcheck disable=SC2016 # single quotes on purpose: the password
# expands inside the container, never in this shell's process list.
if ! "${COMPOSE[@]}" exec -T postgres \
  sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" pg_dump -U timothy -d timothy --no-owner --no-privileges' \
  | gzip -c > "${tmp}"; then
  fail "pg_dump failed; no backup written"
fi

gzip -t "${tmp}" || fail "dump is not valid gzip; no backup written"
# A dump without the secrets table would restore into an instance that
# cannot decrypt anything, so treat its absence as a failed backup.
# grep -c, not -q: -q exits at the first match and the SIGPIPE it sends
# gzip trips pipefail, failing a backup that is actually fine.
[[ "$(gzip -dc "${tmp}" | grep -c 'CREATE TABLE public\.secrets' || true)" -ge 1 ]] \
  || fail "dump does not contain the secrets table; no backup written"

mv "${tmp}" "${target}"
trap - EXIT
chmod 600 "${target}"
log "wrote ${target} ($(du -h "${target}" | cut -f1))"

# Rotation by count, not age: a stack that was off for a month still
# keeps its last good dumps.
mapfile -t stale < <(command ls -1t "${BACKUP_DIR}"/timothy-*.sql.gz 2>/dev/null | tail -n "+$((KEEP + 1))")
for old in "${stale[@]}"; do
  log "pruning ${old}"
  rm -f "${old}"
done

log "done; ${BACKUP_DIR} holds $(command ls -1 "${BACKUP_DIR}"/timothy-*.sql.gz 2>/dev/null | wc -l | tr -d ' ') dump(s)"
