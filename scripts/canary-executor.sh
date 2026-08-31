#!/usr/bin/env bash
# Delegated-executor canary: configures a "canary-executor" route with
# a single native provider/model chain entry, creates a coding mission
# against it with harness=claude-cli (D-051: harness is a mission
# column, not a chain entry), and asserts the D-052 executor protocol's
# own contract — executor.spawned/executor.result events actually fire,
# the CLI subprocess produced real usage, and the mission still went
# through LLM review and harness verification like any other coding
# mission. A native-served generate phase cannot quietly pass: the
# assertions demand executor.spawned and exactly one executor.result,
# so this canary fails loudly the moment the executor path itself breaks.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE=(docker compose -f "${REPO_ROOT}/deploy/docker-compose.yml")
BASE_URL="${CANARY_BASE_URL:-http://localhost:${BRAIN_PORT:-8300}}"
TIMEOUT_SECS="${CANARY_TIMEOUT:-900}"
FIXTURE=/workspace/canary-exec-fixture
ROUTE_NAME="canary-executor"
EXECUTOR_MODEL_FALLBACK="claude-haiku-4-5-20251001"

# A different goal every run, with a random suffix on top of that: two
# canary scripts (this one and canary-coding.sh) must never collide on
# the exact same goal string either, so the tag also makes cross-script
# collisions impossible, not just cross-run ones.
CASES=(
  "docs/EXECUTOR.md|Add a docs/EXECUTOR.md file with one short invented paragraph defining the term delegated executor. Write it from your own knowledge; no research needed."
  "NOTES.md|Add a NOTES.md file at the repository root with a single bullet point about this fixture repo's purpose."
  "docs/ARCHITECTURE.md|Add a docs/ARCHITECTURE.md file with one short paragraph describing this fixture repo's (fictional) architecture."
  ".gitattributes|Add a .gitattributes file at the repository root that sets text=auto for all files."
  "docs/ROADMAP.md|Add a docs/ROADMAP.md file with two short bullet points describing fictional next steps for this fixture repo."
  "LICENSE-NOTE.md|Add a LICENSE-NOTE.md file at the repository root with one short sentence noting this fixture repo has no real license."
  "about.html|Create an about.html page at the repository root describing this repository based on its README."
)
CASE="${CASES[$((RANDOM % ${#CASES[@]}))]}"
ARTIFACT="${CASE%%|*}"
GOAL_BASE="${CASE#*|}"
RUN_TAG="$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')"
GOAL="${GOAL_BASE} [run ${RUN_TAG}]"

# The API token stays in the shell environment only — sourced here,
# never printed.
if [[ -z "${TIMOTHY_API_TOKEN:-}" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/deploy/.env"
  set +a
fi
if [[ -z "${TIMOTHY_API_TOKEN:-}" ]]; then
  echo "canary-executor: TIMOTHY_API_TOKEN not set and deploy/.env did not provide it" >&2
  exit 2
fi

auth=(-H "Authorization: Bearer ${TIMOTHY_API_TOKEN}" -H "Content-Type: application/json")

# --- 1. discover a wire-compatible provider ---------------------------
#
# claude-cli's wire contract (D-051, validateHarnessWireFormat) accepts
# a provider whose driver=="anthropic" or whose options carry
# anthropic_base_url — mirrored here exactly so this never resolves the
# executor axis to a provider the gateway would then mark
# wire-incompatible.
echo "canary-executor: discovering a wire-compatible provider"
providers_resp="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/admin/providers")"
provider_line="$(PROVIDERS_JSON="${providers_resp}" python3 <<'PY'
import json, os
data = json.loads(os.environ["PROVIDERS_JSON"])
for p in data.get("providers", []):
    if not p.get("enabled") or p.get("kind") == "cli":
        continue
    opts = p.get("options") or {}
    if p.get("driver") == "anthropic" or opts.get("anthropic_base_url"):
        print(p["id"], p.get("default_model") or "")
        break
PY
)"
provider_id="${provider_line%% *}"
provider_default_model="${provider_line#* }"
if [[ -z "${provider_id}" ]]; then
  echo "canary-executor: FAIL — no enabled provider with driver=anthropic or options.anthropic_base_url set; configure one in Settings first" >&2
  exit 2
fi
# The chain entry also serves discover/plan/prove natively, so the
# model must be one the provider actually hosts — its own default,
# unless the operator overrides.
MODEL="${CANARY_EXECUTOR_MODEL:-${provider_default_model:-${EXECUTOR_MODEL_FALLBACK}}}"
echo "canary-executor: using provider ${provider_id} model ${MODEL}"

# --- 2. ensure the canary-executor route exists, then pin its chain ---
routes_resp="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/admin/routes")"
route_exists="$(ROUTES_JSON="${routes_resp}" ROUTE_NAME="${ROUTE_NAME}" python3 <<'PY'
import json, os
data = json.loads(os.environ["ROUTES_JSON"])
name = os.environ["ROUTE_NAME"]
print("yes" if any(r["name"] == name for r in data.get("routes", [])) else "no")
PY
)"
if [[ "${route_exists}" == "no" ]]; then
  echo "canary-executor: creating route ${ROUTE_NAME}"
  curl -sf "${auth[@]}" -X POST "${BASE_URL}/v1/admin/routes" \
    -d "{\"name\": \"${ROUTE_NAME}\", \"capability\": \"chat\"}" >/dev/null
else
  echo "canary-executor: route ${ROUTE_NAME} already exists"
fi

# A single native chain entry (D-051 rework: harness is no longer a
# chain field). discover/plan/prove always stream natively over this
# same route; generate alone dispatches to the harness named on the
# mission itself. Silent fail-over to native during generate cannot mask
# an executor break — the assertions below demand executor.spawned and
# exactly one executor.result, so a native-served generate phase still
# fails the canary loudly.
chain_json="$(PROVIDER_ID="${provider_id}" MODEL="${MODEL}" python3 <<'PY'
import json, os
pid, model = os.environ["PROVIDER_ID"], os.environ["MODEL"]
print(json.dumps([
    {"provider_id": pid, "model": model},
]))
PY
)"
patch_body="$(python3 -c 'import json,sys; print(json.dumps({"chain": json.loads(sys.argv[1]), "strategy": "ordered", "enabled": True}))' "${chain_json}")"
curl -sf "${auth[@]}" -X PATCH "${BASE_URL}/v1/admin/routes/${ROUTE_NAME}" -d "${patch_body}" >/dev/null
echo "canary-executor: configured route ${ROUTE_NAME} chain: ${chain_json}"

# --- 3. seed the fixture repo (same pattern as canary-coding.sh) ------
echo "canary-executor: seeding fixture repo at ${FIXTURE} (inside brain)"
"${COMPOSE[@]}" exec -T brain sh -c "
  rm -rf ${FIXTURE} &&
  git init -q -b main ${FIXTURE} &&
  cd ${FIXTURE} &&
  printf '# Canary Executor Fixture\n\nTiny repo the delegated-executor canary works against.\n' > README.md &&
  git add README.md &&
  git -c user.name=timothy -c user.email=timothy@localhost commit -q -m 'add readme'
"

# --- 4. create the mission and poll it (same handling as canary-coding.sh) ---
echo "canary-executor: creating mission against ${BASE_URL}"
create_resp="$(curl -sf "${auth[@]}" -X POST "${BASE_URL}/v1/missions" \
  -d "{\"goal\": \"${GOAL}\", \"kind\": \"coding\", \"repo_path\": \"${FIXTURE}\", \"route\": \"${ROUTE_NAME}\", \"harness\": \"claude-cli\"}")"
id="$(echo "${create_resp}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
echo "canary-executor: mission ${id}"

start=$(date +%s)
phase=""
status=""
while :; do
  now=$(date +%s)
  if (( now - start > TIMEOUT_SECS )); then
    echo "canary-executor: FAIL — timed out after ${TIMEOUT_SECS}s (phase=${phase} status=${status})" >&2
    exit 1
  fi
  m="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}")"
  phase="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["phase"])')"
  status="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')"
  pending="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("pending_permission_tool") or "")')"
  echo "canary-executor: ${phase}/${status} (t+$((now - start))s)${pending:+ [PARKED on ${pending}]}"
  case "${phase}/${status}" in
    done/*) break ;;
    failed/*)
      echo "canary-executor: FAIL — mission failed" >&2
      curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}/events" | python3 -m json.tool | tail -40 >&2
      exit 1 ;;
  esac
  if [[ "${status}" == "paused" || "${status}" == "waiting_for_input" ]]; then
    echo "canary-executor: FAIL — mission parked (${status}); a canary must run unattended" >&2
    exit 1
  fi
  sleep 5
done

worktree="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("worktree") or "")')"
branch="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("branch") or "")')"

# --- 5. assert the executor-specific event contract -------------------
events="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}/events")"
CANARY_EVENTS="${events}" python3 <<'PY'
import json, os, sys
events = json.loads(os.environ["CANARY_EVENTS"])["events"]
kinds = {}
for e in events:
    kinds[e["kind"]] = kinds.get(e["kind"], 0) + 1

parks = kinds.get("mission.permission_requested", 0)
verified = [e for e in events if e["kind"] == "mission.unit_verified"
            and (e.get("payload") or {}).get("passed") is True]
results = [e for e in events if e["kind"] == "executor.result"]

print(f"canary-executor: event counts: {json.dumps(kinds, sort_keys=True)}")

failures = []
if kinds.get("executor.spawned", 0) < 1:
    failures.append("no executor.spawned event — the delegated executor path never ran")
if len(results) != 1:
    failures.append(f"expected exactly 1 executor.result, got {len(results)}")
if kinds.get("executor.died", 0) > 0:
    failures.append(f"{kinds['executor.died']} executor.died event(s) — the executor crashed or lost its transport")
if kinds.get("executor.idle_killed", 0) > 0:
    failures.append(f"{kinds['executor.idle_killed']} executor.idle_killed event(s) — the executor stalled")
if kinds.get("executor.auth_failed", 0) > 0:
    failures.append(f"{kinds['executor.auth_failed']} executor.auth_failed event(s) — the executor's own credential is broken")
if parks > 0:
    failures.append(f"{parks} permission park(s) — canary must run unattended")
if not verified:
    failures.append("no harness-verified unit — done was claimed, never proven")
if kinds.get("mission.review_skipped", 0) > 0:
    failures.append("review was skipped — coding missions must always be LLM-reviewed")
if kinds.get("mission.review_verdict", 0) == 0:
    failures.append("no review verdict — the coding review path never ran")

result_payload = (results[0].get("payload") or {}) if len(results) == 1 else {}
if result_payload.get("status") != "DONE":
    failures.append(f"executor.result status was {result_payload.get('status')!r}, expected DONE")
usage = result_payload.get("usage") or {}
if not usage or not usage.get("output_tokens"):
    failures.append("executor.result usage missing or output_tokens not > 0 — no evidence the CLI actually ran a model turn")

if failures:
    print("canary-executor: FAIL — " + "; ".join(failures), file=sys.stderr)
    sys.exit(1)

cost = usage.get("cost_usd")
print(f"canary-executor: executor usage — input={usage.get('input_tokens')} "
      f"output={usage.get('output_tokens')} cache_read={usage.get('cache_read')} "
      f"cache_write={usage.get('cache_write')} cost_usd={cost if cost is not None else 'null'} "
      f"duration_ms={result_payload.get('duration_ms')}")
PY

# --- 6. independent witness: re-check worktree/branch/artifact --------
if [[ -z "${worktree}" || -z "${branch}" ]]; then
  echo "canary-executor: FAIL — mission has no worktree/branch (worktree='${worktree}' branch='${branch}')" >&2
  exit 1
fi
# Coding missions self-init a standalone repo in the worktree (the
# fixture repo is workspace metadata, never the branch's home), so the
# branch and the committed artifact are both witnessed in the worktree
# repo itself.
"${COMPOSE[@]}" exec -T brain sh -c "
  git -C '${worktree}' rev-parse --verify --quiet '${branch}' >/dev/null &&
  test -s '${worktree}/${ARTIFACT}' &&
  git -C '${worktree}' ls-tree --name-only '${branch}' -- '${ARTIFACT}' | grep -q .
" || {
  echo "canary-executor: FAIL — branch ${branch}, ${worktree}/${ARTIFACT}, or its commit missing in container" >&2
  exit 1
}

echo "canary-executor: PASS"

# Cleanup only runs once every check above has passed (set -e would
# have already aborted the script on any failure) — a failed mission is
# left in place for debugging. The route itself is left configured
# (idempotent ensure handles the next run); only the mission and
# fixture are torn down, mirroring canary-coding.sh. DELETE requires a
# terminal mission, so cancel first — a 409 on an already-finished
# mission is expected and ignored.
curl -s -o /dev/null -X POST "${auth[@]}" "${BASE_URL}/v1/missions/${id}/cancel" || true
if curl -sf -X DELETE "${auth[@]}" "${BASE_URL}/v1/missions/${id}"; then
  echo "canary-executor: cleaned up mission ${id}"
else
  echo "canary-executor: WARNING — cleanup of mission ${id} failed (non-fatal)" >&2
fi
