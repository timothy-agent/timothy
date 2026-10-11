#!/usr/bin/env bash
# Kubernetes sandbox smoke: switches the local stack's sandboxd to the
# kind cluster (deploy/kind/compose.kind.yml), runs the coding canary
# through brain so a real mission execs inside a pod, then asserts a
# sandbox pod ran in the restricted namespace and none is left behind.
# The Docker backend is restored afterwards, pass or fail.
#
# Needs: make kind-up (cluster, manifests, images), the stack up,
# deploy/.env. The canary runs in a docker:27-cli container with
# python3 (no host Python), on the stack's network.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${KIND_CLUSTER:-timothy}"
NS="${SANDBOXD_K8S_NAMESPACE:-timothy-sandbox}"
KUBECTL=(kubectl --context "kind-${CLUSTER}")
COMPOSE=(docker compose -f "${REPO_ROOT}/deploy/docker-compose.yml")
COMPOSE_KIND=("${COMPOSE[@]}" -f "${REPO_ROOT}/deploy/kind/compose.kind.yml")

restore() {
  echo "kind-smoke: restoring sandboxd to the docker backend"
  "${COMPOSE[@]}" up -d --no-deps --force-recreate sandboxd >/dev/null 2>&1 || true
}
trap restore EXIT

mkdir -p "${REPO_ROOT}/build"
kind get kubeconfig --name "${CLUSTER}" --internal > "${REPO_ROOT}/build/kind-kubeconfig"
"${KUBECTL[@]}" get namespace "${NS}" >/dev/null

echo "kind-smoke: switching sandboxd to the kubernetes backend"
"${COMPOSE_KIND[@]}" up -d --no-deps --force-recreate sandboxd >/dev/null
for _ in $(seq 1 30); do
  health="$(docker run --rm --network timothy_timothy-sandbox curlimages/curl:8.11.1 -s http://sandboxd:8083/health || true)"
  if [[ "${health}" == *'"kubernetes":{"status":"ok"}'* ]]; then
    break
  fi
  sleep 2
done
if [[ "${health}" != *'"kubernetes":{"status":"ok"}'* ]]; then
  echo "kind-smoke: FAIL: sandboxd health never reported kubernetes ok: ${health}" >&2
  "${COMPOSE[@]}" logs --tail 30 sandboxd >&2
  exit 1
fi
echo "kind-smoke: sandboxd health: ${health}"

before="$("${KUBECTL[@]}" -n "${NS}" get events --field-selector reason=Started -o name | wc -l | tr -d ' ')"

echo "kind-smoke: running the coding canary through brain"
docker run --rm \
  -v "${REPO_ROOT}:/repo:ro" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --network timothy_timothy \
  -e CANARY_BASE_URL=http://brain:8080 \
  -e CANARY_TIMEOUT="${CANARY_TIMEOUT:-900}" \
  -w /repo docker:27-cli \
  sh -c 'apk add --no-cache -q bash curl python3 git >/dev/null && bash scripts/canary-coding.sh'

echo "kind-smoke: checking the sandbox namespace"
after="$("${KUBECTL[@]}" -n "${NS}" get events --field-selector reason=Started -o name | wc -l | tr -d ' ')"
if (( after <= before )); then
  echo "kind-smoke: FAIL: no sandbox pod started in ${NS} during the mission" >&2
  "${KUBECTL[@]}" -n "${NS}" get events >&2
  exit 1
fi
if "${KUBECTL[@]}" -n "${NS}" get pods -l timothy.owner=timothy -o name | grep -q .; then
  echo "kind-smoke: FAIL: sandbox pods left after mission completion:" >&2
  "${KUBECTL[@]}" -n "${NS}" get pods -l timothy.owner=timothy >&2
  exit 1
fi
echo "kind-smoke: PASS: mission ran in a pod under ${NS} (restricted) and the pod was removed"
