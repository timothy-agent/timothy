#!/usr/bin/env bash
# Retrieval eval harness (issue #412): ingests a curated fixture set
# into a dedicated collection, runs a fixed query set through the real
# hybrid retrieval path, scores recall@5 and MRR, then deletes the
# collection. Self-contained: never depends on, or touches, whatever
# the operator already has in their live knowledge base.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE=(docker compose -f "${REPO_ROOT}/deploy/docker-compose.yml")
BASE_URL="${KB_EVAL_BASE_URL:-http://localhost:${BRAIN_PORT:-8300}}"
MIN_RECALL="${KB_EVAL_MIN_RECALL:-0.8}"
TIMEOUT_SECS="${KB_EVAL_TIMEOUT:-120}"

# A unique run tag every invocation: same reasoning as canary's varying
# goal set, a fixed collection name would let leftovers or provider
# caches flatter the result across runs.
RUN_TAG="kb-eval-$(date +%s)"

# The API token stays in the shell environment only: sourced here,
# never printed.
if [[ -z "${TIMOTHY_API_TOKEN:-}" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/deploy/.env"
  set +a
fi
if [[ -z "${TIMOTHY_API_TOKEN:-}" ]]; then
  echo "kb-eval: TIMOTHY_API_TOKEN not set and deploy/.env did not provide it" >&2
  exit 2
fi

auth=(-H "Authorization: Bearer ${TIMOTHY_API_TOKEN}" -H "Content-Type: application/json")

collection_id=""
results_file="$(mktemp)"
cleanup() {
  rm -f "${results_file}"
  if [[ -n "${collection_id}" ]]; then
    if curl -sf -X DELETE "${auth[@]}" "${BASE_URL}/v1/admin/kb/collections/${collection_id}" >/dev/null; then
      echo "kb-eval: cleaned up collection ${RUN_TAG} (${collection_id})"
    else
      echo "kb-eval: WARNING: cleanup of collection ${RUN_TAG} (${collection_id}) failed (non-fatal)" >&2
    fi
  fi
}
trap cleanup EXIT

echo "kb-eval: creating collection ${RUN_TAG} against ${BASE_URL}"
create_resp="$(curl -sf "${auth[@]}" -X POST "${BASE_URL}/v1/admin/kb/collections" \
  -d "{\"name\": \"${RUN_TAG}\", \"description\": \"kb-eval harness run, safe to delete\"}")"
collection_id="$(echo "${create_resp}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
echo "kb-eval: collection ${collection_id}"

# Upload every fixture document, tracking its document id so we can
# poll ingestion status by name.
declare -A doc_ids
for f in "${FIXTURE_DIR}"/fixtures/*.md; do
  title="$(basename "${f}" .md)"
  resp="$(curl -sf -H "Authorization: Bearer ${TIMOTHY_API_TOKEN}" \
    -X POST "${BASE_URL}/v1/admin/kb/collections/${collection_id}/documents" \
    -F "file=@${f};filename=${title}.md;type=text/markdown")"
  doc_id="$(echo "${resp}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
  doc_ids["${title}"]="${doc_id}"
  echo "kb-eval: uploaded ${title} (${doc_id})"
done

# Poll until every document is ready, failing fast by name on a
# failed or timed-out ingestion.
echo "kb-eval: waiting for ingestion"
start=$(date +%s)
while :; do
  now=$(date +%s)
  if (( now - start > TIMEOUT_SECS )); then
    echo "kb-eval: FAIL: ingestion timed out after ${TIMEOUT_SECS}s" >&2
    exit 1
  fi
  docs_resp="$(curl -sf "${auth[@]}" "${BASE_URL}/v1/admin/kb/collections/${collection_id}/documents")"
  all_ready=true
  for title in "${!doc_ids[@]}"; do
    doc_id="${doc_ids[${title}]}"
    status="$(DOCS_JSON="${docs_resp}" DOC_ID="${doc_id}" python3 -c '
import json, os
docs = json.loads(os.environ["DOCS_JSON"])["documents"]
for d in docs:
    if d["id"] == os.environ["DOC_ID"]:
        print(d["status"])
        break
else:
    print("missing")
')"
    if [[ "${status}" == "failed" ]]; then
      err="$(DOCS_JSON="${docs_resp}" DOC_ID="${doc_id}" python3 -c '
import json, os
docs = json.loads(os.environ["DOCS_JSON"])["documents"]
for d in docs:
    if d["id"] == os.environ["DOC_ID"]:
        print(d.get("error") or "")
        break
')"
      echo "kb-eval: FAIL: document ${title} failed ingestion: ${err}" >&2
      exit 1
    fi
    if [[ "${status}" != "ready" ]]; then
      all_ready=false
    fi
  done
  if [[ "${all_ready}" == "true" ]]; then
    break
  fi
  sleep 2
done
echo "kb-eval: all fixtures ready (t+$(( $(date +%s) - start ))s)"

# Run every query in eval.yaml through the real retrieval stack, via
# memoryd's kb-search directly (memoryd embeds the query itself),
# scoped to this run's collection with docker exec into the brain
# container (memoryd is not published on the host network).
echo "kb-eval: running queries"
query_idx=0
while IFS=$'\t' read -r qtext negative expects; do
  query_idx=$((query_idx + 1))
  payload="$(QTEXT="${qtext}" COLLECTION="${RUN_TAG}" python3 -c '
import json, os
print(json.dumps({"query": os.environ["QTEXT"], "mode": "hybrid", "k": 5, "collection_names": [os.environ["COLLECTION"]]}))
')"
  hits_json="$("${COMPOSE[@]}" exec -T brain wget -qO- \
    --header="Content-Type: application/json" \
    --post-data="${payload}" \
    http://memoryd:8082/v1/kb-search </dev/null)"
  # One JSON object per line (JSONL): the hits response can carry
  # embedded newlines in chunk content, which would otherwise split a
  # single result across multiple lines of a naive TSV file.
  QTEXT="${qtext}" EXPECTS="${expects}" NEGATIVE="${negative}" HITS_JSON="${hits_json}" python3 -c '
import json, os
print(json.dumps({
    "query": os.environ["QTEXT"],
    "expects": os.environ["EXPECTS"],
    "negative": os.environ["NEGATIVE"],
    "hits": json.loads(os.environ["HITS_JSON"]),
}))
' >> "${results_file}"
done < <(EVAL_YAML="$(cat "${FIXTURE_DIR}/eval.yaml")" python3 -c '
import os

lines = os.environ["EVAL_YAML"].splitlines()
query = None
expects = []
negative = False

def flush():
    if query is not None:
        joined = "|".join(expects) if expects else "-"
        flag = 1 if negative else 0
        print(f"{query}\t{flag}\t{joined}")

for line in lines:
    line = line.strip()
    if not line or line.startswith("#"):
        continue
    if line.startswith("query:"):
        flush()
        query = line[len("query:"):].strip()
        expects = []
        negative = False
    elif line.startswith("expect:"):
        expects.append(line[len("expect:"):].strip())
    elif line.startswith("negative:"):
        negative = line[len("negative:"):].strip().lower() == "true"
flush()
')

# Score recall@5 and MRR per query, matching expected docs by title
# (document_title in the kb-search response), then gate on aggregate
# recall@5. A negative query is scored as a miss if it returns any hit
# at all.
KB_EVAL_MIN_RECALL="${MIN_RECALL}" RESULTS_FILE="${results_file}" python3 <<'PY'
import json, os, sys

min_recall = float(os.environ["KB_EVAL_MIN_RECALL"])
results_path = os.environ["RESULTS_FILE"]

rows = []
recalls = []
reciprocal_ranks = []
failures = []

with open(results_path, encoding="utf-8") as fh:
    for line in fh:
        line = line.rstrip("\n")
        if not line:
            continue
        record = json.loads(line)
        qtext = record["query"]
        expects_raw = record["expects"]
        expects = [e for e in expects_raw.split("|") if e] if expects_raw != "-" else []
        negative = record["negative"] == "1"
        hits = record["hits"].get("results", [])
        titles = [h["document_title"] for h in hits]

        if negative:
            hit = len(titles) == 0
            recall = 1.0 if hit else 0.0
            rr = 0.0
            note = "OK (no hits)" if hit else f"MISS (got hits: {titles})"
        else:
            found = [t for t in expects if t in titles]
            recall = len(found) / len(expects) if expects else 1.0
            rr = 0.0
            for rank, t in enumerate(titles, start=1):
                if t in expects:
                    rr = 1.0 / rank
                    break
            note = f"found {found} of {expects}" if found else f"MISS (expected {expects}, got {titles})"

        recalls.append(recall)
        reciprocal_ranks.append(rr)
        rows.append((qtext, recall, rr, note))
        if recall < 1.0:
            failures.append((qtext, note))

agg_recall = sum(recalls) / len(recalls) if recalls else 0.0
agg_mrr = sum(reciprocal_ranks) / len(reciprocal_ranks) if reciprocal_ranks else 0.0

print(f"kb-eval: {len(rows)} queries scored")
for qtext, recall, rr, note in rows:
    print(f"kb-eval:   [{recall:.2f} recall, {rr:.2f} rr] \"{qtext}\": {note}")
print(f"kb-eval: aggregate recall@5 = {agg_recall:.3f} (threshold {min_recall:.3f})")
print(f"kb-eval: aggregate MRR = {agg_mrr:.3f}")

if agg_recall < min_recall:
    print("kb-eval: FAIL: aggregate recall@5 below threshold", file=sys.stderr)
    print("kb-eval: per-query breakdown of failures:", file=sys.stderr)
    for qtext, note in failures:
        print(f"kb-eval:   \"{qtext}\": {note}", file=sys.stderr)
    sys.exit(1)

print("kb-eval: PASS")
PY
