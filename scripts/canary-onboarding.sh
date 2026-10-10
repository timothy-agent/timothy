#!/usr/bin/env bash
# Onboarding canary: brings up a second, throwaway compose project with
# fresh volumes and walks it in a real browser the way a new operator
# would: token link, welcome wizard, provider test, first chat, sample
# mission, setup checklist. Manual gate, not CI: it needs a model key
# (CANARY_PROVIDER_KEY) or a reachable host Ollama
# (CANARY_PROVIDER_PRESET=ollama).
#
# Env: CANARY_PROVIDER_PRESET (default openai), CANARY_PROVIDER_KEY,
# CANARY_PROVIDER_BASE_URL, CANARY_MODEL, CANARY_ONBOARDING_NO_BUILD=1
# (reuse the canary project's images), CANARY_ONBOARDING_KEEP=1 (leave
# the stack up). --dry-run prints the commands instead of running them.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT="timothy-onboarding"
PLAYWRIGHT_IMAGE="mcr.microsoft.com/playwright:v1.63.0-noble"
HEALTH_TIMEOUT_SECS=180
STAGES=(token wizard_source provider_verified roles_shown wizard_done first_reply
  sample_mission_created sample_mission_terminal checklist_ticks)

DRY_RUN=0
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
fi

# The API token stays in the shell environment only: sourced here,
# passed to containers by name, never printed.
if [[ -z "${TIMOTHY_API_TOKEN:-}" && "${DRY_RUN}" == 0 ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/deploy/.env"
  set +a
fi
if [[ -z "${TIMOTHY_API_TOKEN:-}" && "${DRY_RUN}" == 0 ]]; then
  echo "canary-onboarding: TIMOTHY_API_TOKEN not set and deploy/.env did not provide it" >&2
  exit 2
fi

# After the source above: deploy/.env's own ports belong to the dev stack.
export BRAIN_PORT=8310 WEB_PORT=3310
export CANARY_PROVIDER_PRESET="${CANARY_PROVIDER_PRESET:-openai}"
export CANARY_PROVIDER_KEY="${CANARY_PROVIDER_KEY:-}"
export CANARY_MODEL="${CANARY_MODEL:-}"
CANARY_RUN_TAG="onboarding-$(date +%Y%m%d-%H%M%S)"
export CANARY_RUN_TAG
export TIMOTHY_API_TOKEN="${TIMOTHY_API_TOKEN:-}"
if [[ "${CANARY_PROVIDER_PRESET}" == "ollama" ]]; then
  export CANARY_PROVIDER_BASE_URL="${CANARY_PROVIDER_BASE_URL:-http://host.docker.internal:11434/v1}"
else
  export CANARY_PROVIDER_BASE_URL="${CANARY_PROVIDER_BASE_URL:-}"
  if [[ -z "${CANARY_PROVIDER_KEY}" ]]; then
    echo "canary-onboarding: CANARY_PROVIDER_KEY is required for preset ${CANARY_PROVIDER_PRESET} (or use CANARY_PROVIDER_PRESET=ollama)" >&2
    exit 2
  fi
fi

# Docker Desktop does not publish host ports into a --network host
# container, so on macOS the browser reaches the web port through the
# host alias instead.
if [[ "$(uname -s)" == "Darwin" ]]; then
  WEB_URL="http://host.docker.internal:${WEB_PORT}"
  NET_ARGS=()
else
  WEB_URL="http://localhost:${WEB_PORT}"
  NET_ARGS=(--network host)
fi

COMPOSE=(docker compose -p "${PROJECT}" -f "${REPO_ROOT}/deploy/docker-compose.yml" --env-file "${REPO_ROOT}/deploy/.env")
UP=(up -d --build)
if [[ "${CANARY_ONBOARDING_NO_BUILD:-}" == 1 ]]; then
  UP=(up -d)
fi
# A separate node_modules volume: the host's web/node_modules holds
# alpine binaries that the noble image cannot run.
PLAYWRIGHT=(docker run --rm ${NET_ARGS[@]+"${NET_ARGS[@]}"}
  -e "CANARY_BASE_URL=${WEB_URL}" -e TIMOTHY_API_TOKEN -e CANARY_PROVIDER_PRESET
  -e CANARY_PROVIDER_KEY -e CANARY_PROVIDER_BASE_URL -e CANARY_MODEL -e CANARY_RUN_TAG
  -v "${REPO_ROOT}/web:/app" -v "${PROJECT}-playwright-node-modules:/app/node_modules" -w /app
  "${PLAYWRIGHT_IMAGE}"
  sh -c "npm ci --no-audit --no-fund --loglevel=error && npx playwright test e2e/onboarding.spec.ts")

if (( DRY_RUN )); then
  echo "canary-onboarding: dry run, preset=${CANARY_PROVIDER_PRESET} tag=${CANARY_RUN_TAG}"
  echo "canary-onboarding: would run: ${COMPOSE[*]} down -v --remove-orphans"
  echo "canary-onboarding: would run: BRAIN_PORT=${BRAIN_PORT} WEB_PORT=${WEB_PORT} ${COMPOSE[*]} ${UP[*]}"
  echo "canary-onboarding: would wait for: http://localhost:${BRAIN_PORT}/health"
  echo "canary-onboarding: would run: ${PLAYWRIGHT[*]}"
  echo "canary-onboarding: would run: ${COMPOSE[*]} down -v --remove-orphans"
  exit 0
fi

teardown() {
  local rc=$?
  trap - EXIT INT TERM
  if [[ "${CANARY_ONBOARDING_KEEP:-}" == 1 ]]; then
    echo "canary-onboarding: keeping project ${PROJECT} (web ${WEB_URL}, brain http://localhost:${BRAIN_PORT})"
  else
    echo "canary-onboarding: tearing down project ${PROJECT}"
    "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 \
      || echo "canary-onboarding: WARNING - teardown of ${PROJECT} failed; run: docker compose -p ${PROJECT} down -v" >&2
    # Mission sandboxes are started by sandboxd, not compose; their
    # owner label is the compose project name.
    local leftovers
    leftovers="$(docker ps -aq --filter "label=timothy.owner=${PROJECT}")"
    if [[ -n "${leftovers}" ]]; then
      # shellcheck disable=SC2086
      docker rm -f ${leftovers} >/dev/null || true
    fi
  fi
  exit "${rc}"
}
trap teardown EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "canary-onboarding: preset=${CANARY_PROVIDER_PRESET}${CANARY_MODEL:+ model=${CANARY_MODEL}} tag=${CANARY_RUN_TAG}"
# A kept project from an earlier run would not be a fresh install.
"${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
echo "canary-onboarding: starting project ${PROJECT} on ports ${BRAIN_PORT}/${WEB_PORT}"
# The brain image copies build/selfdocs, which only `make selfdocs` writes.
if [[ "${CANARY_ONBOARDING_NO_BUILD:-}" != 1 ]]; then
  make -C "${REPO_ROOT}" selfdocs
fi
"${COMPOSE[@]}" "${UP[@]}"

start=$(date +%s)
until curl -sf --max-time 5 "http://localhost:${BRAIN_PORT}/health" \
  | python3 -c 'import json,sys; sys.exit(json.load(sys.stdin).get("status") != "ok")' 2>/dev/null; do
  if (( $(date +%s) - start > HEALTH_TIMEOUT_SECS )); then
    echo "canary-onboarding: FAIL - brain not healthy after ${HEALTH_TIMEOUT_SECS}s" >&2
    exit 1
  fi
  sleep 3
done
echo "canary-onboarding: brain healthy (t+$(( $(date +%s) - start ))s), running browser walk against ${WEB_URL}"

set +e
"${PLAYWRIGHT[@]}"
pw_rc=$?
set -e
# Playwright's failure snapshots record form field values; the stage
# report below carries the scrubbed failure detail instead.
rm -rf "${REPO_ROOT}/web/e2e/.out/test-results"

report="${REPO_ROOT}/web/e2e/.out/onboarding-${CANARY_RUN_TAG}.json"
table_rc=0
CANARY_REPORT="${report}" CANARY_STAGES="${STAGES[*]}" python3 <<'PY' || table_rc=$?
import json, os, sys
names = os.environ["CANARY_STAGES"].split()
try:
    with open(os.environ["CANARY_REPORT"]) as f:
        got = {s["name"]: s for s in json.load(f)["stages"]}
except (OSError, ValueError, KeyError) as e:
    print(f"canary-onboarding: FAIL - no stage report ({e})", file=sys.stderr)
    sys.exit(1)
failed = False
for name in names:
    s = got.get(name, {"status": "skipped", "ms": 0})
    line = f"canary-onboarding: {name:<24} {s['status']:<8} {s['ms'] / 1000:6.1f}s"
    if s["status"] == "ok":
        print(line, flush=True)
    else:
        failed = True
        detail = (s.get("detail") or "").splitlines()
        print(line + (f"  {detail[0]}" if detail else ""), file=sys.stderr, flush=True)
sys.exit(1 if failed else 0)
PY
if (( pw_rc != 0 || table_rc != 0 )); then
  echo "canary-onboarding: FAIL" >&2
  exit 1
fi
echo "canary-onboarding: PASS"
