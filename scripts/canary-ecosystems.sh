#!/usr/bin/env bash
# Ecosystem smoke matrix (issue #1018): one dependency-audit coding
# mission per pinned reference repo in scripts/ecosystem-matrix.txt, one
# at a time. Records per-stage results (prepare, baseline, plan accepted
# on the first session, build, review, PR, cost, wall time) rather than
# pass/fail, under a gitignored results dir. Each row resets its fork to
# the pinned commit first and closes the mission's PR and deletes its
# branch afterwards. Manual only, never CI.
#
# Needs the stack up, gh authenticated with push access to the
# timothy-agent forks, and:
#   CANARY_GITHUB_CONNECTOR_ID    github connector that clones the forks
#   CANARY_GITHUB_DESTINATION_ID  github destination that opens the PR
# Optional:
#   CANARY_ECOSYSTEM   run one row by name (e.g. laravel)
#   CANARY_TIMEOUT     per-mission ceiling in seconds (default 5400)
#   CANARY_PAUSE_GRACE seconds a mission may stay paused (default 900)
#   CANARY_PR_WAIT     seconds to wait for the PR after done (default 180)
#   CANARY_RESULTS_DIR results root (default canary-results/ecosystems)
#   CANARY_OSV_DB=0    skip the daily osv-scanner database refresh
#   CANARY_CACHES_VOLUME / CANARY_SANDBOX_IMAGE  refresh target and image
# Audits use the refreshed database only when brain runs with
# MISSION_OSV_OFFLINE set.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_URL="${CANARY_BASE_URL:-http://localhost:${BRAIN_PORT:-8300}}"
TIMEOUT_SECS="${CANARY_TIMEOUT:-5400}"
PAUSE_GRACE_SECS="${CANARY_PAUSE_GRACE:-900}"
PR_WAIT_SECS="${CANARY_PR_WAIT:-180}"
MATRIX_FILE="${REPO_ROOT}/scripts/ecosystem-matrix.txt"
RESULTS_ROOT="${CANARY_RESULTS_DIR:-${REPO_ROOT}/canary-results/ecosystems}"
ONLY="${CANARY_ECOSYSTEM:-}"
CACHES_VOLUME="${CANARY_CACHES_VOLUME:-timothy_sandbox-caches}"
SANDBOX_IMAGE="${CANARY_SANDBOX_IMAGE:-timothy-sandbox:latest}"

# shellcheck disable=SC1091
source "${REPO_ROOT}/scripts/lib/ecosystem-matrix.sh"

missing=()
[[ -n "${CANARY_GITHUB_CONNECTOR_ID:-}" ]] || missing+=("CANARY_GITHUB_CONNECTOR_ID (id of the github connector that clones the timothy-agent forks)")
[[ -n "${CANARY_GITHUB_DESTINATION_ID:-}" ]] || missing+=("CANARY_GITHUB_DESTINATION_ID (id of the github destination that opens the PR)")
if (( ${#missing[@]} )); then
  printf 'canary-ecosystems: missing %s\n' "${missing[@]}" >&2
  exit 2
fi
if ! command -v gh >/dev/null; then
  echo "canary-ecosystems: gh CLI not found; it resets the forks and cleans up PRs" >&2
  exit 2
fi

# The API token stays in the shell environment only: sourced here,
# never printed.
if [[ -z "${TIMOTHY_API_TOKEN:-}" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/deploy/.env"
  set +a
fi
if [[ -z "${TIMOTHY_API_TOKEN:-}" ]]; then
  echo "canary-ecosystems: TIMOTHY_API_TOKEN not set and deploy/.env did not provide it" >&2
  exit 2
fi
auth=(-H "Authorization: Bearer ${TIMOTHY_API_TOKEN}" -H "Content-Type: application/json")

rows=()
while IFS= read -r line; do
  [[ -z "${line}" || "${line}" == \#* ]] && continue
  name="${line%%|*}"
  [[ -z "${ONLY}" || "${name}" == "${ONLY}" ]] && rows+=("${line}")
done < "${MATRIX_FILE}"
if (( ${#rows[@]} == 0 )); then
  echo "canary-ecosystems: no matrix row named '${ONLY}' in ${MATRIX_FILE}" >&2
  exit 2
fi

RUN_TAG="eco-$(date -u +%Y%m%dT%H%M%SZ)-$(printf '%04x' "${RANDOM}")"
RUN_DIR="${RESULTS_ROOT}/${RUN_TAG}"
mkdir -p "${RUN_DIR}"
SUMMARY="${RUN_DIR}/summary.tsv"
printf 'name\tprepare_ok\tbaseline_ok\tplan_first_session\tbuild_passed\treview_approved\tpr_opened\tcost\twall_secs\toutcome\tmission_id\tcause\n' > "${SUMMARY}"
echo "canary-ecosystems: run ${RUN_TAG}, ${#rows[@]} row(s), results in ${RUN_DIR}"

# One database per run day: counts cannot drift between rows.
if [[ "${CANARY_OSV_DB:-1}" != "0" ]]; then
  if docker volume inspect "${CACHES_VOLUME}" >/dev/null 2>&1; then
    ecos=()
    for row in "${rows[@]}"; do
      IFS='|' read -r _ _ _ _ _ _ osv _ <<< "${row}"
      for e in ${osv}; do
        [[ " ${ecos[*]} " == *" ${e} "* ]] || ecos+=("${e}")
      done
    done
    matrix_refresh_osv_db "${CACHES_VOLUME}" "${SANDBOX_IMAGE}" "$(date -u +%F)" "${ecos[@]}"
  else
    echo "canary-ecosystems: WARNING - volume ${CACHES_VOLUME} not found; osv-scanner database not refreshed" >&2
  fi
fi

# shellcheck disable=SC1091
source "${REPO_ROOT}/scripts/lib/canary-cancel.sh"

json_field() {
  python3 -c 'import json,sys; v=json.load(sys.stdin).get(sys.argv[1]); print("" if v is None else v)' "$1"
}

run_row() {
  local name ecosystem upstream fork branch sha case_name
  IFS='|' read -r name ecosystem upstream fork branch sha _ _ <<< "$1"
  case_name="${name}-$(printf '%04x%04x' "${RANDOM}" "${RANDOM}")"
  echo "canary-ecosystems: [${name}] resetting ${fork}@${branch} to ${sha:0:12}"
  matrix_reset_fork "${fork}" "${branch}" "${sha}"

  local goal="Audit this repository's dependencies for known vulnerabilities and outdated versions, upgrade them across every package manager the repository uses, keep the existing test suite passing, and open a pull request with the changes through the configured destination. Case: ${case_name}. Run tag: ${RUN_TAG}."
  local body
  body="$(GOAL="${goal}" REPO_URL="https://github.com/${fork}.git" python3 -c '
import json, os
print(json.dumps({
    "goal": os.environ["GOAL"], "kind": "coding", "unattended": True,
    "repo_url": os.environ["REPO_URL"],
    "connector_id": os.environ["CANARY_GITHUB_CONNECTOR_ID"],
    "destination_ids": [os.environ["CANARY_GITHUB_DESTINATION_ID"]],
}))')"
  id="$(curl -sf "${auth[@]}" -X POST "${BASE_URL}/v1/missions" -d "${body}" | json_field id)"
  echo "canary-ecosystems: [${name}] mission ${id} (case ${case_name})"
  canary_cancel_arm

  local start now m phase status outcome="" paused_since=0
  start=$(date +%s)
  while :; do
    now=$(date +%s)
    m="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}")"
    phase="$(json_field phase <<< "${m}")"
    status="$(json_field status <<< "${m}")"
    echo "canary-ecosystems: [${name}] ${phase}/${status} (t+$((now - start))s)"
    case "${phase}" in
      done|failed) outcome="${phase}"; break ;;
    esac
    if [[ "${status}" == "waiting_for_input" ]]; then
      outcome="parked"; break
    fi
    if [[ "${status}" == "paused" ]]; then
      (( paused_since )) || paused_since=${now}
      if (( now - paused_since > PAUSE_GRACE_SECS )); then
        outcome="parked"; break
      fi
    else
      paused_since=0
    fi
    if (( now - start > TIMEOUT_SECS )); then
      outcome="timeout"; break
    fi
    sleep 15
  done
  local wall=$(( $(date +%s) - start ))

  local events
  if [[ "${outcome}" == "done" ]]; then
    local waited=0
    while (( waited < PR_WAIT_SECS )); do
      events="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}/events")"
      if grep -Eq '"mission\.(pr_opened|delivery_failed)"' <<< "${events}"; then
        break
      fi
      sleep 10
      waited=$((waited + 10))
    done
  else
    curl -s -o /dev/null -X POST "${auth[@]}" "${BASE_URL}/v1/missions/${id}/cancel" || true
  fi
  canary_cancel_disarm

  m="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}")"
  events="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}/events")"
  local usage
  usage="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/admin/usage/mission?id=${id}" || echo '{}')"

  M="${m}" EVENTS="${events}" USAGE="${usage}" OUTCOME="${outcome}" WALL="${wall}" \
  NAME="${name}" ECOSYSTEM="${ecosystem}" UPSTREAM="${upstream}" FORK="${fork}" SHA="${sha}" \
  CASE_NAME="${case_name}" RUN_TAG="${RUN_TAG}" OUT="${RUN_DIR}/${name}.json" \
    python3 "${REPO_ROOT}/scripts/lib/ecosystem_stages.py" >> "${SUMMARY}"
  tail -1 "${SUMMARY}"

  local mission_branch
  mission_branch="$(json_field branch <<< "${m}")"
  if [[ -n "${mission_branch}" ]]; then
    matrix_cleanup_branch "${fork}" "${mission_branch}" "${branch}" \
      || echo "canary-ecosystems: WARNING - cleanup of ${fork}:${mission_branch} failed; close its PR and delete it by hand" >&2
  fi
}

for row in "${rows[@]}"; do
  run_row "${row}"
done

echo "canary-ecosystems: summary (${SUMMARY})"
column -t -s $'\t' "${SUMMARY}" 2>/dev/null || command cat "${SUMMARY}"
