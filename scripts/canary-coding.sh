#!/usr/bin/env bash
# Coding-mission canary: seeds a tiny fixture git repo inside the brain
# container, runs one coding mission against it end-to-end, and asserts
# the coding-specific harness contract — worktree provisioned, LLM
# review actually ran (coding never skips review), harness-verified
# artifact present in the worktree, zero human permission parks. The
# research canary (canary-mission.sh) cannot catch regressions in the
# git/worktree/review path; this one exists for exactly that.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE=(docker compose -f "${REPO_ROOT}/deploy/docker-compose.yml")
BASE_URL="${CANARY_BASE_URL:-http://localhost:${BRAIN_PORT:-8300}}"
TIMEOUT_SECS="${CANARY_TIMEOUT:-900}"
FIXTURE=/workspace/canary-fixture
GOAL="Add a CHANGELOG.md file at the repository root with a '## 0.1.0' heading and one bullet point describing the initial release."

# The API token stays in the shell environment only — sourced here,
# never printed.
if [[ -z "${TIMOTHY_API_TOKEN:-}" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/deploy/.env"
  set +a
fi
if [[ -z "${TIMOTHY_API_TOKEN:-}" ]]; then
  echo "canary-coding: TIMOTHY_API_TOKEN not set and deploy/.env did not provide it" >&2
  exit 2
fi

auth=(-H "Authorization: Bearer ${TIMOTHY_API_TOKEN}" -H "Content-Type: application/json")

echo "canary-coding: seeding fixture repo at ${FIXTURE} (inside brain)"
"${COMPOSE[@]}" exec -T brain sh -c "
  rm -rf ${FIXTURE} &&
  git init -q -b main ${FIXTURE} &&
  cd ${FIXTURE} &&
  printf '# Canary Fixture\n\nTiny repo the coding-mission canary works against.\n' > README.md &&
  git add README.md &&
  git -c user.name=timothy -c user.email=timothy@localhost commit -q -m 'add readme'
"

echo "canary-coding: creating mission against ${BASE_URL}"
create_resp="$(curl -sf "${auth[@]}" -X POST "${BASE_URL}/v1/missions" \
  -d "{\"goal\": \"${GOAL}\", \"kind\": \"coding\", \"repo_path\": \"${FIXTURE}\"}")"
id="$(echo "${create_resp}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
echo "canary-coding: mission ${id}"

start=$(date +%s)
phase=""
status=""
while :; do
  now=$(date +%s)
  if (( now - start > TIMEOUT_SECS )); then
    echo "canary-coding: FAIL — timed out after ${TIMEOUT_SECS}s (phase=${phase} status=${status})" >&2
    exit 1
  fi
  m="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}")"
  phase="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["phase"])')"
  status="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')"
  pending="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("pending_permission_tool") or "")')"
  echo "canary-coding: ${phase}/${status} (t+$((now - start))s)${pending:+ [PARKED on ${pending}]}"
  case "${phase}/${status}" in
    done/*) break ;;
    failed/*)
      echo "canary-coding: FAIL — mission failed" >&2
      curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}/events" | python3 -m json.tool | tail -40 >&2
      exit 1 ;;
  esac
  if [[ "${status}" == "paused" || "${status}" == "waiting_for_input" ]]; then
    echo "canary-coding: FAIL — mission parked (${status}); a canary must run unattended" >&2
    exit 1
  fi
  sleep 5
done

worktree="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("worktree") or "")')"
branch="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("branch") or "")')"

events="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}/events")"
CANARY_EVENTS="${events}" python3 <<'PY'
import json, os, sys
events = json.loads(os.environ["CANARY_EVENTS"])["events"]
kinds = {}
for e in events:
    kinds[e["kind"]] = kinds.get(e["kind"], 0) + 1

parks = kinds.get("mission.permission_requested", 0)
turns = [e for e in events if e["kind"] == "mission.turn"]
total_ms = sum((e.get("payload") or {}).get("duration_ms", 0) for e in turns)
verified = [e for e in events if e["kind"] == "mission.unit_verified"
            and (e.get("payload") or {}).get("passed") is True]

print(f"canary-coding: event counts: {json.dumps(kinds, sort_keys=True)}")
print(f"canary-coding: {len(turns)} model turns, {total_ms/1000:.0f}s total model time")

failures = []
if parks > 0:
    failures.append(f"{parks} permission park(s) — canary must run unattended")
if not verified:
    failures.append("no harness-verified unit — done was claimed, never proven")
if kinds.get("mission.review_skipped", 0) > 0:
    failures.append("review was skipped — coding missions must always be LLM-reviewed")
if kinds.get("mission.review_verdict", 0) == 0:
    failures.append("no review verdict — the coding review path never ran")
if len(turns) > 10:
    failures.append(f"{len(turns)} turns for a trivial change — loop is not right-sized")
if failures:
    print("canary-coding: FAIL — " + "; ".join(failures), file=sys.stderr)
    sys.exit(1)
PY

# The harness already verified declared artifacts in the worktree; this
# re-checks from outside the mission's own machinery — an independent
# witness that the branch and the file are really on disk.
if [[ -z "${worktree}" || -z "${branch}" ]]; then
  echo "canary-coding: FAIL — mission has no worktree/branch (worktree='${worktree}' branch='${branch}')" >&2
  exit 1
fi
"${COMPOSE[@]}" exec -T brain sh -c "
  git -C ${FIXTURE} rev-parse --verify --quiet '${branch}' >/dev/null &&
  test -s '${worktree}/CHANGELOG.md'
" || {
  echo "canary-coding: FAIL — branch ${branch} or ${worktree}/CHANGELOG.md missing in container" >&2
  exit 1
}

echo "canary-coding: PASS"
