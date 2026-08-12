#!/usr/bin/env bash
# Research-mission canary: runs one golden research mission end-to-end
# against the live stack and asserts the harness contract holds for
# work that needs live web information — done within a research-sized
# iteration budget, zero human permission parks, every unit
# harness-verified (citations included) by the time the mission
# finishes. canary-mission.sh's trivial lookup goals never touch
# web_search/web_fetch or the citations check; this one exists to catch
# regressions in that path specifically. Run after any change to the
# research/citation-checking machinery (make canary-research).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_URL="${CANARY_BASE_URL:-http://localhost:${BRAIN_PORT:-8300}}"
TIMEOUT_SECS="${CANARY_TIMEOUT:-1200}"

# A different goal every run: repeating one fixed goal would let
# provider prompt caches, model memorization, and leftover artifacts
# from prior runs flatter the result — a pass must mean the harness
# worked NOW, on work it hasn't seen before. Each goal requires current
# web information (not resolvable from training data alone) and a
# cited markdown report, so a correct answer forces web_search/
# web_fetch use and exercises CheckCitations.
GOALS=(
  "Research the current stable versions and release dates of the three most-used JavaScript runtimes (Node.js, Deno, Bun) and write a cited markdown report."
  "Research the current status of the HTTP/3 and QUIC standards (RFC numbers, adoption by major browsers and CDNs) and write a cited markdown report."
  "Research the latest publicly disclosed critical CVEs affecting the OpenSSH server and write a cited markdown report on remediation."
  "Research the current pricing pages of AWS S3, Google Cloud Storage, and Azure Blob Storage for standard storage tiers and write a cited markdown report comparing them."
  "Research the current LTS release schedule and end-of-life dates for PostgreSQL and write a cited markdown report."
  "Research the current stable version and license terms of Docker Engine and Docker Desktop and write a cited markdown report."
  "Research the current W3C standardization status of WebGPU and its browser support matrix and write a cited markdown report."
  "Research the most recent security advisories published for the Redis project and write a cited markdown report on their impact and fixes."
  "Research the current pricing and rate limits of the OpenAI and Anthropic public APIs and write a cited markdown report comparing them."
  "Research the latest stable release of Kubernetes and its deprecation notices for the current minor version and write a cited markdown report."
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
  echo "canary-research: TIMOTHY_API_TOKEN not set and deploy/.env did not provide it" >&2
  exit 2
fi

auth=(-H "Authorization: Bearer ${TIMOTHY_API_TOKEN}" -H "Content-Type: application/json")

echo "canary-research: creating mission against ${BASE_URL}"
create_resp="$(curl -sf "${auth[@]}" -X POST "${BASE_URL}/v1/missions" \
  -d "{\"goal\": \"${GOAL}\", \"kind\": \"general\"}")"
id="$(echo "${create_resp}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
echo "canary-research: mission ${id}"

start=$(date +%s)
phase=""
status=""
m=""
while :; do
  now=$(date +%s)
  if (( now - start > TIMEOUT_SECS )); then
    echo "canary-research: FAIL — timed out after ${TIMEOUT_SECS}s (phase=${phase} status=${status})" >&2
    exit 1
  fi
  m="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}")"
  phase="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["phase"])')"
  status="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')"
  pending="$(echo "${m}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("pending_permission_tool") or "")')"
  echo "canary-research: ${phase}/${status} (t+$((now - start))s)${pending:+ [PARKED on ${pending}]}"
  case "${phase}/${status}" in
    done/*) break ;;
    failed/*)
      echo "canary-research: FAIL — mission failed" >&2
      curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}/events" | python3 -m json.tool | tail -40 >&2
      exit 1 ;;
  esac
  if [[ "${status}" == "paused" || "${status}" == "waiting_for_input" ]]; then
    echo "canary-research: FAIL — mission parked (${status}); a canary must run unattended" >&2
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

# mission.unit_verified fires once per check (artifacts/citations/
# verify_cmd/review) per unit, in order — retries on a failed check
# (e.g. an uncited URL) are allowed mid-run, but the LAST verified
# event recorded for each unit must be passed:true, or "done" was
# claimed without the harness ever proving it for that unit.
verified = [e for e in events if e["kind"] == "mission.unit_verified"]
last_by_unit = {}
for e in verified:
    unit = (e.get("payload") or {}).get("unit")
    last_by_unit[unit] = e

print(f"canary-research: event counts: {json.dumps(kinds, sort_keys=True)}")
print(f"canary-research: {len(turns)} model turns, {total_ms/1000:.0f}s total model time")

failures = []
if parks > 0:
    failures.append(f"{parks} permission park(s) — canary must run unattended")
if not verified:
    failures.append("no harness-verified unit — done was claimed, never proven")
unresolved = [u for u, e in last_by_unit.items() if not (e.get("payload") or {}).get("passed")]
if unresolved:
    failures.append(f"unit(s) {unresolved} never reached a passing final check")
# Research is heavier than the trivial general canary: gathering
# sources, writing, and citation-retry turns legitimately take more
# turns than a one-shot lookup, but an unbounded loop still means the
# harness is not right-sized for this workload.
if len(turns) > 16:
    failures.append(f"{len(turns)} turns for a research mission — loop is not right-sized")
if failures:
    print("canary-research: FAIL — " + "; ".join(failures), file=sys.stderr)
    sys.exit(1)
print("canary-research: deterministic checks PASS")
PY

# Cleanup runs on every PASS exit, including judge-skip paths — only a
# FAIL leaves the mission in place for debugging. Tolerated as
# best-effort: a cleanup failure must never flip a PASS to a FAIL.
cleanup_mission() {
  if curl -sf -X DELETE "${auth[@]}" "${BASE_URL}/v1/missions/${id}"; then
    echo "canary-research: cleaned up mission ${id}"
  else
    echo "canary-research: WARNING — cleanup of mission ${id} failed (non-fatal)" >&2
  fi
}

# --- Best-effort LLM judge -------------------------------------------
#
# Runs only after every deterministic check above has passed. The
# judge must never run on the same route/model that wrote the report —
# a model grading its own work is not an independent check — so it is
# gated on a separate CANARY_JUDGE_ROUTE env var the operator points at
# a different route. No route configured, no report retrievable, or
# any judge-path failure degrades to a warning and exit 0: the
# deterministic checks are the real gate, the judge is a bonus signal.
if [[ -z "${CANARY_JUDGE_ROUTE:-}" ]]; then
  echo "canary-research: judge skipped — CANARY_JUDGE_ROUTE not set" >&2
  cleanup_mission
  echo "canary-research: PASS"
  exit 0
fi

# The mission's declared artifact path(s) come from its own plan, not
# a guessed filename — Spec.Units[].artifacts is what CheckArtifacts
# and CheckCitations already verified against.
report_path="$(echo "${m}" | python3 -c '
import json, sys
m = json.load(sys.stdin)
for u in (m.get("spec") or {}).get("units", []):
    for a in u.get("artifacts") or []:
        print(a)
        raise SystemExit
')"
if [[ -z "${report_path}" ]]; then
  echo "canary-research: judge skipped — mission spec declared no artifact path" >&2
  cleanup_mission
  echo "canary-research: PASS"
  exit 0
fi

report="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/missions/${id}/files/${report_path}" || true)"
if [[ -z "${report}" ]]; then
  echo "canary-research: judge skipped — could not fetch report at ${report_path} via /v1/missions/{id}/files/{path}" >&2
  cleanup_mission
  echo "canary-research: PASS"
  exit 0
fi

echo "canary-research: judging report (${#report} bytes) on route ${CANARY_JUDGE_ROUTE}"

judge_prompt="$(cat <<PROMPT
Judge the following research report against this goal: "${GOAL}"

Respond with STRICT JSON only, no prose, no markdown fences:
{"label": "pass"|"fail", "critique": "<1-3 sentences>"}

Judge on:
- The report actually answers the stated goal.
- Every factual claim carries an inline citation.
- A "## Sources" section is present with real URLs.
- No hype or unsupported superlatives.

Report:
${report}
PROMPT
)"

# /v1/chat is SSE-only (there is no synchronous completion endpoint in
# this codebase) and the gateway's own /v1/stream is internal-only —
# unreachable from this host script — so /v1/chat on brain, with an
# explicit route override, is the only reachable one-shot path. A
# session auto-creates and is thrown away; no session bootstrapping is
# needed beyond that. curl -N disables buffering so SSE lines arrive as
# they stream instead of only after the connection closes.
judge_body="$(python3 -c '
import json, sys
print(json.dumps({"message": sys.argv[1], "route": sys.argv[2]}))
' "${judge_prompt}" "${CANARY_JUDGE_ROUTE}")"

judge_sse="$(curl -sf -N "${auth[@]}" -X POST "${BASE_URL}/v1/chat" -d "${judge_body}" || true)"
if [[ -z "${judge_sse}" ]]; then
  echo "canary-research: judge skipped — /v1/chat request failed or returned nothing" >&2
  cleanup_mission
  echo "canary-research: PASS"
  exit 0
fi

judge_verdict="$(CANARY_JUDGE_SSE="${judge_sse}" python3 <<'PY' || true
import json, os, re

sse = os.environ["CANARY_JUDGE_SSE"]
text = ""
for line in sse.splitlines():
    line = line.strip()
    if not line.startswith("data:"):
        continue
    payload = line[len("data:"):].strip()
    if not payload:
        continue
    try:
        event = json.loads(payload)
    except json.JSONDecodeError:
        continue
    if isinstance(event, dict) and "text" in event and isinstance(event["text"], str):
        text += event["text"]

# Model output may wrap the JSON in prose or fences despite the
# instruction — pull out the first {...} object rather than requiring
# an exact match.
match = re.search(r"\{.*\}", text, re.DOTALL)
if not match:
    raise SystemExit(1)
verdict = json.loads(match.group(0))
label = str(verdict.get("label", "")).strip().lower()
critique = str(verdict.get("critique", "")).strip()
if label not in ("pass", "fail"):
    raise SystemExit(1)
print(json.dumps({"label": label, "critique": critique}))
PY
)"

if [[ -z "${judge_verdict}" ]]; then
  echo "canary-research: judge skipped — could not parse a verdict from the judge's response" >&2
  cleanup_mission
  echo "canary-research: PASS"
  exit 0
fi

judge_label="$(echo "${judge_verdict}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["label"])')"
judge_critique="$(echo "${judge_verdict}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["critique"])')"

if [[ "${judge_label}" != "pass" ]]; then
  echo "canary-research: FAIL — judge verdict: fail — ${judge_critique}" >&2
  exit 1
fi
echo "canary-research: judge verdict: pass — ${judge_critique}"
cleanup_mission
echo "canary-research: PASS"
