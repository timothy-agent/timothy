#!/usr/bin/env bash
# Decision model eval harness (issue #1245):
# Evaluates decision model performance vs LLM fallback on curated dataset
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

NETWORK="${TIMOTHY_DOCKER_NETWORK:-}"
if [[ -z "${NETWORK}" ]]; then
  NETWORK="$(docker inspect timothy-gateway-1 --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}' 2>/dev/null || echo "timothy_timothy")"
fi

GATEWAY_URL="${GATEWAY_URL:-http://gateway:8081}"

docker run --rm --network "${NETWORK}" -v "${REPO_ROOT}":/src -w /src \
  -v timothy-go-mod:/go/pkg/mod -v timothy-go-cache:/root/.cache/go-build \
  -e GOFLAGS=-buildvcs=false golang:1.26.9 \
  go run ./scripts/decide-eval/ -dataset ./scripts/decide-eval/dataset.json -gateway-url "${GATEWAY_URL}" "$@"
