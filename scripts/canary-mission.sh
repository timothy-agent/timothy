#!/usr/bin/env bash
# Canary mission: runs one golden research mission end-to-end against
# the live stack and asserts the harness contract holds — done within
# the iteration budget, zero human permission parks, artifacts verified
# by the harness. Run after every harness change (make canary) so
# regressions surface here, not in production missions.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_URL="${CANARY_BASE_URL:-http://localhost:${BRAIN_PORT:-8300}}"
TIMEOUT_SECS="${CANARY_TIMEOUT:-900}"

# A different goal every run: repeating one fixed goal would let
# provider prompt caches, model memorization, and leftover artifacts
# from prior runs flatter the result — a pass must mean the harness
# worked NOW, on work it hasn't seen before.
GOALS=(
  "Summarize what HTTP status code 429 means and when a client should retry, in a markdown file."
  "Explain how the Retry-After header works with 503 responses, in a markdown file."
  "Describe HTTP ETag headers and conditional requests, in a markdown file."
  "Explain CORS preflight requests and when browsers send them, in a markdown file."
  "Compare HTTP 301 and 308 redirects and when to use each, in a markdown file."
  "Describe the robots.txt format and how crawlers use it, in a markdown file."
  "Explain HTTP chunked transfer encoding and when it applies, in a markdown file."
  "Compare HTTP HEAD and GET requests and typical uses of HEAD, in a markdown file."
  "Summarize what DNS TTL means and how it affects record changes, in a markdown file."
  "Explain what an idempotent HTTP method is with examples, in a markdown file."
)
GOAL="${GOALS[$((RANDOM % ${#GOALS[@]}))]}"

# The API token stays in the shell environment only — sourced here,
# never printed.
if [[ -z "${TIMOTHY_API_TOKEN:-}" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/deploy/.env"
  set +a
fi
if [[ -z "${TIMOTHY_API_TOKEN:-}" ]]; then
  echo "canary: TIMOTHY_API_TOKEN not set and deploy/.env did not provide it" >&2
  exit 2
fi

auth=(-H "Authorization: Bearer ${TIMOTHY_API_TOKEN}" -H "Content-Type: application/json")

echo "canary: creating mission against ${BASE_URL}"
create_resp="$(curl -sf "${auth[@]}" -X POST "${BASE_URL}/v1/missions" \
  -d "{\"goal\": \"${GOAL}\", \"kind\": \"research\"}")"
id="$(echo "${create_resp}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
echo "canary: mission ${id}"

start=$(date +%s)
phase=""
status=""
while :; do
  now=$(date +%s)
  if (( now - start > TIMEOUT_SECS )); then
    echo "canary: FAIL — timed out after ${TIMEOUT_SECS}s (phase=${phase} status=${status})" >&2
    exit 1
  fi
  m="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}")"
  phase="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["phase"])')"
  status="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')"
  pending="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("pending_permission_tool") or "")')"
  echo "canary: ${phase}/${status} (t+$((now - start))s)${pending:+ [PARKED on ${pending}]}"
  case "${phase}/${status}" in
    done/*) break ;;
    failed/*)
      echo "canary: FAIL — mission failed" >&2
      curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}/events" | python3 -m json.tool | tail -40 >&2
      exit 1 ;;
  esac
  if [[ "${status}" == "paused" || "${status}" == "waiting_for_input" ]]; then
    echo "canary: FAIL — mission parked (${status}); a canary must run unattended" >&2
    exit 1
  fi
  sleep 5
done

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

print(f"canary: event counts: {json.dumps(kinds, sort_keys=True)}")
print(f"canary: {len(turns)} model turns, {total_ms/1000:.0f}s total model time")

failures = []
if parks > 0:
    failures.append(f"{parks} permission park(s) — canary must run unattended")
if not verified:
    failures.append("no harness-verified unit — done was claimed, never proven")
if len(turns) > 8:
    failures.append(f"{len(turns)} turns for a trivial mission — loop is not right-sized")
if failures:
    print("canary: FAIL — " + "; ".join(failures), file=sys.stderr)
    sys.exit(1)
print("canary: PASS")
PY
